package engine

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/kovach/p2ser/internal/scheduler"
)

// Agent відповідає за узгодження стану Pod-ів з containerd (Пункт 5.2 та 5.1)
type Agent struct {
	nodeID    string
	scheduler *scheduler.Scheduler
	cm        *ContainerManager
}

func NewAgent(nodeID string, scheduler *scheduler.Scheduler, cm *ContainerManager) *Agent {
	return &Agent{
		nodeID:    nodeID,
		scheduler: scheduler,
		cm:        cm,
	}
}

// checkHealth виконує перевірку Liveness/Readiness (Пункт 5.3)
func (a *Agent) checkHealth(probe *scheduler.HealthCheck, podIP string) bool {
	// Якщо перевірка не задана, або IP ще немає (наприклад, CNI ще не відпрацював) - за замовчуванням вважаємо живим/готовим
	if probe == nil || podIP == "" {
		return true
	}

	address := fmt.Sprintf("%s:%d", podIP, probe.Port)

	if probe.Type == "tcp" {
		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}

	if probe.Type == "http" {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s%s", address, probe.Path))
		if err != nil || resp.StatusCode >= 400 {
			return false
		}
		return true
	}

	return true
}

// Start запускає цикл узгодження
func (a *Agent) Start() {
	go func() {
		for {
			time.Sleep(5 * time.Second)
			a.reconcile()
		}
	}()
}

func (a *Agent) reconcile() {
	// 1. Отримуємо список всіх Pod-ів
	pods, err := a.scheduler.FetchPods()
	if err != nil {
		log.Printf("Agent: Помилка отримання Pod-ів: %v", err)
		return
	}

	// 2. Фільтруємо Pod-и, які призначені для нашого вузла
	var myPods []scheduler.Pod
	for _, pod := range pods {
		if pod.NodeID == a.nodeID {
			myPods = append(myPods, pod)
		}
	}

	// 3. Звіряємо бажаний стан із фактичним (containerd)
	// Це і є реалізація Пункту 5.2 (Level-Triggered Reconciliation)
	ctx := context.Background()

	for _, pod := range myPods {
		if pod.Status == "Scheduled" || pod.Status == "Running" {
			// Перевіряємо фактичний стан в containerd
			isRunning, err := a.cm.IsContainerRunning(ctx, pod.ID)
			if err != nil {
				log.Printf("Agent: помилка перевірки стану контейнера %s: %v", pod.ID, err)
				continue
			}

			// Якщо бажаний стан (Scheduled/Running), а фактичний - контейнер не працює (впав або був вбитий)
			if !isRunning {
				log.Printf("Agent: [Розбіжність] Контейнер %s має працювати, але він зупинений. Перезапускаємо...", pod.ID)

				// Спочатку очищуємо старі залишки, якщо контейнер впав з помилкою
				_ = a.cm.StopContainer(ctx, pod.ID)

				// Образ береться з конфігурації scheduler.Pod
				imageName := pod.Image
				if imageName == "" {
					log.Printf("Agent: Помилка! Для контейнера %s не вказано образ (image: порожній). Запуск скасовано.", pod.ID)
					continue
				}

				podIP, err := a.cm.RunContainer(ctx, pod)
				if err != nil {
					log.Printf("Agent: Помилка перезапуску контейнера %s: %v", pod.ID, err)
				} else {
					log.Printf("Agent: Контейнер %s успішно (пере)запущений через containerd!", pod.ID)
					
					needsUpdate := false
					if pod.Status == "Scheduled" {
						pod.Status = "Running"
						needsUpdate = true
					}
					if pod.PodIP != podIP {
						pod.PodIP = podIP
						needsUpdate = true
					}

					if needsUpdate {
						// Оновлюємо статус та IP в базі даних через API
						a.scheduler.UpdatePod(pod)
					}
				}
			} else if isRunning {
				if pod.Status == "Scheduled" {
					pod.Status = "Running"
					a.scheduler.UpdatePod(pod)
				}
				// Пункт 5.3: Перевірки Liveness та Readiness (якщо контейнер зараз працює)
				livenessOK := a.checkHealth(pod.LivenessProbe, pod.PodIP)
				if !livenessOK {
					log.Printf("Agent: [Healthcheck] Контейнер %s провалив Liveness Probe. Примусовий перезапуск!", pod.ID)
					_ = a.cm.StopContainer(ctx, pod.ID)
					// Після зупинки, на наступній ітерації він буде перезапущений
					continue
				}

				readinessOK := a.checkHealth(pod.ReadinessProbe, pod.PodIP)
				if readinessOK != pod.Ready {
					pod.Ready = readinessOK
					if pod.Ready {
						log.Printf("Agent: [Healthcheck] Контейнер %s пройшов Readiness Probe і готовий приймати трафік!", pod.ID)
					} else {
						log.Printf("Agent: [Healthcheck] Контейнер %s НЕ пройшов Readiness Probe (тимчасово недоступний).", pod.ID)
					}
					// TODO: відправити оновлення pod.Ready на API сервер (маршрутизатор перенаправить/перестане перенаправляти трафік)
				}
			}
		}
	}
}

// FenceStatefulPods примусово зупиняє всі Stateful-контейнери при втраті кворуму (Пункт 5.4)
func (a *Agent) FenceStatefulPods() {
	pods, err := a.scheduler.FetchPods()
	if err != nil {
		log.Printf("Fencing: не вдалося отримати список Pod-ів: %v", err)
		return
	}

	ctx := context.Background()
	for _, pod := range pods {
		if pod.NodeID == a.nodeID && pod.IsStateful {
			log.Printf("!!! FENCING ACTIVATED !!! Примусова зупинка Stateful-контейнера %s для захисту даних (Split-Brain)!", pod.ID)
			_ = a.cm.StopContainer(ctx, pod.ID)
			// В реальній системі тут також відбудеться відмонтування мережевих дисків/томів
		}
	}
}
