package main

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/hashicorp/raft"
	"github.com/soheilhy/cmux"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/kovach/p2ser/internal/api"
	"github.com/kovach/p2ser/internal/cluster"
	cfgPkg "github.com/kovach/p2ser/internal/config"
	"github.com/kovach/p2ser/internal/deploy"
	"github.com/kovach/p2ser/internal/dns"
	"github.com/kovach/p2ser/internal/engine"
	"github.com/kovach/p2ser/internal/network"
	"github.com/kovach/p2ser/internal/scheduler"
	"github.com/kovach/p2ser/internal/system"
)

// Вбудовуємо зібраний UI безпосередньо в бінарник.
// Якщо директорії немає — просто порожньо go:embed, бінарник працює без UI.
//
//go:embed all:ui/dist
var embeddedUI embed.FS

// Цей файл імплементує пункт 1.1.1: Відсутність майстра.
// Ми просто створюємо екземпляр вузла (Node) за допомогою memberlist.
// Всі такі вузли в мережі будуть абсолютно рівноправними (P2P).

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Використання: p2ser <команда> [опції]")
		fmt.Println("Команди:")
		fmt.Println("  init <token>   - Ініціалізувати новий кластер P2SER")
		fmt.Println("  start [token]  - Приєднатися до існуючого кластера через mDNS")
		fmt.Println("  install        - Встановити систему автозапуску (systemd) для цього вузла")
		fmt.Println("  deploy         - Задеплоїти docker-compose.yaml в кластер")
		fmt.Println("  deploy-dir     - Задеплоїти всю папку (з вихідниками та .env) для збірки кластером")
		fmt.Println("  deploy-git     - Задеплоїти проект прямо з Git репозиторію")
		os.Exit(1)
	}

	command := os.Args[1]

	if command == "install" {
		system.InstallSystemdService()
		os.Exit(0)
	}

	if command == "deploy" {
		fs := flag.NewFlagSet("deploy", flag.ExitOnError)
		fileFlag := fs.String("f", "docker-compose.yaml", "Шлях до файлу маніфесту")
		endpointFlag := fs.String("H", "http://127.0.0.1:8002", "API Endpoint вузла")
		tokenFlag := fs.String("t", "", "API Token для авторизації (необов'язково, якщо є config.yaml)")
		fs.Parse(os.Args[2:])

		apiToken := *tokenFlag
		if apiToken == "" {
			if cfg, err := cfgPkg.LoadConfig("config.yaml"); err == nil {
				apiToken = cfg.UIToken
			}
		}

		deploy.DeployCompose(*fileFlag, *endpointFlag, apiToken)
		os.Exit(0)
	}

	if command == "deploy-dir" {
		fs := flag.NewFlagSet("deploy-dir", flag.ExitOnError)
		dirFlag := fs.String("d", ".", "Шлях до папки з проектом")
		endpointFlag := fs.String("H", "http://127.0.0.1:8002", "API Endpoint вузла")
		tokenFlag := fs.String("t", "", "API Token для авторизації")
		fs.Parse(os.Args[2:])

		apiToken := *tokenFlag
		if apiToken == "" {
			if cfg, err := cfgPkg.LoadConfig("config.yaml"); err == nil {
				apiToken = cfg.UIToken
			}
		}

		deploy.DeploySource(*dirFlag, *endpointFlag, apiToken)
		os.Exit(0)
	}

	if command == "deploy-git" {
		fs := flag.NewFlagSet("deploy-git", flag.ExitOnError)
		urlFlag := fs.String("u", "", "URL Git репозиторію (напр. https://github.com/user/repo.git)")
		branchFlag := fs.String("b", "", "Гілка (необов'язково)")
		endpointFlag := fs.String("H", "http://127.0.0.1:8002", "API Endpoint вузла")
		tokenFlag := fs.String("t", "", "API Token для авторизації")
		fs.Parse(os.Args[2:])

		if *urlFlag == "" {
			fmt.Println("Помилка: прапорець -u (Git URL) обов'язковий!")
			os.Exit(1)
		}

		apiToken := *tokenFlag
		if apiToken == "" {
			if cfg, err := cfgPkg.LoadConfig("config.yaml"); err == nil {
				apiToken = cfg.UIToken
			}
		}

		deploy.DeployGit(*urlFlag, *branchFlag, map[string]string{}, *endpointFlag, apiToken)
		os.Exit(0)
	}

	if command != "init" && command != "start" {
		fmt.Printf("Невідома команда: %s\n", command)
		os.Exit(1)
	}

	isBootstrap := (command == "init")

	// Прапарці командного рядка
	var name string
	var port int
	var raftPort int
	var dataDir string
	var joinAddr string
	var vpsEndpoint string
	var vpsPubKey string
	var bootstrapExpect int
	var vpsPrivKey string
	var vpsTunnelIP string
	var edgeMode bool
	var uiToken string

	configPath := "config.yaml"
	var cfg *cfgPkg.NodeConfig
	var err error

	if _, errStat := os.Stat(configPath); errStat == nil {
		cfg, err = cfgPkg.LoadConfig(configPath)
		if err != nil {
			log.Fatalf("Помилка читання config.yaml: %v", err)
		}
		fmt.Println("✓ Завантажено налаштування з config.yaml")
	}

	var token string
	if cfg == nil {
		if len(os.Args) < 3 {
			fmt.Printf("Помилка: Команда '%s' вимагає секретний токен (або наявність config.yaml).\n", command)
			os.Exit(1)
		}
		token = os.Args[2]

		fs := flag.NewFlagSet(command, flag.ExitOnError)
		fs.StringVar(&name, "name", "", "Ім'я вузла")
		fs.IntVar(&port, "port", 8001, "Порт для Gossip")
		fs.IntVar(&raftPort, "raft-port", 8002, "Порт для Raft (TCP)")
		fs.StringVar(&dataDir, "data", "./p2ser-data", "Директорія для зберігання стану")
		fs.StringVar(&joinAddr, "join", "", "IP:Port seed-вузлів (через кому)")
		fs.StringVar(&vpsEndpoint, "ingress-vps", "", "IP:Port зовнішнього VPS")
		fs.StringVar(&vpsPubKey, "ingress-pubkey", "", "Публічний ключ VPS")
		fs.StringVar(&vpsPrivKey, "ingress-privkey", "", "Приватний ключ локального вузла")
		fs.StringVar(&vpsTunnelIP, "ingress-ip", "10.100.0.2/24", "Внутрішня IP-адреса тунелю")
		fs.BoolVar(&edgeMode, "edge", false, "Увімкнути профіль Gossip для нестабільних мереж")
		fs.StringVar(&uiToken, "ui-token", "", "Окремий токен для UI (генерується автоматично, якщо порожній)")
		fs.IntVar(&bootstrapExpect, "bootstrap-expect", 1, "Кількість вузлів для очікування перед bootstrap (лише для init)")

		fs.Parse(os.Args[3:])

		// Зберігаємо конфіг (Пункт 9.2)
		cfg = &cfgPkg.NodeConfig{
			Token:       token,
			UIToken:     uiToken,
			Name:        name,
			Port:        port,
			RaftPort:    raftPort,
			DataDir:     dataDir,
			EdgeMode:    edgeMode,
			Arch:        "arm64",
			VPSEndpoint: vpsEndpoint,
			VPSPubKey:   vpsPubKey,
			VPSPrivKey:  vpsPrivKey,
			VPSTunnelIP: vpsTunnelIP,
			PodSubnet:   "10.88.0.0/16",
			BridgeName:  "p2ser0",
			UpstreamDNS: "1.1.1.1",
			SanctionedCodes: []string{"RU", "BY", "KP", "IR", "SY"},
		}
		
		if err := cfgPkg.SaveConfig(configPath, cfg); err != nil {
			log.Fatalf("Помилка збереження config.yaml: %v", err)
		}
		fmt.Println("✓ Створено новий файл config.yaml")
	} else {
		// Якщо конфіг уже є, але L-11 поля порожні, ініціалізуємо їх
		updated := false
		if cfg.PodSubnet == "" { cfg.PodSubnet = "10.88.0.0/16"; updated = true }
		if cfg.BridgeName == "" { cfg.BridgeName = "p2ser0"; updated = true }
		if cfg.UpstreamDNS == "" { cfg.UpstreamDNS = "1.1.1.1"; updated = true }
		if len(cfg.SanctionedCodes) == 0 { cfg.SanctionedCodes = []string{"RU", "BY", "KP", "IR", "SY"}; updated = true }
		if updated {
			cfgPkg.SaveConfig(configPath, cfg)
		}
		
		token = cfg.Token
		name = cfg.Name
		port = cfg.Port
		raftPort = cfg.RaftPort
		dataDir = cfg.DataDir
		edgeMode = cfg.EdgeMode
		vpsEndpoint = cfg.VPSEndpoint
		vpsPubKey = cfg.VPSPubKey
		vpsPrivKey = cfg.VPSPrivKey
		vpsTunnelIP = cfg.VPSTunnelIP

		if len(os.Args) >= 3 {
			fs := flag.NewFlagSet(command, flag.ExitOnError)
			fs.StringVar(&joinAddr, "join", "", "IP:Port seed-вузлів (через кому)")
			_ = fs.Parse(os.Args[2:])
		}
	}
	cfgPkg.GlobalConfig = cfg

	var config *memberlist.Config
	if edgeMode {
		// 8.4: Адаптивний Gossip для Wi-Fi/LTE
		config = memberlist.DefaultWANConfig()
		log.Println("Edge Mode: Увімкнено WAN-профіль для локального Gossip (стійкість до втрати пакетів)")
	} else {
		// 1.1.2: Використовуємо конфігурацію для локальних мереж (LAN).
		config = memberlist.DefaultLANConfig()
	}
	
	// 11.1: Симетричне шифрування Gossip-трафіку (AES)
	// Використовуємо хеш від Bootstrap Token як 32-байтний ключ AES-256-GCM
	tokenHash := sha256.Sum256([]byte(cfg.Token))
	config.SecretKey = tokenHash[:]

	// 1.4.3: Додаємо обробник подій для моніторингу статусу вузлів
	config.Events = &network.MyEventDelegate{}

	// Це вже оголошено вище, видаляємо дублікати парсингу аргументів

	if name != "" {
		config.Name = name
	}
	config.BindPort = port

	// Черга для розсилки метрик (Пункт 1.3.1)
	broadcasts := &memberlist.TransmitLimitedQueue{
		NumNodes:       func() int { return 1 }, // Тимчасовий фейк до ініціалізації list
		RetransmitMult: 3,
	}

	// 4.1, 4.2, 4.3: Налаштування Network Manager
	localIP := getOutboundIP()
	netManager := network.NewNetworkManager(config.Name, localIP)
	localSubnet := netManager.SetupAllocateSubnet()
	_ = netManager.GenerateCNIConfig()
	netManager.SetupVXLAN()

	// 7.3: Налаштування Ingress Proxy (обхід NAT)
	if vpsEndpoint != "" && vpsPubKey != "" && vpsPrivKey != "" {
		netManager.SetupIngressProxy(vpsEndpoint, vpsPubKey, vpsPrivKey, vpsTunnelIP)
	}

	// 7.1: Застосування Geo-IP Санкційного Фільтра
	netManager.ApplyGeoIPSanctions()

	// 1.1.3: Обмін метаданими (Delegate).
	config.Delegate = &network.MyDelegate{
		Meta: network.NodeMetadata{
			Arch:     "arm64",
			FreeRAM:  2048,
			FreeCPU:  4,
			Subnet:   localSubnet, // Додаємо наш пул IP до Gossip для пункту 4.4
			RaftPort: raftPort,    // Передаємо Raft TCP порт для автоматичного додавання
		},
		Broadcasts: broadcasts,
		NetManager: netManager,
	}

	// 7.2: Георозподілені кластери (Federation)
	var wanList *memberlist.Memberlist
	var wanBroadcasts *memberlist.TransmitLimitedQueue
	if joinAddr != "" {
		// Створюємо окреме кільце WAN для об'єднання незалежних Raft-кластерів
		wanConfig := memberlist.DefaultWANConfig()
		wanConfig.SecretKey = tokenHash[:] // Застосовуємо шифрування і для WAN-кільця
		if name != "" {
			wanConfig.Name = name + "-wan"
		} else {
			wanConfig.Name = "wan-" + fmt.Sprintf("%d", time.Now().Unix())
		}
		wanConfig.BindPort = port + 1000 // Наприклад 9001, якщо LAN Gossip на 8001

		wanBroadcasts = &memberlist.TransmitLimitedQueue{
			NumNodes:       func() int { return 1 },
			RetransmitMult: 3,
		}
		wanConfig.Delegate = &network.MyDelegate{
			Meta:       config.Delegate.(*network.MyDelegate).Meta, // Шаримо ті самі метадані для compose.Service Discovery
			Broadcasts: wanBroadcasts,
		}
		wanConfig.Events = &network.MyEventDelegate{NetManager: netManager}

		var errWan error
		wanList, errWan = memberlist.Create(wanConfig)
		if errWan != nil {
			log.Fatalf("Помилка створення WAN Gossip (Federation): %v", errWan)
		}
		wanBroadcasts.NumNodes = func() int { return wanList.NumMembers() }

		// 1.2.2: Глобальний Bootstrap (Seed Nodes) - тепер через Federation WAN
		// Якщо вказані IP адреси, приєднуємося до них через WAN/Internet
		log.Printf("Gossip: Спроба підключення до WAN вузлів: %s", joinAddr)
		_, err = wanList.Join(strings.Split(joinAddr, ","))
		if err != nil {
			log.Printf("Помилка підключення до WAN-кластера: %v", err)
		}
	}

	// 1.4.3 та 4.4: Додаємо обробник подій
	eventDelegate := &network.MyEventDelegate{NetManager: netManager}
	config.Events = eventDelegate

	// Запускаємо вузол
	list, err := memberlist.Create(config)
	if err != nil {
		log.Fatalf("Помилка створення вузла (P2P): %v", err)
	}

	// 1.2.1: Запускаємо Auto-Discovery через mDNS (Пункт 9.1)
	go network.SetupMDNS(list, token, config.BindPort)


	// Виводимо інформацію про те, що вузол успішно стартував як рівноправний учасник
	localNode := list.LocalNode()
	fmt.Printf("✓ Вузол [%s] успішно запущено за адресою %s:%d\n", localNode.Name, localNode.Addr, localNode.Port)
	fmt.Println("✓ Цей вузол рівноправний і не має майстра (Пункт 1.1.1 виконано).")

	// 2.1.2, 2.1.4, 2.2.1, 2.2.2: Ініціалізація cluster.FSM (кінцевого автомата на базі bbolt)
	nodeDataDir := filepath.Join(dataDir, localNode.Name)
	if err := os.MkdirAll(nodeDataDir, 0700); err != nil {
		log.Fatalf("Помилка створення директорії даних: %v", err)
	}
	fsm, err := cluster.NewFSM(filepath.Join(nodeDataDir, "state.db"), cfg.Token)
	if err != nil {
		log.Fatalf("Помилка ініціалізації cluster.FSM (Bbolt): %v", err)
	}

	// 2.1.3: Мережевий транспорт з мультиплексором (cmux)
	// Це дозволить на одному TCP-порту приймати і Raft, і (в майбутньому) gRPC/HTTP API
	raftAddr := fmt.Sprintf("0.0.0.0:%d", raftPort)
	listener, err := net.Listen("tcp", raftAddr)
	if err != nil {
		log.Fatalf("Помилка Listen на порту Raft: %v", err)
	}

	m := cmux.New(listener)
	// Матчимо HTTP-трафік (для API сервера)
	httpListener := m.Match(cmux.HTTP1Fast())
	// Матчимо Raft-трафік (все інше йде на Raft)
	raftListener := m.Match(cmux.Any())

	// Запускаємо cmux у фоні
	go func() {
		if err := m.Serve(); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Fatalf("Помилка cmux: %v", err)
		}
	}()

	// Пункт 2.1.1: Запуск Raft
	// Використовуємо кастомний Listener замість звичайного TCP та передаємо прапорець bootstrap
	if isBootstrap {
		if bootstrapExpect > 1 {
			log.Printf("Очікування %d вузлів (через mDNS/Gossip) перед bootstrap...", bootstrapExpect)
			for list.NumMembers() < bootstrapExpect {
				time.Sleep(200 * time.Millisecond) // Детерміністичне очікування, а не sleep 2s
			}
			
			// C-6: Детерміністичний вибір єдиного bootstrap-лідера
			members := list.Members()
			var names []string
			for _, m := range members {
				names = append(names, m.Name)
			}
			sort.Strings(names)
			
			if names[0] != localNode.Name {
				log.Printf("Детерміністичний вибір: вузол %s буде bootstrap-лідером. Ми перемикаємось у режим start (приєднання).", names[0])
				isBootstrap = false
			} else {
				log.Printf("Цей вузол (%s) обрано bootstrap-лідером.", localNode.Name)
			}
		} else {
			// Якщо bootstrap-expect = 1, просто запускаємося як bootstrap без очікування
			if list.NumMembers() > 1 || (wanList != nil && wanList.NumMembers() > 1) {
				log.Println("Знайдено існуючий кластер. Перемикання в режим приєднання (start) замість bootstrap.")
				isBootstrap = false
			}
		}
	}

	raftNode, err := cluster.SetupRaftWithListener(localNode.Name, listener.Addr().String(), raftListener, nodeDataDir, fsm, isBootstrap)
	if err != nil {
		log.Fatalf("Помилка запуску Raft: %v", err)
	}

	// Зберігаємо посилання на Raft в Delegate, щоб Leader міг автоматично додавати Voter-ів
	eventDelegate.RaftNode = raftNode

	// Незалежний токен для UI
	apiToken := cfg.UIToken
	if apiToken == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("Fatal: failed to generate API token: %v", err)
		}
		apiToken = hex.EncodeToString(b)
		cfg.UIToken = apiToken
		cfgPkg.SaveConfig(configPath, cfg)
	}

	// Пункт 3: Запуск децентралізованого планувальника
	scheduler := scheduler.NewScheduler(localNode.Name, fmt.Sprintf("127.0.0.1:%d", raftPort), config.Delegate.(*network.MyDelegate).Meta, apiToken)
	
	// C-11: Pass live-node check to Scheduler to prevent split-brain during Standby promotion
	scheduler.IsNodeAlive = func(nodeID string) bool {
		if list == nil {
			return false
		}
		for _, member := range list.Members() {
			if member.Name == nodeID && member.State == memberlist.StateAlive {
				return true
			}
		}
		return false
	}
	
	scheduler.Start()

	// Запуск внутрішнього DNS сервера
	dnsServer := dns.NewServer(scheduler)
	go dnsServer.Start()

	// Пункт 5.1: Ініціалізація прямої інтеграції з containerd та запуск агента
	var agent *engine.Agent
	cm, err := engine.NewContainerManager("") // Використає дефолтний /run/containerd/containerd.sock
	if err != nil {
		log.Printf("Попередження: не вдалося підключитися до containerd (можливо він не встановлений): %v", err)
	} else {
		engine.EnsureCNIPlugins()
		// C-10: Provide container state checker to scheduler for lease renewal
		scheduler.ContainerChecker = cm.IsContainerRunning
		agent = engine.NewAgent(localNode.Name, scheduler, cm)
		agent.Start()
		fmt.Println("✓ Containerd engine.Agent успішно запущено")
	}

	// Пункт 2.4.3: Налаштовуємо Fencing (Самоізоляцію) при втраті кворуму
	engine.SetupFencing(raftNode, func() {
		if agent != nil {
			agent.FenceStatefulPods()
		} else {
			log.Println("Fencing: Агент не ініціалізовано, зупинка контейнерів неможлива.")
		}
	})

	// (apiToken already generated above)

	// Пункт 2.3: Запуск HTTP API сервера для маршрутизації записів (Write Forwarding)
	apiServer := api.NewAPIServer(raftNode, fsm, netManager, apiToken, cm, localNode.Name)
	go func() {
		// Спробуємо отримати вбудований UI (якщо vite build був запущений)
		var uiFS http.FileSystem
		subFS, err := fs.Sub(embeddedUI, "ui/dist")
		if err == nil {
			uiFS = http.FS(subFS)
			fmt.Println("✓ Вбудований UI знайдено — роздається на /")
		} else {
			fmt.Println("⚠  Вбудований UI не знайдено (запустіть 'npm run build' в ui/)")
		}
		// same-origin CORS — зовнішні сайти не можуть робити запити до API
		if err := apiServer.ServeWithUI(httpListener, uiFS, "same-origin"); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Помилка запуску API сервера: %v", err)
		}
	}()

	fmt.Printf("✓ Raft-вузол та API сервер запущено на порту %d (Мультиплексор cmux працює)\n", raftPort)
	fmt.Printf("🔑 Ваш токен для доступу до API/Dashboard: %s\n", apiToken)

	// Оновлюємо посилання на NumNodes, щоб черга знала розмір кластера
	broadcasts.NumNodes = func() int { return list.NumMembers() }

	// 1.3.1 та 1.3.2: Транспорт метрик (Broadcasts + MessagePack)
	go func() {
		// 3.1.2: Оновлення оренди
		for {
			time.Sleep(15 * time.Second)

			// M-13: Зчитуємо реальні системні метрики замість захардкоджених
			cpuUsage := 0.0
			if b, err := os.ReadFile("/proc/loadavg"); err == nil {
				parts := strings.Fields(string(b))
				if len(parts) > 0 {
					var loadAvg float64
					fmt.Sscanf(parts[0], "%f", &loadAvg)
					// Ділимо loadavg на кількість ядер, щоб отримати %, але не більше 1.0, і переводимо у 0-100%
					numCores := float64(runtime.NumCPU())
					if numCores > 0 {
						cpuUsage = (loadAvg / numCores) * 100.0
						if cpuUsage > 100.0 {
							cpuUsage = 100.0
						}
					}
				}
			}
			
			ramFreeMB := 0
			ramTotalMB := 2048 // Дефолтне значення, якщо не вдалося зчитати
			if b, err := os.ReadFile("/proc/meminfo"); err == nil {
				lines := strings.Split(string(b), "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "MemTotal:") {
						var kb int
						fmt.Sscanf(line, "MemTotal: %d kB", &kb)
						ramTotalMB = kb / 1024
					}
					if strings.HasPrefix(line, "MemAvailable:") {
						var kb int
						fmt.Sscanf(line, "MemAvailable: %d kB", &kb)
						ramFreeMB = kb / 1024
					}
				}
			}

			metrics := network.NodeMetrics{
				Node:     localNode.Name,
				Version:  time.Now().UnixNano(), // 1.3.3: Кожна нова метрика має більшу версію (штамп часу)
				CPUUsage: cpuUsage,
				RAMFree:  ramFreeMB,
				RAMTotal: ramTotalMB,
			}

			if raftNode.State() == raft.Leader {
				raftNode.Apply([]byte(`{"op":"heartbeat", "node_id":"`+localNode.Name+`"}`), 3*time.Second)
			}
			// Серіалізуємо у бінарний MessagePack (дуже компактно!)
			b, err := msgpack.Marshal(&metrics)
			if err == nil {
				broadcasts.QueueBroadcast(&network.MetricsBroadcast{Msg: b})
			}
		}
	}()

	// 1.4.3: Обробка Graceful Shutdown (Планове вимкнення)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nОтримано сигнал на вимкнення. Виконуємо Graceful Shutdown...")
	// Надсилаємо сусідам повідомлення, що ми йдемо, щоб вони не вважали нас Dead
	if err := list.Leave(time.Second * 5); err != nil {
		log.Printf("Помилка при плановому виході з LAN: %v", err)
	}
	if err := wanList.Leave(time.Second * 5); err != nil {
		log.Printf("Помилка при плановому виході з WAN: %v", err)
	}
	list.Shutdown()
	wanList.Shutdown()
	listener.Close()
	fmt.Println("Вузол успішно зупинено.")
}

// getOutboundIP отримує локальну IP-адресу, обходячи мережеві інтерфейси
func getOutboundIP() string {
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, i := range ifaces {
			if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
				continue
			}
			// Пропускаємо віртуальні інтерфейси
			if strings.HasPrefix(i.Name, "docker") || strings.HasPrefix(i.Name, "p2ser") || strings.HasPrefix(i.Name, "vxlan") || strings.HasPrefix(i.Name, "br-") {
				continue
			}
			addrs, err := i.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				ip = ip.To4()
				if ip != nil {
					return ip.String()
				}
			}
		}
	}
	log.Println("Попередження: не вдалося визначити локальний IP, використовуємо 127.0.0.1")
	return "127.0.0.1"
}
