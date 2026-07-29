package compose

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kovach/p2ser/internal/scheduler"
)

// maxReplicas is the maximum number of replicas allowed (M-7: prevent resource exhaustion)
const maxReplicas = 100

// ComposeFile представляє структуру docker-compose.yaml
type ComposeFile struct {
	Version  string             `yaml:"version"`
	Services map[string]Service `yaml:"services"`
}

var envRe = regexp.MustCompile(`(?:\$\{([a-zA-Z_][a-zA-Z0-9_]*)(?:(?:-(?:)|:-)([^}]*))?\})|(?:\$([a-zA-Z_][a-zA-Z0-9_]*))`)

func interpolate(s string, envMap map[string]string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		matches := envRe.FindStringSubmatch(m)
		varName := matches[1]
		defaultVal := matches[2]
		if varName == "" {
			varName = matches[3]
		}

		// M-4: only use explicitly provided envMap values
		// Do NOT fall back to os.Getenv() to prevent host secret leakage into containers
		val := envMap[varName]
		if val != "" {
			return val
		}
		return defaultVal
	})
}

type Service struct {
	Image        string        `yaml:"image"`
	User         string        `yaml:"user"`
	Ports        []string      `yaml:"ports"`
	Volumes      []string      `yaml:"volumes"`
	Environment  interface{}   `yaml:"environment"`
	Deploy       *DeployConfig `yaml:"deploy"`
	DependsOn    interface{}   `yaml:"depends_on"`
	XK1nStandby  int           `yaml:"x-k1n-standby"`
	XUsernsRemap bool          `yaml:"x-userns-remap"`
}

type DeployConfig struct {
	Replicas int `yaml:"replicas"`
}

// ParseComposeFile перетворює docker-compose.yaml у масив Pod-ів (Пункт 6.1)
func ParseComposeFile(projectName string, content []byte, envMap map[string]string, workingDir string) ([]scheduler.Pod, error) {
	var compose ComposeFile
	if err := yaml.Unmarshal(content, &compose); err != nil {
		return nil, fmt.Errorf("помилка парсингу compose-файлу: %v", err)
	}

	var pods []scheduler.Pod

	for name, svc := range compose.Services {
		replicas := 1
		if svc.Deploy != nil && svc.Deploy.Replicas > 0 {
			replicas = svc.Deploy.Replicas
		}
		// M-7: cap replicas to prevent resource exhaustion
		if replicas > maxReplicas {
			replicas = maxReplicas
		}

		deps := make(map[string]scheduler.DependsOnConfig)
		if svc.DependsOn != nil {
			switch v := svc.DependsOn.(type) {
			case []interface{}:
				for _, dep := range v {
					if depStr, ok := dep.(string); ok {
						deps[depStr] = scheduler.DependsOnConfig{Condition: "service_started"}
					}
				}
			case map[string]interface{}:
				for k, val := range v {
					if depMap, ok := val.(map[string]interface{}); ok {
						cond, _ := depMap["condition"].(string)
						deps[k] = scheduler.DependsOnConfig{Condition: cond}
					}
				}
			}
		}

		var envList []string
		if svc.Environment != nil {
			switch v := svc.Environment.(type) {
			case []interface{}:
				for _, e := range v {
					if strEnv, ok := e.(string); ok {
						envList = append(envList, interpolate(strEnv, envMap))
					}
				}
			case map[string]interface{}:
				for k, val := range v {
					if s, ok := val.(string); ok {
						envList = append(envList, fmt.Sprintf("%s=%s", k, interpolate(s, envMap)))
					}
				}
			}
		}

		// M-1: Auto-generate a random PostgreSQL password instead of hardcoded literal
		// to prevent password exposure when code is publicly available.
		if strings.Contains(svc.Image, "postgres") {
			hasPass := false
			for i, e := range envList {
				if strings.HasPrefix(e, "POSTGRES_PASSWORD=") {
					if len(e) == 18 { // "POSTGRES_PASSWORD=" (empty password)
						envList[i] = "POSTGRES_PASSWORD=" + generateSecurePassword()
					}
					hasPass = true
					break
				}
			}
			if !hasPass {
				envList = append(envList, "POSTGRES_PASSWORD="+generateSecurePassword())
			}
		}

		var volList []string
		for _, v := range svc.Volumes {
			interpolated := interpolate(v, envMap)
			if strings.HasPrefix(interpolated, "./") || 
			   strings.HasPrefix(interpolated, "../") {
				if workingDir != "" {
					interpolated = filepath.Join(workingDir, interpolated)
				}
			}
			volList = append(volList, interpolated)
		}

		imageName := interpolate(svc.Image, envMap)
		if imageName == "" {
			imageName = fmt.Sprintf("%s-%s", projectName, name)
		}

		for i := 0; i < replicas; i++ {
			podID := fmt.Sprintf("%s-active-%d", name, i)

			runAsUser := svc.User
			if runAsUser == "" {
				// C-13: Default to non-root execution unless explicitly specified
				runAsUser = "1000:1000"
			}

			pod := scheduler.Pod{
				ID:          podID,
				BaseID:      podID, // 6.4: Зберігаємо базовий ідентифікатор
				Image:       imageName,
				RunAsUser:   runAsUser,
				Ports:       svc.Ports,
				Volumes:     volList,
				UsernsRemap: svc.XUsernsRemap,
				Status:      "Pending",
				App:         name,
				Role:        "Active", // Пункт 6.2: Роль
				DependsOn:   deps,     // 6.3: Зберігаємо залежності
				Env:         envList,
			}
			pods = append(pods, pod)
		}

		// Резервні репліки (Standby)
		for i := 0; i < svc.XK1nStandby; i++ {
			podID := fmt.Sprintf("%s-standby-%d", name, i)

			pod := scheduler.Pod{
				ID:          podID,
				BaseID:      podID, // 6.4
				Image:       imageName,
				RunAsUser:   svc.User,
				Ports:       svc.Ports,
				Volumes:     volList,
				UsernsRemap: svc.XUsernsRemap,
				Status:      "Pending",
				App:         name,
				Role:        "Standby", // Пункт 6.2: Роль резерву
				Env:         envList,
			}
			pods = append(pods, pod)
		}
	}

	return pods, nil
}

// generateSecurePassword generates a cryptographically random password (M-1 fix).
func generateSecurePassword() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// Fallback: should not happen
		return "p2ser-change-me-" + base64.URLEncoding.EncodeToString([]byte("fallback"))
	}
	return base64.URLEncoding.EncodeToString(b)
}
