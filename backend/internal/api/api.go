package api

import (
	"archive/zip"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"github.com/hashicorp/raft"

	"github.com/kovach/p2ser/internal/builder"
	"github.com/kovach/p2ser/internal/cluster"
	"github.com/kovach/p2ser/internal/compose"
	"github.com/kovach/p2ser/internal/engine"
	"github.com/kovach/p2ser/internal/network"
	"github.com/kovach/p2ser/internal/scheduler"
)

// validPodID validates pod ID to prevent command injection (C-2)
var validPodID = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,62}$`)

// sanitizeLog removes newlines/CR from log strings to prevent log injection (L-1)
func sanitizeLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

// APIServer обслуговує запити до кластера (Пункт 2.3 та 2.4.2)
type APIServer struct {
	raftNode    *raft.Raft
	fsm         *cluster.FSM
	netManager  *network.NetworkManager
	apiToken    string
	cm          *engine.ContainerManager
	localNodeID string
	wsTickets   sync.Map // Зберігає одноразові тікети для WebSocket (H-12)
}

func NewAPIServer(raftNode *raft.Raft, fsm *cluster.FSM, netManager *network.NetworkManager, apiToken string, cm *engine.ContainerManager, localNodeID string) *APIServer {
	return &APIServer{
		raftNode:    raftNode,
		fsm:         fsm,
		netManager:  netManager,
		apiToken:    apiToken,
		cm:          cm,
		localNodeID: localNodeID,
	}
}

// forwardToLeader proxies the request to the cluster leader if the current node is a Follower.
// Returns true if the request was proxied (and handled), false if the current node is the Leader.
func (s *APIServer) forwardToLeader(w http.ResponseWriter, r *http.Request) bool {
	if s.raftNode.State() == raft.Leader {
		return false
	}

	leaderAddr := s.raftNode.Leader()
	if leaderAddr == "" {
		http.Error(w, "Leader not found (cluster election in progress)", http.StatusServiceUnavailable)
		return true
	}

	log.Printf("API: Вузол є Follower. Проксіювання запиту до лідера %s", leaderAddr)

	targetURL, _ := url.Parse("http://" + string(leaderAddr))
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ServeHTTP(w, r)
	return true
}

// HandleApply відповідає за обробку команд на зміну стану (Пункт 2.3.2 та 2.3.3)
func (s *APIServer) HandleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.forwardToLeader(w, r) {
		return
	}

	// H-2: limit request body to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Request body too large or failed to read", http.StatusRequestEntityTooLarge)
		return
	}
	defer r.Body.Close()

	// 2.3.3: Команди ідемпотентні (наприклад, "set"), тому подвійне виконання безпечне
	// Якщо ми лідер, застосовуємо команду до Raft логу
	applyFuture := s.raftNode.Apply(body, 5*time.Second)
	if err := applyFuture.Error(); err != nil {
		http.Error(w, fmt.Sprintf("Raft Apply error: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Command successfully committed by Leader\n"))
}

// HandleUploadSource приймає ZIP-архів з проектом, збирає його і відправляє в кластер
func (s *APIServer) HandleUploadSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// M-9: Write-forwarding for deploy handlers
	if s.forwardToLeader(w, r) {
		return
	}

	// H-3: Limit upload size to prevent disk exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20) // 500 MB max
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "Upload too large (max 500 MB)", http.StatusRequestEntityTooLarge)
		return
	}
	// 1. Приймаємо файл
	file, _, err := r.FormFile("project")
	if err != nil {
		http.Error(w, fmt.Sprintf("Помилка отримання файлу: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 2. Створюємо тимчасову папку
	buildID := fmt.Sprintf("p2ser-builds-%d", time.Now().UnixNano())
	tmpDir := filepath.Join(os.TempDir(), buildID)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir) // L-4: always clean up temp dirs

	archivePath := filepath.Join(tmpDir, "project.zip")
	out, err := os.Create(archivePath)
	if err != nil {
		http.Error(w, "Помилка створення файлу", http.StatusInternalServerError)
		return
	}
	io.Copy(out, file)
	out.Close()

	// H-1: Go-native ZIP extractor with Zip Slip prevention
	if err := safeUnzip(archivePath, tmpDir); err != nil {
		http.Error(w, fmt.Sprintf("Помилка розпакування: %v", err), http.StatusInternalServerError)
		return
	}

	// Якщо архів мав кореневу папку, знаходимо її
	projectDir := tmpDir
	entries, _ := os.ReadDir(tmpDir)
	for _, entry := range entries {
		if entry.IsDir() {
			// Перевіряємо чи там є docker-compose.yml
			if _, err := os.Stat(filepath.Join(tmpDir, entry.Name(), "docker-compose.yml")); err == nil {
				projectDir = filepath.Join(tmpDir, entry.Name())
				break
			}
		}
	}

	// 4. Передаємо в Builder
	pods, err := builder.BuildAndLoad(projectDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Помилка збірки проекту: %v", err), http.StatusInternalServerError)
		return
	}

	// 5. Відправляємо в Raft FSM
	for _, pod := range pods {
		podBytes, _ := json.Marshal(pod)
		cmd := map[string]string{
			"op":    "set",
			"key":   "pod:" + pod.ID,
			"value": string(podBytes),
		}
		cmdBytes, _ := json.Marshal(cmd)
		future := s.raftNode.Apply(cmdBytes, 5*time.Second)
		if err := future.Error(); err != nil {
			http.Error(w, fmt.Sprintf("Apply error: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Проект успішно зібрано і запущено!"))
}

// HandleDeployGit приймає Git URL, клонує, збирає і відправляє в кластер
func (s *APIServer) HandleDeployGit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// M-9: Write-forwarding for deploy handlers
	if s.forwardToLeader(w, r) {
		return
	}

	var reqData struct {
		URL    string            `json:"url"`
		Branch string            `json:"branch"`
		Env    map[string]string `json:"env"`
	}
	// H-2: limit body size
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&reqData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// H-4: Validate Git URL — allow only https/git schemes, block private IPs
	err := validateGitURL(reqData.URL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid Git URL: %v", err), http.StatusBadRequest)
		return
	}
	// H-4: Validate branch name
	validBranch := regexp.MustCompile(`^[a-zA-Z0-9._\-/]{0,100}$`)
	if reqData.Branch != "" && !validBranch.MatchString(reqData.Branch) {
		http.Error(w, "Invalid branch name", http.StatusBadRequest)
		return
	}

	buildID := fmt.Sprintf("p2ser-builds-git-%d", time.Now().UnixNano())
	tmpDir := filepath.Join(os.TempDir(), buildID)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir) // L-4: always clean up temp dirs

	// 1. Клонуємо репозиторій
	args := []string{"clone", "--depth=1"}
	if reqData.Branch != "" {
		args = append(args, "-b", reqData.Branch)
	}
	args = append(args, reqData.URL, tmpDir)

	if err := exec.Command("git", args...).Run(); err != nil {
		http.Error(w, fmt.Sprintf("Помилка клонування git: %v", err), http.StatusInternalServerError)
		return
	}

	// H-5: Sanitize .env keys and values to prevent newline injection
	if len(reqData.Env) > 0 {
		envContent := ""
		validKey := regexp.MustCompile(`^[A-Z_a-z][A-Z_a-z0-9]*$`)
		sanitizeVal := strings.NewReplacer("\n", "", "\r", "")
		for k, v := range reqData.Env {
			if !validKey.MatchString(k) {
				continue // skip invalid keys
			}
			envContent += fmt.Sprintf("%s=%s\n", k, sanitizeVal.Replace(v))
		}
		os.WriteFile(filepath.Join(tmpDir, ".env"), []byte(envContent), 0600)
	}

	// 3. Передаємо в Builder
	pods, err := builder.BuildAndLoad(tmpDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Помилка збірки проекту: %v", err), http.StatusInternalServerError)
		return
	}

	// 4. Відправляємо в Raft FSM
	for _, pod := range pods {
		podBytes, _ := json.Marshal(pod)
		cmd := map[string]string{
			"op":    "set",
			"key":   "pod:" + pod.ID,
			"value": string(podBytes),
		}
		cmdBytes, _ := json.Marshal(cmd)
		future := s.raftNode.Apply(cmdBytes, 5*time.Second)
		if err := future.Error(); err != nil {
			http.Error(w, fmt.Sprintf("Apply error: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Git-проект успішно склоновано, зібрано і запущено!"))
}

// HandleState відповідає за читання стану кластера
// 2.4.2: Деградація (Read-Only). Навіть ізольовані вузли без кворуму
// можуть безпечно виконувати читання з локальної bbolt БД.
func (s *APIServer) HandleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "Missing 'key' query parameter", http.StatusBadRequest)
		return
	}

	// Читаємо безпосередньо з локального cluster.FSM (Пункт 2.1.4: Read Tx)
	// Це працює паралельно, миттєво і не вимагає Raft-лідерства.
	val, err := s.fsm.GetState(key)
	if err != nil {
		http.Error(w, fmt.Sprintf("Read error: %v", err), http.StatusInternalServerError)
		return
	}

	if val == "" {
		http.Error(w, "Key not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(val))
}

// HandlePods повертає всі відомі Pod-и
func (s *APIServer) HandlePods(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}

	podsData, err := s.fsm.GetAllPods()
	if err != nil {
		http.Error(w, fmt.Sprintf("Read error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("["))
	for i, p := range podsData {
		w.Write([]byte(p))
		if i < len(podsData)-1 {
			w.Write([]byte(","))
		}
	}
	w.Write([]byte("]"))
}

// HandleNodes повертає список вузлів кластера (з точки зору Raft)
func (s *APIServer) HandleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}

	future := s.raftNode.GetConfiguration()
	if err := future.Error(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to get raft configuration: %v", err), http.StatusInternalServerError)
		return
	}

	leaderAddr := s.raftNode.Leader()
	servers := future.Configuration().Servers

	type NodeInfo struct {
		ID       string `json:"id"`
		Address  string `json:"address"`
		Role     string `json:"role"`
		CpuUsage int    `json:"cpuUsage"` // Відсоток (0-100)
		RamUsage int    `json:"ramUsage"` // Відсоток (0-100)
	}

	var nodes []NodeInfo
	for _, srv := range servers {
		role := "Follower"
		if srv.Address == leaderAddr {
			role = "Leader"
		} else if s.raftNode.State() == raft.Leader && string(srv.ID) == s.localNodeID {
			role = "Leader"
		}

		cpuUsage := 0
		ramUsage := 0
		
		if s.netManager != nil {
			if metrics, ok := s.netManager.GetNodeMetrics(string(srv.ID)); ok {
				cpuUsage = int(metrics.CPUUsage)
				total := metrics.RAMTotal
				if total <= 0 {
					total = 2048 // Fallback
				}
				ramUsage = 100 - int(float64(metrics.RAMFree)*100.0/float64(total))
				if ramUsage < 0 { ramUsage = 0 }
				if ramUsage > 100 { ramUsage = 100 }
			}
		}

		nodes = append(nodes, NodeInfo{
			ID:       string(srv.ID),
			Address:  string(srv.Address),
			Role:     role,
			CpuUsage: cpuUsage,
			RamUsage: ramUsage,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}

// HandleCompose приймає docker-compose.yaml і конвертує його в Pod-и (Пункт 6.1)
func (s *APIServer) HandleCompose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.forwardToLeader(w, r) {
		return
	}

	// H-2: limit body size to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB for compose files
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Request body too large or failed to read", http.StatusRequestEntityTooLarge)
		return
	}
	defer r.Body.Close()

	projectName := r.Header.Get("X-Project-Name")
	if projectName == "" {
		projectName = "default"
	}

	// H-6: Validate X-Working-Dir to prevent path traversal into sensitive directories
	workingDir := r.Header.Get("X-Working-Dir")
	if workingDir != "" {
		const allowedWorkdirRoot = "/var/lib/p2ser"
		clean := filepath.Clean(workingDir)
		if !strings.HasPrefix(clean+"/", allowedWorkdirRoot+"/") {
			http.Error(w, "Invalid X-Working-Dir: path not allowed", http.StatusBadRequest)
			return
		}
		workingDir = clean
	}

	// Ми лідер, парсимо файл
	parsedPods, err := compose.ParseComposeFile(projectName, body, nil, workingDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Parse error: %v", err), http.StatusBadRequest)
		return
	}

	// 1. Отримуємо існуючі Pod-и з бази
	existingByBase := make(map[string]scheduler.Pod)
	if podsData, err := s.fsm.GetAllPods(); err == nil {
		for _, data := range podsData {
			var p scheduler.Pod
			if err := json.Unmarshal([]byte(data), &p); err == nil {
				// Ігноруємо старі Pod-и, які вже знаходяться в процесі заміни
				if p.UpdateToPodID == "" {
					existingByBase[p.BaseID] = p
				}
			}
		}
	}

	// 2. Обробляємо нові та рахуємо diff
	for i := range parsedPods {
		newPod := &parsedPods[i]
		newPod.Project = projectName

		oldPod, exists := existingByBase[newPod.BaseID]

		if exists && oldPod.Project == projectName {
			if oldPod.Image != newPod.Image {
				// 6.4: Rolling Update (Zero-Downtime Deployment)
				log.Printf("Rolling Update: образ для %s змінився з %s на %s", newPod.BaseID, oldPod.Image, newPod.Image)

				// Створюємо новий Pod з унікальним ID
				newPod.ID = fmt.Sprintf("%s-%d", newPod.BaseID, time.Now().UnixNano())

				// Оновлюємо старий Pod (вказуємо, на який замінити)
				oldPod.UpdateToPodID = newPod.ID
				oldPodBytes, _ := json.Marshal(oldPod)

				cmdOld := map[string]string{
					"op":    "set",
					"key":   "pod:" + oldPod.ID,
					"value": string(oldPodBytes),
				}
				cmdOldBytes, _ := json.Marshal(cmdOld)
				applyFutureOld := s.raftNode.Apply(cmdOldBytes, 5*time.Second)
				if err := applyFutureOld.Error(); err != nil {
					log.Printf("API: Помилка збереження старого Pod %s (Rolling Update): %v", oldPod.ID, err)
					http.Error(w, fmt.Sprintf("Failed to apply old pod update: %v", err), http.StatusInternalServerError)
					return
				}
			} else {
				// Якщо образ не змінився, оновлюємо поточний (але зберігаємо ID і Status, щоб не перезапустити)
				newPod.ID = oldPod.ID
				newPod.Status = oldPod.Status
				newPod.NodeID = oldPod.NodeID
				newPod.Ready = oldPod.Ready
				newPod.ResourceVersion = oldPod.ResourceVersion
			}
		}

		// Зберігаємо новий Pod у Raft
		podBytes, _ := json.Marshal(newPod)
		cmd := map[string]string{
			"op":    "set",
			"key":   "pod:" + newPod.ID,
			"value": string(podBytes),
		}
		cmdBytes, _ := json.Marshal(cmd)

		applyFuture := s.raftNode.Apply(cmdBytes, 5*time.Second)
		if err := applyFuture.Error(); err != nil {
			log.Printf("API: Помилка збереження Pod %s: %v", newPod.ID, err)
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("Successfully parsed docker-compose and processed %d Pods in cluster\n", len(parsedPods))))
}


// Rate Limiter variables — M-2: map eviction to prevent unbounded memory growth
type visitorEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

var (
	visitors = make(map[string]*visitorEntry)
	mu       sync.Mutex
)

func init() {
	// Evict stale visitor entries every minute (M-2 fix)
	go func() {
		for range time.Tick(time.Minute) {
			mu.Lock()
			for ip, v := range visitors {
				if time.Since(v.lastSeen) > 3*time.Minute {
					delete(visitors, ip)
				}
			}
			mu.Unlock()
		}
	}()
}

func getVisitor(ip string) *rate.Limiter {
	mu.Lock()
	defer mu.Unlock()

	v, exists := visitors[ip]
	if !exists {
		// 11.4: 10 запитів на секунду з можливістю burst до 20
		v = &visitorEntry{
			limiter:  rate.NewLimiter(rate.Limit(10), 20),
			lastSeen: time.Now(),
		}
		visitors[ip] = v
	} else {
		v.lastSeen = time.Now()
	}
	return v.limiter
}

// 11.4: RateLimitMiddleware для захисту від DoS-атак
func RateLimitMiddleware(netManager *network.NetworkManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		
		limiter := getVisitor(ip)
		if !limiter.Allow() {
			log.Printf("[SECURITY] Rate Limit перевищено для IP: %s! Застосовуємо BanIP.", ip)
			if netManager != nil {
				// Динамічний бан на рівні ядра (ipset)
				_ = netManager.BanIP(ip)
			}
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware додає Access-логування (Аудит доступу)
// Ми логуємо RemoteAddr (IP) та метадані, але навмисно НЕ логуємо повне тіло (payload), щоб не розкривати паролі чи токени.
// L-1: Sanitize User-Agent and RemoteAddr to prevent log injection
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[AUDIT] Клієнт: %s | Метод: %s | Маршрут: %s | User-Agent: %s | Розмір тіла: %d",
			sanitizeLog(r.RemoteAddr), r.Method, r.URL.Path, sanitizeLog(r.UserAgent()), r.ContentLength)
		next.ServeHTTP(w, r)
	})
}


// BotProtectionMiddleware перевіряє User-Agent на наявність відомих ботів та сканерів.
// M-12 SECURITY NOTE: This is for crawler/noise filtering only. Do NOT rely on it as an access control mechanism, as User-Agent is trivially spoofed.
func BotProtectionMiddleware(netManager *network.NetworkManager, fsm *cluster.FSM, next http.Handler) http.Handler {
	// Список відомих шкідливих сканерів та AI-ботів (в нижньому регістрі)
	defaultBots := []string{
		"gptbot", "chatgpt-user", "anthropic", "claude", "applebot-extended",
		"bytespider", "ccbot", "diffbot", "facebookbot", "google-extended",
		"omgilibot", "perplexitybot", "masscan", "zgrab", "nmap", "sqlmap",
		"nikto", "dirbuster", "wpscan", "nuclei", "httrack",
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := strings.ToLower(r.UserAgent())

		// Комбінуємо дефолтні та динамічні
		allBots := append([]string{}, defaultBots...)
		if fsm != nil {
			dynamicBots, _ := fsm.GetAllBots()
			allBots = append(allBots, dynamicBots...)
		}

		for _, bot := range allBots {
			if bot != "" && strings.Contains(ua, bot) {
				ip, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					ip = r.RemoteAddr
				}
				// L-1: sanitize User-Agent before logging
				log.Printf("[SECURITY] Виявлено неавторизованого бота/сканера (%s) з IP: %s! Застосовуємо BanIP.", sanitizeLog(r.UserAgent()), sanitizeLog(ip))
				if netManager != nil {
					_ = netManager.BanIP(ip) // Бан на рівні iptables/ipset
				}

				// Повертаємо 403 Forbidden
				http.Error(w, "Forbidden: Bot/Scanner detected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// HandleGetLogs reads the log file of a pod
func (s *APIServer) HandleGetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}
	podID := r.URL.Query().Get("id")
	if podID == "" {
		http.Error(w, "id parameter required", http.StatusBadRequest)
		return
	}
	
	// H-10: validate podID to prevent path traversal
	if !validPodID.MatchString(podID) {
		http.Error(w, "Invalid pod id", http.StatusBadRequest)
		return
	}

	logPath := "/tmp/p2ser_" + podID + ".log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.Write([]byte("No logs found for this pod yet."))
			return
		}
		http.Error(w, "Failed to read logs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return last 20000 bytes if too big
	if len(data) > 20000 {
		data = data[len(data)-20000:]
	}
	w.Write(data)
}

// AuthMiddleware перевіряє наявність правильного токена у запиті (Bearer або query)
// C-5: використовуємо constant-time comparison щоб уникнути timing side-channel
func (s *APIServer) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Пропускаємо CORS preflight
		if r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}

		// Перевірка токена
		reqToken := r.Header.Get("Authorization")
		if reqToken != "" {
			reqToken = strings.TrimPrefix(reqToken, "Bearer ")
		}

		// H-12: WebSocket ticket authentication
		if r.Header.Get("Upgrade") == "websocket" {
			ticket := r.URL.Query().Get("ticket")
			if ticket != "" {
				if expiry, ok := s.wsTickets.LoadAndDelete(ticket); ok {
					if time.Now().Before(expiry.(time.Time)) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
		}

		// C-5: constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(reqToken), []byte(s.apiToken)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// CORSMiddleware — дозволяє CORS лише з того ж хоста (UI вбудований → same-origin).
// Для dev-режиму (окремий Vite) передається allowedOrigin = "*".
func CORSMiddleware(allowedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedOrigin == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			// Дозволяємо тільки запити з того ж хоста
			requestHost := r.Host
			if strings.HasPrefix(origin, "http://"+requestHost) ||
				strings.HasPrefix(origin, "https://"+requestHost) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			// якщо origin чужий — заголовок не виставляємо → браузер заблокує
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, Authorization, X-Env-Vars")
		w.Header().Set("Access-Control-Max-Age", "86400") // кеш preflight на добу

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}


// HandleDirList повертає список підкаталогів для UI File Browser
// C-1: restrict to allowed root to prevent full filesystem traversal
const dirListAllowedRoot = "/var/lib/p2ser"

func (s *APIServer) HandleDirList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}

	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = dirListAllowedRoot
	}

	// C-1: prevent path traversal — ensure path stays within allowed root
	clean := filepath.Clean(dir)
	if !strings.HasPrefix(clean+"/", dirListAllowedRoot+"/") && clean != dirListAllowedRoot {
		http.Error(w, "Forbidden: path not allowed", http.StatusForbidden)
		return
	}

	entries, err := os.ReadDir(clean)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path": clean,
		"dirs": dirs,
	})
}

// HandleStats повертає реальну статистику сервера (RAM, Load)
func (s *APIServer) HandleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}
	
	// Читаємо RAM
	mem, _ := os.ReadFile("/proc/meminfo")
	var total, free, available int
	lines := strings.Split(string(mem), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "MemTotal:") {
			fmt.Sscanf(l, "MemTotal: %d kB", &total)
		} else if strings.HasPrefix(l, "MemFree:") {
			fmt.Sscanf(l, "MemFree: %d kB", &free)
		} else if strings.HasPrefix(l, "MemAvailable:") {
			fmt.Sscanf(l, "MemAvailable: %d kB", &available)
		}
	}
	usedGB := float64(total-available) / (1024 * 1024)
	totalGB := float64(total) / (1024 * 1024)

	// Читаємо Load Average
	load, _ := os.ReadFile("/proc/loadavg")
	loadAvg := strings.Split(string(load), " ")[0]

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"cpu":          fmt.Sprintf("%s", loadAvg),
		"ram_used":     fmt.Sprintf("%.1f GB", usedGB),
		"ram_total":    fmt.Sprintf("%.1f GB", totalGB),
		"geo_ip_error": s.netManager.GetGeoIPError(),
	})
}

// HandleDeletePod зупиняє Pod (видаляє з Raft)
func (s *APIServer) HandleDeletePod(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Only DELETE allowed", http.StatusMethodNotAllowed)
		return
	}

	podID := r.URL.Query().Get("id")
	if podID == "" {
		http.Error(w, "Missing pod id", http.StatusBadRequest)
		return
	}

	// Видаляємо Pod з FSM (Raft)
	cmd := map[string]interface{}{
		"op":  "del",
		"key": "pod:" + podID,
	}
	cmdBytes, _ := json.Marshal(cmd)
	f := s.raftNode.Apply(cmdBytes, 500*time.Millisecond)
	if f.Error() != nil {
		http.Error(w, f.Error().Error(), http.StatusInternalServerError)
		return
	}

	w.Write([]byte("Pod stopped (deleted from cluster)\n"))
}

// HandleRestartPod примусово перезапускає Pod
func (s *APIServer) HandleRestartPod(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	podID := r.URL.Query().Get("id")
	if podID == "" {
		http.Error(w, "Missing pod id", http.StatusBadRequest)
		return
	}

	// Отримуємо поточний стан Pod-а з FSM
	podDataStr, err := s.fsm.GetState("pod:" + podID)
	if err != nil || podDataStr == "" {
		http.Error(w, "Pod not found", http.StatusNotFound)
		return
	}

	var pod scheduler.Pod
	json.Unmarshal([]byte(podDataStr), &pod)

	// Скидаємо NodeID і статус, щоб планувальник міг знову його зашедулити
	// а Agent зупинив його, бо він більше не Scheduled на його вузлі.
	pod.Status = "Pending"
	pod.NodeID = ""
	pod.ResourceVersion++
	
	podDataBytes, _ := json.Marshal(pod)
	cmd := map[string]interface{}{
		"op":    "set",
		"key":   "pod:" + podID,
		"value": string(podDataBytes),
	}
	cmdBytes, _ := json.Marshal(cmd)
	f := s.raftNode.Apply(cmdBytes, 500*time.Millisecond)
	if f.Error() != nil {
		http.Error(w, f.Error().Error(), http.StatusInternalServerError)
		return
	}

	w.Write([]byte("Pod restart initiated (set to Pending)\n"))
}

// HandleExecPod відкриває WebSocket з'єднання і прокидає його до процесу /bin/sh в контейнері
func (s *APIServer) HandleExecPod(w http.ResponseWriter, r *http.Request) {
	podID := r.URL.Query().Get("id")
	if podID == "" {
		http.Error(w, "Missing pod id", http.StatusBadRequest)
		return
	}
	// C-2: Validate pod ID format to prevent command injection
	if !validPodID.MatchString(podID) {
		http.Error(w, "Invalid pod id format", http.StatusBadRequest)
		return
	}

	// H-8: validate WebSocket origin to prevent CSRF
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // allow requests without Origin (native clients, curl)
			}
			return origin == "https://"+r.Host || origin == "http://"+r.Host
		},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 1. Отримуємо стан Pod, щоб дізнатися, на якому вузлі він працює
	podDataStr, err := s.fsm.GetState("pod:" + podID)
	if err != nil || podDataStr == "" {
		conn.WriteMessage(websocket.TextMessage, []byte("Pod not found in state\r\n"))
		return
	}
	var pod scheduler.Pod
	json.Unmarshal([]byte(podDataStr), &pod)

	localNode := s.localNodeID // string ID
	if pod.NodeID != localNode {
		// ПРОКСІЮВАННЯ НА ВУЗОЛ, ДЕ ЗАПУЩЕНО POD
		future := s.raftNode.GetConfiguration()
		if err := future.Error(); err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte("Cannot get configuration\r\n"))
			return
		}
		var targetAddr string
		for _, srv := range future.Configuration().Servers {
			if string(srv.ID) == pod.NodeID {
				targetAddr = string(srv.Address)
				break
			}
		}
		if targetAddr == "" {
			conn.WriteMessage(websocket.TextMessage, []byte("Node not found\r\n"))
			return
		}

		wsURL := fmt.Sprintf("ws://%s/pod/exec?id=%s", targetAddr, podID)
		headers := http.Header{}
		headers.Add("Authorization", "Bearer "+s.apiToken)
		targetConn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Proxy failed: %v\r\n", err)))
			return
		}
		defer targetConn.Close()

		errc := make(chan error, 2)
		go func() {
			for {
				msgType, p, err := conn.ReadMessage()
				if err != nil { errc <- err; return }
				targetConn.WriteMessage(msgType, p)
			}
		}()
		go func() {
			for {
				msgType, p, err := targetConn.ReadMessage()
				if err != nil { errc <- err; return }
				conn.WriteMessage(msgType, p)
			}
		}()
		<-errc
		return
	}

	// 2. Якщо Pod працює на НАШОМУ вузлі, виконуємо exec через ctr
	conn.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[33m*** Starting terminal session... ***\x1b[0m\r\n"))
	
	execID := fmt.Sprintf("exec-%d", time.Now().UnixNano())
	cmd := exec.Command("ctr", "-n", "p2ser", "tasks", "exec", "--exec-id", execID, "-t", podID, "/bin/sh")
	
	// Create pipes
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	
	if err := cmd.Start(); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\x1b[31mError starting ctr: %v\x1b[0m\r\n", err)))
		return
	}
	
	errc := make(chan error, 2)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		for {
			_, p, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			stdin.Write(p)
		}
	}()
	
	<-errc
	stdin.Close()
	cmd.Process.Kill()
	cmd.Wait()
}

// HandleManageBot дозволяє додавати або видаляти ботів з чорного списку (динамічно через Raft)
func (s *APIServer) HandleManageBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "Only POST or DELETE allowed", http.StatusMethodNotAllowed)
		return
	}

	botName := r.URL.Query().Get("name")
	if botName == "" {
		http.Error(w, "Missing 'name' parameter", http.StatusBadRequest)
		return
	}
	botName = strings.ToLower(botName)

	if s.raftNode.State() != raft.Leader {
		leaderAddr := s.raftNode.Leader()
		if leaderAddr == "" {
			http.Error(w, "Leader not found", http.StatusServiceUnavailable)
			return
		}
		// Проксі на лідера
		proxyReq, _ := http.NewRequest(r.Method, fmt.Sprintf("http://%s%s", leaderAddr, r.URL.Path+"?"+r.URL.RawQuery), r.Body)
		proxyReq.Header.Set("Authorization", "Bearer "+s.apiToken)
		client := &http.Client{Timeout: 10 * time.Second} // M-6: always set timeout
		resp, err := client.Do(proxyReq)
		if err != nil {
			http.Error(w, "Failed to proxy request", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	op := "set"
	if r.Method == http.MethodDelete {
		op = "del"
	}

	cmd := map[string]string{
		"op":    op,
		"key":   "bot:" + botName,
		"value": "true",
	}
	cmdBytes, _ := json.Marshal(cmd)

	applyFuture := s.raftNode.Apply(cmdBytes, 5*time.Second)
	if err := applyFuture.Error(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to update bot list: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	if op == "set" {
		w.Write([]byte(fmt.Sprintf("Bot '%s' added to blacklist\n", botName)))
	} else {
		w.Write([]byte(fmt.Sprintf("Bot '%s' removed from blacklist\n", botName)))
	}
}

// HandleBan обробляє запити на блокування IP (Пункт 7.4)
func (s *APIServer) HandleBan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	targetIP := r.URL.Query().Get("ip")
	if targetIP == "" {
		http.Error(w, "Missing 'ip' parameter", http.StatusBadRequest)
		return
	}

	if s.netManager == nil {
		http.Error(w, "network.NetworkManager is not initialized", http.StatusInternalServerError)
		return
	}

	err := s.netManager.BanIP(targetIP)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to ban IP: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("IP %s successfully banned.\n", targetIP)))
}

// ServeHTTP запускає HTTP сервер на переданому Listener (від cmux)
// L-3: default to same-origin instead of wildcard CORS
func (s *APIServer) Serve(listener net.Listener) error {
	return s.ServeWithUI(listener, nil, "same-origin")
}

// ServeWithUI запускає сервер з вбудованим UI (uiFS) або без нього.
// allowedOrigin: "*" для dev-режиму, або конкретний host для продакшну.
func (s *APIServer) ServeWithUI(listener net.Listener, uiFS http.FileSystem, allowedOrigin string) error {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/apply", s.HandleApply)
	apiMux.HandleFunc("/state", s.HandleState)
	apiMux.HandleFunc("/pods", s.HandlePods)
	apiMux.HandleFunc("/pod", s.HandleDeletePod)
	apiMux.HandleFunc("/pod/restart", s.HandleRestartPod)
	apiMux.HandleFunc("/pod/exec", s.HandleExecPod)
	apiMux.HandleFunc("/pod/logs", s.HandleGetLogs)
	apiMux.HandleFunc("/nodes", s.HandleNodes)
	apiMux.HandleFunc("/compose", s.HandleCompose)
	apiMux.HandleFunc("/upload", s.HandleUploadSource)
	apiMux.HandleFunc("/deploy-git", s.HandleDeployGit)
	apiMux.HandleFunc("/ls", s.HandleDirList)
	apiMux.HandleFunc("/stats", s.HandleStats)
	apiMux.HandleFunc("/ban", s.HandleBan)
	apiMux.HandleFunc("/bot", s.HandleManageBot)
	apiMux.HandleFunc("/ticket", s.HandleGetWsTicket) // H-12

	// Захищений API (токен обов'язковий)
	protectedAPI := CORSMiddleware(allowedOrigin,
		LoggingMiddleware(
			RateLimitMiddleware(s.netManager,
				BotProtectionMiddleware(s.netManager, s.fsm,
					s.AuthMiddleware(apiMux)))))

	rootMux := http.NewServeMux()

	if uiFS != nil {
		// UI роздається без токена (публічні статичні файли)
		// але захищені same-origin CORS — зовнішні сайти не можуть їх читати
		uiHandler := http.FileServer(uiFS)
		rootMux.Handle("/assets/", uiHandler)
		rootMux.Handle("/favicon.ico", uiHandler)
		// SPA fallback — всі невідомі GET → index.html
		rootMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
				// Спочатку пробуємо як API
				if strings.HasPrefix(r.URL.Path, "/pod") ||
					strings.HasPrefix(r.URL.Path, "/compose") ||
					strings.HasPrefix(r.URL.Path, "/apply") ||
					strings.HasPrefix(r.URL.Path, "/state") ||
					strings.HasPrefix(r.URL.Path, "/nodes") ||
					strings.HasPrefix(r.URL.Path, "/upload") ||
					strings.HasPrefix(r.URL.Path, "/deploy-git") ||
					strings.HasPrefix(r.URL.Path, "/stats") ||
					strings.HasPrefix(r.URL.Path, "/pods") ||
					strings.HasPrefix(r.URL.Path, "/ban") ||
					strings.HasPrefix(r.URL.Path, "/bot") ||
					strings.HasPrefix(r.URL.Path, "/ls") {
					protectedAPI.ServeHTTP(w, r)
					return
				}
			}
			// SPA: повертаємо index.html для всіх інших шляхів
			index, err := uiFS.Open("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer index.Close()
			http.ServeContent(w, r, "index.html", time.Time{}, index.(io.ReadSeeker))
		})
	} else {
		// Без вбудованого UI — всі запити йдуть в API
		rootMux.Handle("/", protectedAPI)
	}

	// API маршрути завжди захищені
	rootMux.Handle("/apply", protectedAPI)
	rootMux.Handle("/state", protectedAPI)
	rootMux.Handle("/pods", protectedAPI)
	rootMux.Handle("/pod", protectedAPI)
	rootMux.Handle("/pod/restart", protectedAPI)
	rootMux.Handle("/pod/exec", protectedAPI)
	rootMux.Handle("/pod/logs", protectedAPI)
	rootMux.Handle("/nodes", protectedAPI)
	rootMux.Handle("/compose", protectedAPI)
	rootMux.Handle("/upload", protectedAPI)
	rootMux.Handle("/deploy-git", protectedAPI)
	rootMux.Handle("/ls", protectedAPI)
	rootMux.Handle("/stats", protectedAPI)
	rootMux.Handle("/ban", protectedAPI)
	rootMux.Handle("/bot", protectedAPI)

	server := &http.Server{
		Handler:           rootMux,
		ReadHeaderTimeout: 5 * time.Second,  // L-5: prevent Slowloris attack
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return server.Serve(listener)
}

// HandleGetWsTicket видає одноразовий квиток для доступу по WebSocket (H-12)
func (s *APIServer) HandleGetWsTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	ticketBytes := make([]byte, 16)
	if _, err := rand.Read(ticketBytes); err != nil {
		http.Error(w, "Failed to generate ticket", http.StatusInternalServerError)
		return
	}
	ticket := fmt.Sprintf("%x", ticketBytes)
	
	s.wsTickets.Store(ticket, time.Now().Add(1*time.Minute))
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ticket": ticket})
}

// safeUnzip extracts a ZIP archive safely, preventing Zip Slip attacks (H-1).
// Every entry is validated to ensure it stays within the destination directory.
func safeUnzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	destAbs := filepath.Clean(dest) + string(os.PathSeparator)

	for _, f := range r.File {
		// Build destination path
		dst := filepath.Join(dest, f.Name)
		// Zip Slip protection: ensure dst is within dest
		if !strings.HasPrefix(filepath.Clean(dst)+string(os.PathSeparator), destAbs) {
			return fmt.Errorf("zip slip attempt detected: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0755); err != nil {
				return err
			}
			continue
		}

		// Ensure parent dir exists
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}

		// Limit individual file size to 200 MB
		if f.UncompressedSize64 > 200<<20 {
			return fmt.Errorf("file too large in archive: %s", f.Name)
		}

		outFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, io.LimitReader(rc, 200<<20))
		rc.Close()
		outFile.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// validateGitURL validates a Git URL to prevent SSRF attacks (H-4).
// Only https:// and git:// schemes are allowed; private/internal IPs are blocked.
func validateGitURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL is required")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Allow only safe schemes
	switch parsed.Scheme {
	case "https", "git":
		// OK
	default:
		return fmt.Errorf("scheme %q not allowed (use https:// or git://)", parsed.Scheme)
	}

	// Resolve host to IPs and block private/loopback ranges
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("empty host in URL")
	}

	addrs, err := net.LookupHost(host)
	if err != nil {
		// If DNS fails, still allow (might work at clone time; don't pre-block)
		return nil
	}

	privateRanges := []string{
		"10.", "172.16.", "172.17.", "172.18.", "172.19.",
		"172.20.", "172.21.", "172.22.", "172.23.", "172.24.",
		"172.25.", "172.26.", "172.27.", "172.28.", "172.29.",
		"172.30.", "172.31.", "192.168.", "127.", "169.254.", "::1", "fc", "fd",
	}

	for _, addr := range addrs {
		for _, private := range privateRanges {
			if strings.HasPrefix(addr, private) {
				return fmt.Errorf("URL resolves to private/internal address %s — blocked for security", addr)
			}
		}
	}

	// Suppress unused import warnings (strconv used for port validation elsewhere if needed)
	_ = strconv.Itoa
	return nil
}
