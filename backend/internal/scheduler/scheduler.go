package scheduler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/kovach/p2ser/internal/network"
)

// HealthCheck описує конфігурацію перевірки (Пункт 5.3)
type HealthCheck struct {
	Type string `json:"type"` // "http" або "tcp"
	Port int    `json:"port"` // Порт для перевірки
	Path string `json:"path"` // Шлях для HTTP перевірки (наприклад "/healthz")
}

// DependsOnConfig описує умову залежності (Пункт 6.3)
type DependsOnConfig struct {
	Condition string `json:"condition" yaml:"condition"`
}

// Pod представляє одиницю розгортання в нашій системі
type Pod struct {
	ID              string `json:"id"`
	BaseID          string `json:"base_id,omitempty"` // 6.4: Оригінальне ім'я репліки
	Image           string `json:"image"`             // Назва образу (напр. "nginx:alpine")
	Status          string `json:"status"`            // "Pending", "Scheduled", "Running"
	NodeID          string `json:"node_id"`
	ResourceVersion int    `json:"resource_version"`
	LeaseExpiresAt  int64  `json:"lease_expires_at"` // 3.1.2: TTL оренди
	Project         string `json:"project"`          // 10.5: Концепція Проектів

	// Вимоги до ресурсів
	CPUReq float64 `json:"cpu_req"`
	RAMReq int     `json:"ram_req"`
	Arch   string  `json:"arch"`

	// Порти
	Ports []string `json:"ports,omitempty"`
	
	// Томи (Volumes)
	Volumes []string `json:"volumes,omitempty"`

	// Метадані для планування
	App  string `json:"app"`  // 3.2.3: Ідентифікатор сервісу для Anti-Affinity
	Role string `json:"role"` // 3.3: "Active" або "Standby"

	// 5.3: Мережа та Healthchecks
	PodIP          string       `json:"pod_ip"` // Локальна IP адреса контейнера
	Ready          bool         `json:"ready"`  // Чи готова програма всередині приймати трафік
	LivenessProbe  *HealthCheck `json:"liveness_probe,omitempty"`
	ReadinessProbe *HealthCheck `json:"readiness_probe,omitempty"`

	// 5.4: Stateful
	IsStateful bool `json:"is_stateful"` // Якщо true, підлягає Fencing-у при втраті кворуму

	// Безпека (Security Context)
	RunAsUser   string `json:"run_as_user,omitempty"` // UID:GID для запуску без рута (напр. "1000:1000")
	UsernsRemap bool   `json:"userns_remap"`          // Якщо true, рут в контейнері мапиться на непривілейованого юзера на хості

	// 6.3: Контроль залежностей
	DependsOn map[string]DependsOnConfig `json:"depends_on,omitempty"`

	// Змінні середовища
	Env []string `json:"env,omitempty"`

	// 6.4: Rolling Update
	UpdateToPodID string `json:"update_to_pod_id,omitempty"` // ID нового Pod-а, який має його замінити
}

// Scheduler відповідає за децентралізоване планування (Розділ 3)
type Scheduler struct {
	nodeID           string
	apiAddr          string
	apiToken         string
	meta             network.NodeMetadata
	ContainerChecker func(ctx context.Context, id string) (bool, error)
	IsNodeAlive      func(nodeID string) bool
}

func NewScheduler(nodeID, apiAddr string, meta network.NodeMetadata, apiToken string) *Scheduler {
	return &Scheduler{
		nodeID:   nodeID,
		apiAddr:  apiAddr,
		meta:     meta,
		apiToken: apiToken,
	}
}

// Start запускає фоновий цикл планувальника
func (s *Scheduler) Start() {
	go func() {
		for {
			time.Sleep(2 * time.Second)
			s.reconcile()
		}
	}()

	// Окремий потік для оновлення оренди (Heartbeat) - Пункт 3.1.2
	go func() {
		for {
			time.Sleep(5 * time.Second)
			s.renewLeases()
		}
	}()
}

func (s *Scheduler) FetchPods() ([]Pod, error) {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/pods", s.apiAddr), nil)
	if s.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiToken)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var pods []Pod
	if err := json.NewDecoder(resp.Body).Decode(&pods); err != nil {
		return nil, err
	}
	return pods, nil
}

func (s *Scheduler) reconcile() {
	pods, err := s.FetchPods()
	if err != nil {
		log.Printf("Scheduler: не вдалося отримати список Pod-ів: %v", err)
		return
	}

	now := time.Now().Unix()

	// 6.4: Rolling Update (Zero-Downtime)
	// Перевіряємо старі поди, які чекають на нові
	for _, oldPod := range pods {
		if oldPod.UpdateToPodID != "" {
			var newPod *Pod
			for _, p := range pods {
				if p.ID == oldPod.UpdateToPodID {
					pCopy := p
					newPod = &pCopy
					break
				}
			}

			if newPod != nil && newPod.Status == "Running" {
				// Якщо є Readiness probe, чекаємо Ready
				if newPod.ReadinessProbe == nil || newPod.Ready {
					log.Printf("Rolling Update: Новий Pod %s готовий! Видаляємо старий %s (Zero-Downtime)", newPod.ID, oldPod.ID)
					s.deletePod(oldPod.ID)
				}
			}
		}
	}

	// 3.3: Перевіряємо, чи потрібно перевести локальний Standby в Active
	s.reconcileStandby(pods, now)

	for _, pod := range pods {
		// Шукаємо Pending або ті, що "згоріли" (LeaseExpired)
		isPending := pod.Status == "Pending"
		isExpired := (pod.Status == "Scheduled" || pod.Status == "Running") && pod.LeaseExpiresAt > 0 && pod.LeaseExpiresAt < now

		if !isPending && !isExpired {
			continue
		}

		if isExpired {
			log.Printf("Scheduler: Знайдено Pod %s з простроченою орендою (Heartbeat failed). Вузол %s ймовірно впав.", pod.ID, pod.NodeID)
		}

		// 6.3: Контроль залежностей (DAG)
		if len(pod.DependsOn) > 0 {
			if !s.checkDependencies(pods, pod.DependsOn) {
				// Залишаємо в Pending, поки залежності не будуть виконані
				continue
			}
		}

		// 8.2: Підтримка Multi-Arch (Фільтрація)
		if pod.Arch != "" && pod.Arch != s.meta.Arch {
			continue
		}

		safeRAM := float64(s.meta.FreeRAM) * 0.90
		if float64(pod.RAMReq) > safeRAM {
			continue
		}

		// 3.2.3: Anti-Affinity
		hasSameService := s.checkLocalAntiAffinity(pods, pod.App)
		antiAffinityPenaltyMs := 0
		if hasSameService {
			antiAffinityPenaltyMs = 5000
			log.Printf("Scheduler: Anti-Affinity спрацював для Pod %s (App: %s). Додаємо штраф 5000мс", pod.ID, pod.App)
		}

		// 3.2.2: Зважений Jitter
		fillRatio := float64(pod.RAMReq) / safeRAM
		baseDelayMs := 300
		if fillRatio > 0.8 {
			baseDelayMs = 10
		}

		jitter := rand.Intn(100)
		delay := time.Duration(baseDelayMs+jitter+antiAffinityPenaltyMs) * time.Millisecond

		log.Printf("Scheduler: Намагаюсь захопити Pod %s через %v...", pod.ID, delay)
		time.Sleep(delay)

		// Намагаємось захопити (CAS)
		s.tryLockPod(pod)

		// Обробляємо по одному за цикл
		return
	}
}

func (s *Scheduler) checkLocalAntiAffinity(allPods []Pod, app string) bool {
	if app == "" {
		return false
	}

	// Шукаємо, чи є на нашому вузлі вже запущений Pod такого ж додатку
	for _, p := range allPods {
		if p.NodeID == s.nodeID && p.App == app && (p.Status == "Scheduled" || p.Status == "Running") {
			return true
		}
	}
	return false
}

// 6.3: Перевірка виконання залежностей Pod-а
func (s *Scheduler) checkDependencies(allPods []Pod, dependsOn map[string]DependsOnConfig) bool {
	for depApp, config := range dependsOn {
		met := false
		for _, p := range allPods {
			if p.App == depApp && p.Role == "Active" {
				if config.Condition == "service_healthy" {
					// БД має бути Running та Ready (пройти Healthcheck)
					if p.Status == "Running" && p.Ready {
						met = true
						break
					}
				} else {
					// service_started (по замовчуванню)
					if p.Status == "Running" || p.Status == "Scheduled" {
						met = true
						break
					}
				}
			}
		}
		if !met {
			return false // Хоча б одна залежність не готова
		}
	}
	return true
}

// 3.3: Warm Standby (Резервування N+1)
// Вузол аналізує свої локальні Standby-контейнери. Якщо він бачить, що Active-репліка
// цього ж додатку впала (прострочена оренда), він миттєво активує свій Standby.
func (s *Scheduler) reconcileStandby(allPods []Pod, now int64) {
	for _, localPod := range allPods {
		if localPod.NodeID == s.nodeID && localPod.Role == "Standby" && (localPod.Status == "Scheduled" || localPod.Status == "Running") {
			// Шукаємо впавшу Active-репліку цього ж додатку
			for _, otherPod := range allPods {
				if otherPod.App == localPod.App && otherPod.Role == "Active" {
					isExpired := otherPod.LeaseExpiresAt > 0 && otherPod.LeaseExpiresAt < now
					
					// C-11: Stronger death confirmation for Active pod before promotion
					confirmedDead := isExpired
					if confirmedDead && s.IsNodeAlive != nil {
						if s.IsNodeAlive(otherPod.NodeID) && otherPod.Status != "Fenced" {
							log.Printf("Warm Standby: Active %s прострочив оренду, але вузол %s живий. Скасування промоушена (Split-Brain protection).", otherPod.ID, otherPod.NodeID)
							confirmedDead = false
						}
					}

					if confirmedDead {
						log.Printf("Warm Standby: Виявлено підтверджене падіння Active репліки %s (App: %s).", otherPod.ID, otherPod.App)
						log.Printf("Warm Standby: Миттєво переводимо локальний Standby %s в Active-режим!", localPod.ID)

						// Промоутимо себе в Active
						localPod.Role = "Active"
						localPod.Status = "Running"
						s.tryLockPod(localPod) // CAS запит

						// Тут оркестратор також генерує новий Pending Standby Pod у cluster.FSM для відновлення резерву N+1
						s.createNewStandbyPod(localPod.App, localPod.CPUReq, localPod.RAMReq, localPod.Arch)
						return
					}
				}
			}
		}
	}
}

func (s *Scheduler) createNewStandbyPod(app string, cpuReq float64, ramReq int, arch string) {
	newPod := Pod{
		ID:              fmt.Sprintf("pod-%s-standby-%d", app, time.Now().UnixNano()),
		Status:          "Pending",
		Role:            "Standby",
		App:             app,
		CPUReq:          cpuReq,
		RAMReq:          ramReq,
		Arch:            arch,
		ResourceVersion: 0,
	}

	podData, _ := json.Marshal(newPod)
	cmd := struct {
		Op              string `json:"op"`
		Key             string `json:"key"`
		Value           string `json:"value"`
		ExpectedVersion int    `json:"expected_version"`
	}{
		Op:              "cas",
		Key:             "pod:" + newPod.ID,
		Value:           string(podData),
		ExpectedVersion: 0,
	}

	cmdBytes, _ := json.Marshal(cmd)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/apply", s.apiAddr), bytes.NewBuffer(cmdBytes))
	req.Header.Set("Content-Type", "application/json")
	if s.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiToken)
	}
	client := &http.Client{}
	client.Do(req)
	log.Printf("Warm Standby: Заплановано нову Standby-репліку %s для відновлення N+1", newPod.ID)
}

// renewLeases поновлює оренду для всіх власних Pod-ів (Пункт 3.1.2)
func (s *Scheduler) renewLeases() {
	pods, err := s.FetchPods()
	if err != nil {
		return
	}

	for _, pod := range pods {
		if pod.NodeID == s.nodeID && (pod.Status == "Scheduled" || pod.Status == "Running") {
			// C-10: Check actual container state before renewing
			if s.ContainerChecker != nil {
				isRunning, _ := s.ContainerChecker(context.Background(), pod.ID)
				if !isRunning {
					continue
				}
			}
			// Поновлюємо оренду на +15 секунд (Heartbeat)
			s.tryLockPod(pod)
		}
	}
}

// tryLockPod реалізує 3.1.1: Оптимістичне блокування (CAS)
func (s *Scheduler) tryLockPod(pod Pod) {
	expectedVersion := pod.ResourceVersion

	// Оновлюємо статус на свій вузол (але не перезаписуємо Running)
	if pod.Status == "Pending" || pod.Status == "" {
		pod.Status = "Scheduled"
	}
	pod.NodeID = s.nodeID
	pod.ResourceVersion++                                        // Збільшуємо версію
	pod.LeaseExpiresAt = time.Now().Add(15 * time.Second).Unix() // 3.1.2: TTL 15 секунд

	podData, _ := json.Marshal(pod)

	cmd := struct {
		Op              string `json:"op"`
		Key             string `json:"key"`
		Value           string `json:"value"`
		ExpectedVersion int    `json:"expected_version"`
	}{
		Op:              "cas",
		Key:             "pod:" + pod.ID,
		Value:           string(podData),
		ExpectedVersion: expectedVersion,
	}

	cmdBytes, _ := json.Marshal(cmd)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/apply", s.apiAddr), bytes.NewBuffer(cmdBytes))
	req.Header.Set("Content-Type", "application/json")
	if s.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiToken)
	}
	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		log.Printf("Scheduler: Помилка мережі при спробі CAS: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf("Scheduler: Успішно захоплено Pod %s (CAS пройшов)", pod.ID)
	} else {
		log.Printf("Scheduler: Не вдалося захопити Pod %s (Можливо інший вузол встиг швидше). Код: %d", pod.ID, resp.StatusCode)
	}
}

// 6.4: Видалення старого Pod-а
func (s *Scheduler) deletePod(podID string) {
	cmd := struct {
		Op  string `json:"op"`
		Key string `json:"key"`
	}{
		Op:  "del",
		Key: "pod:" + podID,
	}
	cmdBytes, _ := json.Marshal(cmd)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/apply", s.apiAddr), bytes.NewBuffer(cmdBytes))
	req.Header.Set("Content-Type", "application/json")
	if s.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiToken)
	}
	client := &http.Client{}
	client.Do(req)
}

// UpdatePod дозволяє безпечно оновити стан Pod (напр. з Scheduled в Running)
func (s *Scheduler) UpdatePod(pod Pod) {
	expectedVersion := pod.ResourceVersion
	pod.ResourceVersion++

	podData, _ := json.Marshal(pod)

	cmd := struct {
		Op              string `json:"op"`
		Key             string `json:"key"`
		Value           string `json:"value"`
		ExpectedVersion int    `json:"expected_version"`
	}{
		Op:              "cas",
		Key:             "pod:" + pod.ID,
		Value:           string(podData),
		ExpectedVersion: expectedVersion,
	}

	cmdBytes, _ := json.Marshal(cmd)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/apply", s.apiAddr), bytes.NewBuffer(cmdBytes))
	req.Header.Set("Content-Type", "application/json")
	if s.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiToken)
	}
	client := &http.Client{}
	client.Do(req)
}
