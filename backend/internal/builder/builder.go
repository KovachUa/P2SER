package builder

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kovach/p2ser/internal/compose"
	"github.com/kovach/p2ser/internal/scheduler"
)

// ParseEnvFile читає .env файл та повертає map зі змінними
func ParseEnvFile(envPath string) map[string]string {
	envMap := make(map[string]string)
	data, err := ioutil.ReadFile(envPath)
	if err != nil {
		return envMap // Якщо файлу немає, повертаємо порожній map
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			envMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return envMap
}

// BuildAndLoad виконує збірку проекту та завантаження образів у кластер
func BuildAndLoad(projectPath string) ([]scheduler.Pod, error) {
	log.Printf("Builder: Починаємо процес збірки для проекту %s", projectPath)

	// 1. Читаємо .env файл, якщо він є
	envMap := ParseEnvFile(filepath.Join(projectPath, ".env"))

	// 2. Читаємо docker-compose.yml
	composePath := filepath.Join(projectPath, "docker-compose.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		composePath = filepath.Join(projectPath, "docker-compose.yaml")
	}
	composeData, err := ioutil.ReadFile(composePath)
	if err != nil {
		return nil, fmt.Errorf("не вдалося знайти docker-compose.yml: %v", err)
	}

	projectName := filepath.Base(projectPath)
	// Очищаємо ім'я проекту від спецсимволів
	projectName = strings.ReplaceAll(projectName, "_", "")
	projectName = strings.ReplaceAll(projectName, ".", "")
	projectName = strings.ToLower(projectName)

	// 3. Парсимо docker-compose.yml для визначення необхідності збірки
	var composeFile struct {
		Services map[string]struct {
			Image string      `yaml:"image"`
			Build interface{} `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeData, &composeFile); err != nil {
		return nil, fmt.Errorf("помилка попереднього парсингу compose-файлу: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 4. Ізольована збірка образів (C-14)
	for name, svc := range composeFile.Services {
		if svc.Build != nil {
			log.Printf("Builder: Збірка образу для сервісу %s...", name)
			buildCtx := "."
			dockerfile := "Dockerfile"

			switch b := svc.Build.(type) {
			case string:
				buildCtx = b
			case map[interface{}]interface{}:
				if ctxStr, ok := b["context"].(string); ok {
					buildCtx = ctxStr
				}
				if dfStr, ok := b["dockerfile"].(string); ok {
					dockerfile = dfStr
				}
			case map[string]interface{}:
				if ctxStr, ok := b["context"].(string); ok {
					buildCtx = ctxStr
				}
				if dfStr, ok := b["dockerfile"].(string); ok {
					dockerfile = dfStr
				}
			}

			imgName := svc.Image
			if imgName == "" {
				imgName = fmt.Sprintf("%s-%s", projectName, name)
			}

			args := []string{
				"build",
				"-t", imgName,
				"-f", filepath.Join(projectPath, buildCtx, dockerfile),
				"--network=none", // C-14: No network access during build
				"--memory=1g",    // C-14: Memory limit
				filepath.Join(projectPath, buildCtx),
			}
			
			cmdBuild := exec.CommandContext(ctx, "docker", args...)
			cmdBuild.Dir = projectPath
			cmdBuild.Stdout = os.Stdout
			cmdBuild.Stderr = os.Stderr
			if err := cmdBuild.Run(); err != nil {
				return nil, fmt.Errorf("помилка ізольованої збірки для %s: %v", name, err)
			}
		}
	}

	// 5. Парсимо docker-compose з підстановкою змінних з .env для планувальника
	pods, err := compose.ParseComposeFile(projectName, composeData, envMap, projectPath)
	if err != nil {
		return nil, fmt.Errorf("помилка парсингу compose-файлу: %v", err)
	}

	// 6. Автоматично імпортуємо всі образи в containerd кластера P2SER
	for _, pod := range pods {
		if pod.Image == "" {
			continue
		}
		
		log.Printf("Builder: Експорт образу %s та імпорт у containerd (p2ser)...", pod.Image)
		
		// Переконуємось, що образ має префікс registry для правильного парсингу в P2SER
		imageName := pod.Image
		if !strings.Contains(imageName, "/") {
			imageName = "docker.io/library/" + imageName
			exec.Command("docker", "tag", pod.Image, imageName).Run()
		}

		cmdSave := exec.Command("docker", "save", imageName)
		// C-14: Видалено sudo. Вузол має запускатися з необхідними правами (узгоджено з H-11)
		cmdImport := exec.Command("ctr", "-n", "p2ser", "images", "import", "-") 
		
		pipe, err := cmdSave.StdoutPipe()
		if err != nil {
			log.Printf("Builder: Помилка створення pipe для %s: %v", pod.Image, err)
			continue
		}
		
		cmdImport.Stdin = pipe
		cmdImport.Stdout = os.Stdout
		cmdImport.Stderr = os.Stderr
		
		if err := cmdSave.Start(); err != nil {
			log.Printf("Builder: Помилка docker save для %s: %v", pod.Image, err)
			continue
		}
		if err := cmdImport.Start(); err != nil {
			log.Printf("Builder: Помилка ctr import для %s: %v", pod.Image, err)
			continue
		}
		
		cmdSave.Wait()
		cmdImport.Wait()
		log.Printf("Builder: Образ %s успішно завантажено в кластер!", pod.Image)
	}

	return pods, nil
}
