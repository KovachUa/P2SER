package deploy

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

// DeployCompose відправляє локальний docker-compose.yaml в кластер P2SER (Пункт 9.4)
func DeployCompose(composePath string, apiEndpoint string, apiToken string) {
	fmt.Printf("Читання маніфесту %s...\n", composePath)
	data, err := os.ReadFile(composePath)
	if err != nil {
		log.Fatalf("Помилка читання файлу %s: %v", composePath, err)
	}

	url := fmt.Sprintf("%s/compose", apiEndpoint)
	fmt.Printf("Відправка маніфесту в кластер (%s)...\n", url)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		log.Fatalf("Помилка створення запиту: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-yaml")
	
	absPath, _ := filepath.Abs(composePath)
	req.Header.Set("X-Working-Dir", filepath.Dir(absPath))
	
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Помилка відправки запиту до кластера: %v\nПереконайтеся, що вузол P2SER запущений і API доступний.", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("✓ Маніфест успішно прийнято кластером!")
		if len(bodyBytes) > 0 {
			fmt.Printf("Відповідь: %s\n", string(bodyBytes))
		}
		fmt.Println("Планувальник (scheduler.Scheduler) автоматично завантажить образи та розподілить контейнери по вузлах (Zero-Touch Deployment).")
	} else {
		log.Fatalf("Помилка кластера (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}
}

// DeploySource пакує директорію в ZIP і відправляє на ендпоінт /upload
func DeploySource(dirPath string, apiEndpoint string, apiToken string) {
	fmt.Printf("Пакування директорії %s...\n", dirPath)
	
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	
	part, err := writer.CreateFormFile("project", "project.zip")
	if err != nil {
		log.Fatalf("Помилка створення форми: %v", err)
	}

	zipWriter := zip.NewWriter(part)
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}
		zipFile, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}
		fsFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fsFile.Close()
		_, err = io.Copy(zipFile, fsFile)
		return err
	})
	if err != nil {
		log.Fatalf("Помилка пакування файлів: %v", err)
	}
	zipWriter.Close()
	writer.Close()

	url := fmt.Sprintf("%s/upload", apiEndpoint)
	fmt.Printf("Відправка архіву в кластер для автоматичної збірки (%s)...\n", url)

	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		log.Fatalf("Помилка створення запиту: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Помилка відправки в кластер: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("✓ Проект успішно прийнято та зібрано кластером!")
		fmt.Printf("Відповідь: %s\n", string(bodyBytes))
	} else {
		log.Fatalf("Помилка кластера (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}
}

// DeployGit відправляє Git URL в кластер для збірки
func DeployGit(gitUrl string, branch string, env map[string]string, apiEndpoint string, apiToken string) {
	fmt.Printf("Запит кластеру на клонування %s (гілка: %s)...\n", gitUrl, branch)

	payload := map[string]interface{}{
		"url":    gitUrl,
		"branch": branch,
		"env":    env,
	}
	payloadBytes, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/deploy-git", apiEndpoint)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		log.Fatalf("Помилка створення запиту: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Помилка відправки в кластер: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("✓ Кластер успішно склонував і запустив Git-проект!")
		fmt.Printf("Відповідь: %s\n", string(bodyBytes))
	} else {
		log.Fatalf("Помилка кластера (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}
}
