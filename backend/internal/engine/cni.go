package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kovach/p2ser/internal/config"
)

// EnsureCNIPlugins завантажує CNI плагіни, якщо їх немає (поведінка як у k3s)
func EnsureCNIPlugins() error {
	cniDir := "/opt/p2ser/cni/bin"
	if _, err := os.Stat(filepath.Join(cniDir, "bridge")); err == nil {
		return nil
	}
	fmt.Println("P2SER: Встановлення вбудованих CNI плагінів (як у k3s)...")
	os.MkdirAll(cniDir, 0755)

	cmd := exec.Command("bash", "-c", fmt.Sprintf(`
		cd /tmp &&
		wget -qO cni.tgz https://github.com/containernetworking/plugins/releases/download/v1.3.0/cni-plugins-linux-amd64-v1.3.0.tgz &&
		echo "754a71ed60a4bd08726c3af705a7d55ee3df03122b12e389fdba4bea35d7dd7e  cni.tgz" | sha256sum -c - &&
		tar -C %s -xzf cni.tgz &&
		rm cni.tgz
	`, cniDir))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("помилка завантаження CNI: %v", err)
	}
	fmt.Println("P2SER: CNI плагіни успішно встановлено!")
	return nil
}

// SetupCNINetwork викликає bridge плагін для підключення контейнера до мережі
func SetupCNINetwork(containerID, netnsPath string) (string, error) {
	cniDir := "/opt/p2ser/cni/bin"

	bridgeName := "p2ser0"
	podSubnet := "10.88.0.0/16"
	if config.GlobalConfig != nil {
		if config.GlobalConfig.BridgeName != "" {
			bridgeName = config.GlobalConfig.BridgeName
		}
		if config.GlobalConfig.PodSubnet != "" {
			podSubnet = config.GlobalConfig.PodSubnet
		}
	}

	cniConf := []byte(fmt.Sprintf(`{
		"cniVersion": "1.0.0",
		"name": "p2ser-net",
		"type": "bridge",
		"bridge": "%s",
		"isGateway": true,
		"ipMasq": true,
		"hairpinMode": true,
		"ipam": {
			"type": "host-local",
			"subnet": "%s",
			"routes": [ { "dst": "0.0.0.0/0" } ]
		}
	}`, bridgeName, podSubnet))

	cmd := exec.Command(filepath.Join(cniDir, "bridge"))
	cmd.Env = []string{
		"CNI_COMMAND=ADD",
		"CNI_CONTAINERID=" + containerID,
		"CNI_NETNS=" + netnsPath,
		"CNI_IFNAME=eth0",
		"CNI_PATH=" + cniDir,
		"PATH=/sbin:/usr/sbin:/usr/local/sbin:/bin:/usr/bin:" + os.Getenv("PATH"),
	}
	cmd.Stdin = bytes.NewReader(cniConf)
	
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("CNI ADD error: %v, out: %s", err, string(out))
	}

	// Парсимо JSON щоб дістати IP
	var result struct {
		IPs []struct {
			Address string `json:"address"`
		} `json:"ips"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("failed to parse CNI output: %v, out: %s", err, string(out))
	}

	if len(result.IPs) > 0 {
		return result.IPs[0].Address, nil
	}
	return "", nil
}

// TeardownCNINetwork видаляє контейнер з мережі
func TeardownCNINetwork(containerID, netnsPath string) error {
	cniDir := "/opt/p2ser/cni/bin"

	config := []byte(`{
		"cniVersion": "1.0.0",
		"name": "p2ser-net",
		"type": "bridge",
		"bridge": "p2ser0",
		"ipam": {
			"type": "host-local",
			"subnet": "10.88.0.0/16"
		}
	}`)

	cmd := exec.Command(filepath.Join(cniDir, "bridge"))
	cmd.Env = []string{
		"CNI_COMMAND=DEL",
		"CNI_CONTAINERID=" + containerID,
		"CNI_NETNS=" + netnsPath,
		"CNI_IFNAME=eth0",
		"CNI_PATH=" + cniDir,
		"PATH=/sbin:/usr/sbin:/usr/local/sbin:/bin:/usr/bin:" + os.Getenv("PATH"),
	}
	cmd.Stdin = bytes.NewReader(config)
	
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("CNI DEL error: %v, out: %s", err, string(out))
	}
	return nil
}
