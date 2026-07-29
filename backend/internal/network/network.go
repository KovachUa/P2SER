package network

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kovach/p2ser/internal/config"
	"github.com/vishvananda/netlink"
)

// NetworkManager відповідає за Розділ 4: Мережева інфраструктура Pod-ів
type NetworkManager struct {
	nodeID      string
	localIP     string
	subnet      string
	geoIPError  string
	nodeMetrics map[string]NodeMetrics
	mu          sync.RWMutex
}

func (nm *NetworkManager) UpdateNodeMetrics(metrics NodeMetrics) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	if nm.nodeMetrics == nil {
		nm.nodeMetrics = make(map[string]NodeMetrics)
	}
	nm.nodeMetrics[metrics.Node] = metrics
}

func (nm *NetworkManager) GetNodeMetrics(nodeID string) (NodeMetrics, bool) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	if nm.nodeMetrics == nil {
		return NodeMetrics{}, false
	}
	m, ok := nm.nodeMetrics[nodeID]
	return m, ok
}

func (nm *NetworkManager) GetGeoIPError() string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.geoIPError
}

func (nm *NetworkManager) setGeoIPError(errStr string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.geoIPError = errStr
}

func NewNetworkManager(nodeID, localIP string) *NetworkManager {
	return &NetworkManager{
		nodeID:  nodeID,
		localIP: localIP,
	}
}

// SetupAllocateSubnet генерує унікальний блок адрес (Пункт 4.1: IPAM host-local)
// Щоб уникнути звернення до лідера на кожен scheduler.Pod, вузол отримує цілий /24 блок.
func (nm *NetworkManager) SetupAllocateSubnet() string {
	hash := sha256.Sum256([]byte(nm.nodeID))
	x := int(hash[0])
	if x == 0 || x == 255 {
		x = 1
	}

	nm.subnet = fmt.Sprintf("10.244.%d.0/24", x)
	log.Printf("IPAM: Вузлу %s виділено локальний блок IP: %s (Пункт 4.1)", nm.nodeID, nm.subnet)
	return nm.subnet
}

// GenerateCNIConfig створює JSON конфігурацію для CNI плагінів (Пункт 4.2)
func (nm *NetworkManager) GenerateCNIConfig() string {
	config := map[string]interface{}{
		"cniVersion": "0.3.1",
		"name":       "p2ser-net",
		"type":       "bridge",
		"bridge":     "cni0",
		"isGateway":  true,
		"ipMasq":     true,
		"ipam": map[string]interface{}{
			"type":   "host-local",
			"subnet": nm.subnet,
			"routes": []map[string]string{
				{"dst": "0.0.0.0/0"},
			},
		},
	}

	bytes, _ := json.MarshalIndent(config, "", "  ")
	log.Println("CNI: Згенеровано конфігурацію для плагінів bridge та host-local (Пункт 4.2)")
	return string(bytes)
}

// SetupVXLAN налаштовує віртуальну мережу над фізичною (Пункт 4.3)
// Тепер використовує бібліотеку netlink замість os/exec
func (nm *NetworkManager) SetupVXLAN() {
	vxlanName := "vxlan0"

	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{Name: vxlanName},
		VxlanId:   100,
		Port:      4789,
	}

	// 1. Створюємо інтерфейс
	err := netlink.LinkAdd(vxlan)
	if err != nil && !strings.Contains(err.Error(), "file exists") {
		log.Printf("VXLAN Помилка: створення інтерфейсу %s провалилося: %v", vxlanName, err)
		return
	}
	log.Printf("VXLAN: Інтерфейс %s створено/вже існує (VNI: 100)", vxlanName)

	// 2. Отримуємо його
	link, err := netlink.LinkByName(vxlanName)
	if err != nil {
		log.Printf("VXLAN Помилка: не знайдено інтерфейс %s: %v", vxlanName, err)
		return
	}

	// 3. Піднімаємо інтерфейс
	if err := netlink.LinkSetUp(link); err != nil {
		log.Printf("VXLAN Помилка підняття: %v", err)
	} else {
		log.Printf("VXLAN: Інтерфейс overlay мережі успішно піднято. Локальний IP: %s (Пункт 4.3)", nm.localIP)
	}
}

// UpdateRouteTable отримує дані з Gossip та оновлює правила маршрутизації (Пункт 4.4)
func (nm *NetworkManager) UpdateRouteTable(peerNodeID, peerIP, peerSubnet string) {
	if peerNodeID == nm.nodeID {
		return // Самому собі маршрут не потрібен
	}

	_, dstCIDR, err := net.ParseCIDR(peerSubnet)
	if err != nil {
		return
	}

	// Хост, на який направляємо трафік
	gw := net.ParseIP(peerIP)

	link, err := netlink.LinkByName("vxlan0")
	if err != nil {
		return
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dstCIDR,
		Gw:        gw,
	}

	err = netlink.RouteAdd(route)
	if err != nil && !strings.Contains(err.Error(), "file exists") {
		log.Printf("Маршрутизація Помилка: не вдалося додати маршрут до %s: %v", peerSubnet, err)
	} else if err == nil || strings.Contains(err.Error(), "file exists") {
		log.Printf("Маршрутизація: Додано/Оновлено маршрут до %s через фізичний IP %s (Пункт 4.4)", peerSubnet, peerIP)
	}

	// C-8: Add FDB entry for VXLAN
	hwAddr, _ := net.ParseMAC("00:00:00:00:00:00")
	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		State:        netlink.NUD_PERMANENT,
		Family:       syscall.AF_BRIDGE,
		Flags:        netlink.NTF_SELF,
		IP:           gw,
		HardwareAddr: hwAddr,
	}
	err = netlink.NeighAppend(neigh)
	if err != nil {
		log.Printf("VXLAN FDB Помилка: не вдалося додати FDB запис для %s: %v", peerIP, err)
	} else {
		log.Printf("VXLAN FDB: Додано/Оновлено FDB запис для фізичного IP %s", peerIP)
	}
}

// ApplyGeoIPSanctions реалізує Пункт 7.1 (Geo-IP Санкційний Фільтр через iptables/ipset)
// M-10: Atomic ipset swap, native Go HTTP, and local caching.
func (nm *NetworkManager) ApplyGeoIPSanctions() {
	countries := []string{"ru", "by"}
	if config.GlobalConfig != nil && len(config.GlobalConfig.SanctionedCodes) > 0 {
		countries = config.GlobalConfig.SanctionedCodes
	}
	
	// Ensure lower case for URLs
	for i, c := range countries {
		countries[i] = strings.ToLower(c)
	}

	cacheDir := "/var/lib/p2ser/geoip"
	os.MkdirAll(cacheDir, 0755)

	// Переконуємось, що основний ipset існує
	_ = exec.Command("ipset", "create", "p2ser_sanctions", "hash:net").Run()

	tmpSetName := fmt.Sprintf("p2ser_sanctions_tmp_%d", time.Now().Unix())
	_ = exec.Command("ipset", "create", tmpSetName, "hash:net").Run()
	defer exec.Command("ipset", "destroy", tmpSetName).Run()

	var allErrors []string
	success := true

	for _, country := range countries {
		log.Printf("Geo-IP: Завантаження бази адрес для країни: %s...", country)
		
		cacheFile := filepath.Join(cacheDir, country+".zone")
		var reader io.Reader

		resp, err := http.Get(fmt.Sprintf("https://www.ipdeny.com/ipblocks/data/countries/%s.zone", country))
		if err != nil || resp.StatusCode != 200 {
			if err == nil {
				resp.Body.Close()
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			log.Printf("Geo-IP Fetch failed for %s: %v. Using cache.", country, err)
			allErrors = append(allErrors, fmt.Sprintf("%s: fetch failed (%v)", country, err))

			// Fallback to cache
			f, cacheErr := os.Open(cacheFile)
			if cacheErr != nil {
				log.Printf("Geo-IP Cache failed for %s: %v.", country, cacheErr)
				allErrors = append(allErrors, fmt.Sprintf("%s: cache failed (%v)", country, cacheErr))
				success = false
				continue
			}
			defer f.Close()
			
			// Load entire file into memory so we can use strings.NewReader
			data, _ := io.ReadAll(f)
			reader = strings.NewReader(string(data))
		} else {
			defer resp.Body.Close()
			
			bodyBytes, _ := io.ReadAll(resp.Body)
			os.WriteFile(cacheFile, bodyBytes, 0644)
			reader = strings.NewReader(string(bodyBytes))
		}

		// Restore into tmp ipset
		var restoreCmds []string
		data, _ := io.ReadAll(reader)
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			ip := strings.TrimSpace(line)
			if ip != "" && !strings.HasPrefix(ip, "#") {
				restoreCmds = append(restoreCmds, fmt.Sprintf("add %s %s", tmpSetName, ip))
			}
		}

		if len(restoreCmds) > 0 {
			restoreData := strings.Join(restoreCmds, "\n") + "\n"
			cmd := exec.Command("ipset", "restore")
			cmd.Stdin = strings.NewReader(restoreData)
			if err := cmd.Run(); err != nil {
				log.Printf("Geo-IP ipset restore failed for %s: %v", country, err)
				allErrors = append(allErrors, fmt.Sprintf("%s: ipset restore failed", country))
				success = false
			}
		}
	}

	if success {
		// Atomic swap
		_ = exec.Command("ipset", "swap", tmpSetName, "p2ser_sanctions").Run()
		nm.setGeoIPError("") // Clear error if completely successful
	} else if len(allErrors) > 0 {
		nm.setGeoIPError(strings.Join(allErrors, "; "))
	}

	// 2. Додаємо правила iptables на рівні ядра (DROP)
	// Вхідний трафік
	if err := exec.Command("iptables", "-C", "INPUT", "-m", "set", "--match-set", "p2ser_sanctions", "src", "-j", "DROP").Run(); err != nil {
		_ = exec.Command("iptables", "-I", "INPUT", "-m", "set", "--match-set", "p2ser_sanctions", "src", "-j", "DROP").Run()
	}

	// Вихідний трафік (запобігання витоку даних)
	if err := exec.Command("iptables", "-C", "OUTPUT", "-m", "set", "--match-set", "p2ser_sanctions", "dst", "-j", "DROP").Run(); err != nil {
		_ = exec.Command("iptables", "-I", "OUTPUT", "-m", "set", "--match-set", "p2ser_sanctions", "dst", "-j", "DROP").Run()
	}

	// Транзитний/контейнерний трафік (FORWARD)
	if err := exec.Command("iptables", "-C", "FORWARD", "-m", "set", "--match-set", "p2ser_sanctions", "src", "-j", "DROP").Run(); err != nil {
		_ = exec.Command("iptables", "-I", "FORWARD", "-m", "set", "--match-set", "p2ser_sanctions", "src", "-j", "DROP").Run()
	}
	if err := exec.Command("iptables", "-C", "FORWARD", "-m", "set", "--match-set", "p2ser_sanctions", "dst", "-j", "DROP").Run(); err != nil {
		_ = exec.Command("iptables", "-I", "FORWARD", "-m", "set", "--match-set", "p2ser_sanctions", "dst", "-j", "DROP").Run()
	}

	// Блокування ICMP (Ping) для уникнення сканування мережі (Stealth Mode)
	if err := exec.Command("iptables", "-C", "INPUT", "-p", "icmp", "--icmp-type", "echo-request", "-j", "DROP").Run(); err != nil {
		_ = exec.Command("iptables", "-I", "INPUT", "-p", "icmp", "--icmp-type", "echo-request", "-j", "DROP").Run()
	}
	log.Println("Security: Санкційний фільтр застосовано та ICMP Ping (Stealth Mode) заблоковано на рівні ядра.")
}

// SetupIngressProxy налаштовує WireGuard тунель до зовнішнього VPS (Пункт 7.3)
func (nm *NetworkManager) SetupIngressProxy(vpsEndpoint, pubKey, privKey, tunnelIP string) {
	log.Println("Ingress Proxy: Ініціалізація WireGuard тунелю до VPS...")

	linkName := "wg0"

	// Видаляємо старий інтерфейс, якщо він залишився від попереднього запуску
	_ = exec.Command("ip", "link", "del", linkName).Run()

	// 1. Створюємо інтерфейс WireGuard
	if err := exec.Command("ip", "link", "add", "dev", linkName, "type", "wireguard").Run(); err != nil {
		log.Printf("Ingress Proxy Помилка: не вдалося створити інтерфейс %s (перевірте чи встановлено wireguard-tools): %v", linkName, err)
		return
	}

	// 2. Призначаємо IP-адресу (внутрішню для тунелю)
	if err := exec.Command("ip", "address", "add", "dev", linkName, tunnelIP).Run(); err != nil {
		log.Printf("Ingress Proxy Помилка: призначення IP %s провалилося: %v", tunnelIP, err)
		return
	}

	// C-4: Write private key safely without shell injection
	// Use os.WriteFile with 0600 permissions instead of echo via sh -c
	keyPath := "/tmp/p2ser_wg_privkey"
	if err := os.WriteFile(keyPath, []byte(privKey+"\n"), 0600); err != nil {
		log.Printf("Ingress Proxy Помилка: не вдалося записати приватний ключ: %v", err)
		return
	}
	defer os.Remove(keyPath)

	// 4. Налаштовуємо конфігурацію WireGuard (прив'язуємо до VPS)
	err := exec.Command("wg", "set", linkName,
		"private-key", keyPath,
		"peer", pubKey,
		"endpoint", vpsEndpoint,
		"allowed-ips", "0.0.0.0/0", // Прокидаємо весь зовнішній Ingress трафік
		"persistent-keepalive", "25").Run()

	if err != nil {
		log.Printf("Ingress Proxy Помилка: конфігурація wg провалилася: %v", err)
		return
	}

	// 5. Піднімаємо інтерфейс
	if err := exec.Command("ip", "link", "set", "up", "dev", linkName).Run(); err != nil {
		log.Printf("Ingress Proxy Помилка: не вдалося підняти інтерфейс: %v", err)
		return
	}

	log.Printf("Ingress Proxy: Успішно підключено до зовнішнього VPS (%s) через WireGuard. Кластер доступний ззовні, обходячи NAT!", vpsEndpoint)
}

// BanIP реалізує Пункт 7.4 (Динамічний бан по IP)
// Ми додаємо IP до того ж ipset, який використовується для Geo-IP, щоб миттєво обірвати всі підключення на рівні ядра
func (nm *NetworkManager) BanIP(targetIP string) error {
	// Перевіряємо валідність IP
	if net.ParseIP(targetIP) == nil {
		return fmt.Errorf("invalid IP address format: %s", targetIP)
	}

	// ipset add ігнорує помилку, якщо IP вже є в списку, але ми використовуємо -exist щоб уникнути ворнінгів
	err := exec.Command("ipset", "add", "-exist", "p2ser_sanctions", targetIP).Run()
	if err != nil {
		log.Printf("Security Помилка: не вдалося забанити IP %s: %v", targetIP, err)
		return err
	}

	log.Printf("Security: IP %s успішно додано до чорного списку (DROP). З'єднання розірвано.", targetIP)
	return nil
}
