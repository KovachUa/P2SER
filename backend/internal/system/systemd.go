package system

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// InstallSystemdService створює systemd юніт для автоматичного запуску P2SER після збоїв по живленню
func InstallSystemdService() {
	execPath, err := os.Executable()
	if err != nil {
		log.Fatalf("Не вдалося отримати шлях до бінарника: %v", err)
	}
	execPath, err = filepath.Abs(execPath)
	if err != nil {
		log.Fatalf("Не вдалося отримати абсолютний шлях: %v", err)
	}

	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("Не вдалося отримати робочу директорію: %v", err)
	}

	// Формуємо вміст сервіс-файлу
	// WorkingDirectory гарантує, що p2ser знайде свій config.yaml та папку з даними
	serviceContent := fmt.Sprintf(`[Unit]
Description=P2SER Edge Orchestrator
After=network.target containerd.service

[Service]
Type=simple
ExecStart=%s start
User=root
Group=root
WorkingDirectory=%s
Restart=always
RestartSec=5
LimitNOFILE=65536
LimitNPROC=65536
LimitCORE=0

[Install]
WantedBy=multi-user.target
`, execPath, workDir)

	servicePath := "/etc/systemd/system/p2ser.service"

	// Перевіряємо права доступу (потрібен root)
	if os.Geteuid() != 0 {
		fmt.Println("⚠️ Попередження: Встановлення сервісу вимагає прав адміністратора.")
		fmt.Printf("Якщо виникне помилка `permission denied`, зупиніть і запустіть через: sudo %s install\n\n", execPath)
	}

	fmt.Printf("Створення systemd-сервісу за адресою %s...\n", servicePath)
	err = os.WriteFile(servicePath, []byte(serviceContent), 0644)
	if err != nil {
		log.Fatalf("Помилка запису файлу сервісу: %v", err)
	}

	fmt.Println("Перезавантаження конфігурації systemd...")
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		log.Printf("Попередження: daemon-reload не вдався (це нормально, якщо ви в Docker): %v", err)
	}

	fmt.Println("Активація автозапуску (enable)...")
	if err := exec.Command("systemctl", "enable", "p2ser").Run(); err != nil {
		log.Printf("Попередження: systemctl enable не вдався: %v", err)
	}

	fmt.Println("Запуск сервісу у фоні...")
	if err := exec.Command("systemctl", "start", "p2ser").Run(); err != nil {
		log.Printf("Попередження: systemctl start не вдався: %v", err)
	}

	fmt.Println("\n✓ P2SER успішно встановлено як системний сервіс!")
	fmt.Println("Тепер він автоматично запускатиметься при кожному завантаженні Raspberry Pi (навіть якщо вимикали світло).")
	fmt.Println("Корисні команди:")
	fmt.Println("  Перевірити статус: systemctl status p2ser")
	fmt.Println("  Дивитися логи:     journalctl -fu p2ser")
}
