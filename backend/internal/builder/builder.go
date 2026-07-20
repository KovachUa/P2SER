package builder

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

	// 3. Запускаємо збірку образів через docker compose
	log.Printf("Builder: Запускаю 'docker compose build'...")
	cmdBuild := exec.Command("docker", "compose", "-p", projectName, "build")
	cmdBuild.Dir = projectPath
	cmdBuild.Stdout = os.Stdout
	cmdBuild.Stderr = os.Stderr
	if err := cmdBuild.Run(); err != nil {
		return nil, fmt.Errorf("помилка збірки образів: %v", err)
	}

	// 4. Парсимо docker-compose з підстановкою змінних з .env
	pods, err := compose.ParseComposeFile(projectName, composeData, envMap, projectPath)
	if err != nil {
		return nil, fmt.Errorf("помилка парсингу compose-файлу: %v", err)
	}

	// 5. Автоматично імпортуємо всі образи в containerd кластера P2SER
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
		cmdImport := exec.Command("sudo", "ctr", "-n", "p2ser", "images", "import", "-") // sudo потрібен якщо P2SER не від root
		
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
