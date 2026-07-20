package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// NodeConfig зберігає параметри запуску вузла (Пункт 9.2)
type NodeConfig struct {
	Token    string `yaml:"token"`
	UIToken  string `yaml:"ui_token"`
	Name     string `yaml:"name"`
	Port     int    `yaml:"port"`
	RaftPort int    `yaml:"raft_port"`
	DataDir  string `yaml:"data_dir"`
	Arch     string `yaml:"arch"`
	EdgeMode bool   `yaml:"edge_mode"`

	// Налаштування для Ingress (Пункт 7.3)
	VPSEndpoint string `yaml:"vps_endpoint"`
	VPSPubKey   string `yaml:"vps_pubkey"`
	VPSPrivKey  string `yaml:"vps_privkey"`
	VPSTunnelIP string `yaml:"vps_tunnel_ip"`
}

// LoadConfig завантажує конфігурацію з файлу
func LoadConfig(path string) (*NodeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg NodeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig зберігає конфігурацію у файл
func SaveConfig(path string, cfg *NodeConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	// Важливо: 0600 (тільки власник має доступ), оскільки тут зберігається токен і приватні ключі
	return os.WriteFile(path, data, 0600)
}
