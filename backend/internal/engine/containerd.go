package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/kovach/p2ser/internal/scheduler"
)

// namedVolumeDir — базова директорія для named volumes (аналог /var/lib/docker/volumes)
const namedVolumeDir = "/var/lib/p2ser/volumes"

// resolveVolumeSrc резолвить named volume у абсолютний шлях на хості.
// C-13: Забороняє вихід за межі дозволених директорій (Path Traversal / Host Compromise).
func resolveVolumeSrc(src string, pod *scheduler.Pod) (string, error) {
	if filepath.IsAbs(src) {
		// Забороняємо довільні абсолютні шляхи на хості (напр. /:/host)
		// Дозволяємо лише якщо це вже всередині namedVolumeDir
		cleanSrc := filepath.Clean(src)
		if !strings.HasPrefix(cleanSrc, namedVolumeDir) {
			// Якщо це тимчасова директорія, дозволяємо лише специфічну для цього проекту
			if strings.HasPrefix(cleanSrc, os.TempDir()) {
				allowedTmp := filepath.Join(os.TempDir(), "p2ser-project-"+pod.Project)
				if cleanSrc == allowedTmp || strings.HasPrefix(cleanSrc, allowedTmp+string(filepath.Separator)) {
					return cleanSrc, nil
				}
				return "", fmt.Errorf("безпека: тимчасовий шлях %s заборонено (дозволено лише %s)", src, allowedTmp)
			}
			return "", fmt.Errorf("безпека: абсолютний шлях %s заборонено", src)
		}
		return cleanSrc, nil
	}
	if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
		// Відносні шляхи вже мають бути відрезолвлені парсером відносно workingDir.
		// Якщо вони дійшли сюди сирими, значить workingDir не був заданий.
		// Відхиляємо ../ щоб уникнути виходу за межі.
		cleanSrc := filepath.Clean(src)
		if strings.HasPrefix(cleanSrc, "..") {
			return "", fmt.Errorf("безпека: відносний шлях з виходом за межі (%s) заборонено", src)
		}
		abs, err := filepath.Abs(src)
		if err == nil {
			return abs, nil
		}
		return src, nil
	}
	// Named volume — резолвимо в /var/lib/p2ser/volumes/<name>
	resolved := filepath.Join(namedVolumeDir, src)
	// Перевірка на path traversal у самій назві volume (напр. "../../etc")
	if !strings.HasPrefix(filepath.Clean(resolved), namedVolumeDir) {
		return "", fmt.Errorf("безпека: некоректна назва volume %s", src)
	}
	log.Printf("Volume: named volume '%s' → '%s'", src, resolved)
	return resolved, nil
}

// ContainerManager відповідає за пряму взаємодію з containerd (Пункт 5.1)
type ContainerManager struct {
	client *containerd.Client
}

// NewContainerManager створює новий клієнт до UNIX-сокета containerd
func NewContainerManager(socketPath string) (*ContainerManager, error) {
	if socketPath == "" {
		socketPath = "/run/containerd/containerd.sock" // Стандартний шлях
	}

	client, err := containerd.New(socketPath)
	if err != nil {
		return nil, fmt.Errorf("помилка підключення до containerd: %v", err)
	}

	return &ContainerManager{
		client: client,
	}, nil
}

// Close закриває з'єднання з containerd
func (m *ContainerManager) Close() {
	if m.client != nil {
		m.client.Close()
	}
}

// RunContainer створює та запускає контейнер у containerd
func (cm *ContainerManager) RunContainer(ctx context.Context, pod scheduler.Pod) (string, error) {
	// Встановлюємо namespace для нашого оркестратора
	ctx = namespaces.WithNamespace(ctx, "p2ser")

	imageName := pod.Image
	if imageName == "" {
		imageName = "docker.io/library/nginx:alpine"
	}

	// Нормалізація імені образу для containerd (додаємо docker.io/library/ якщо потрібно)
	if !strings.Contains(imageName, "/") {
		imageName = "docker.io/library/" + imageName
	} else {
		parts := strings.Split(imageName, "/")
		if !strings.Contains(parts[0], ".") && parts[0] != "localhost" {
			imageName = "docker.io/" + imageName
		}
	}
	log.Printf("ContainerManager: Завантаження образу %s для %s...", imageName, pod.ID)

	// Перевіряємо, чи образ вже є локально
	image, err := cm.client.GetImage(ctx, imageName)
	if err != nil {
		log.Printf("ContainerManager: Локальний образ %s не знайдено, завантажуємо (Pull)...", imageName)
		image, err = cm.client.Pull(ctx, imageName, containerd.WithPullUnpack)
		if err != nil {
			return "", fmt.Errorf("помилка завантаження образу: %v", err)
		}
	} else {
		log.Printf("ContainerManager: Знайдено локальний образ %s", imageName)
	}

	log.Printf("ContainerManager: Створення OCI-специфікації для %s...", pod.ID)

	// Створюємо власний resolv.conf для DNS сервера
	resolvConfPath := "/tmp/p2ser_resolv.conf"
	os.WriteFile(resolvConfPath, []byte("nameserver 10.88.0.1\n"), 0644)

	// Формуємо масив монтувань
	mounts := []specs.Mount{
		{
			Destination: "/etc/resolv.conf",
			Type:        "bind",
			Source:      resolvConfPath,
			Options:     []string{"rbind", "ro"},
		},
	}

	for _, v := range pod.Volumes {
		parts := strings.Split(v, ":")
		if len(parts) >= 2 {
			// Резолвимо named volumes у абсолютні шляхи (з перевіркою безпеки C-13)
			src, err := resolveVolumeSrc(parts[0], &pod)
			if err != nil {
				log.Printf("Volume: пропущено %s через порушення політики безпеки: %v", parts[0], err)
				continue
			}
			dst := parts[1]
			opts := []string{"rbind"}
			if len(parts) >= 3 && parts[2] == "ro" {
				opts = append(opts, "ro")
			}

			// Автоматично створюємо директорію на хості якщо не існує
			if err := os.MkdirAll(src, 0755); err != nil {
				log.Printf("Volume: попередження — не вдалося створити '%s': %v", src, err)
			} else {
				log.Printf("Volume: bind mount %s → %s", src, dst)
			}

			mounts = append(mounts, specs.Mount{
				Destination: dst,
				Type:        "bind",
				Source:      src,
				Options:     opts,
			})
		}
	}

	// Налаштовуємо базові опції специфікації (тут також забороняємо нові привілеї для безпеки)
	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithNoNewPrivileges, // 11.5: Заборона ескалації привілеїв
		// Власна мережа через CNI
		oci.WithLinuxNamespace(specs.LinuxNamespace{Type: specs.NetworkNamespace}),
		oci.WithMounts(mounts),
		oci.WithHostHostsFile,
		oci.WithEnv(pod.Env),
	}

	// Якщо в docker-compose було задано користувача (наприклад, "1000:1000")
	if pod.RunAsUser != "" {
		specOpts = append(specOpts, oci.WithUser(pod.RunAsUser))
		log.Printf("ContainerManager: Застосовано безпековий контекст: RunAsUser=%s", pod.RunAsUser)
	}

	// 11.5: Ізоляція контейнерів (Rootless).
	if pod.UsernsRemap {
		specOpts = append(specOpts, oci.WithUserNamespace(
			[]specs.LinuxIDMapping{{ContainerID: 0, HostID: 100000, Size: 65536}},
			[]specs.LinuxIDMapping{{ContainerID: 0, HostID: 100000, Size: 65536}},
		))
		log.Printf("Security: Ввімкнено User Namespaces (Rootless) та NoNewPrivileges для контейнера %s", pod.ID)
	} else {
		log.Printf("Security: User Namespaces вимкнено для контейнера %s (UsernsRemap=false)", pod.ID)
	}
	// Примусове видалення старого контейнера, якщо він залишився
	if oldContainer, err := cm.client.LoadContainer(ctx, pod.ID); err == nil {
		oldTask, errTask := oldContainer.Task(ctx, nil)
		if errTask == nil {
			oldTask.Kill(ctx, 9)
			oldTask.Delete(ctx)
		}
		oldContainer.Delete(ctx, containerd.WithSnapshotCleanup)
	}

	// Примусове видалення старого снепшоту, якщо він "завис"
	snapshotService := cm.client.SnapshotService("overlayfs")
	if snapshotService != nil {
		_ = snapshotService.Remove(ctx, pod.ID+"-snapshot")
	}

	// Створення контейнера з OCI-специфікацією
	container, err := cm.client.NewContainer(
		ctx,
		pod.ID,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(pod.ID+"-snapshot", image),
		containerd.WithNewSpec(specOpts...),
	)
	if err != nil {
		return "", fmt.Errorf("помилка створення контейнера: %v", err)
	}

	log.Printf("ContainerManager: Створення задачі (Task) для %s...", pod.ID)

	// Створення задачі (Task), яка безпосередньо взаємодіє з cgroups та namespaces
	logPath := "/tmp/p2ser_" + pod.ID + ".log"
	task, err := container.NewTask(ctx, cio.LogFile(logPath))
	if err != nil {
		// Очищення при помилці
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return "", fmt.Errorf("помилка створення задачі: %v", err)
	}

	netns := fmt.Sprintf("/proc/%d/ns/net", task.Pid())
	podIP, err := SetupCNINetwork(pod.ID, netns)
	if err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return "", fmt.Errorf("помилка налаштування CNI: %v", err)
	}
	log.Printf("CNI: Контейнер %s отримав IP %s", pod.ID, podIP)

	// Налаштування Port Forwarding через iptables
	cleanIP := strings.Split(podIP, "/")[0]
	
	// Дозволяємо маршрутизацію localhost (127.0.0.0/8) через міст (необхідно для DNAT з localhost)
	exec.Command("sysctl", "-w", "net.ipv4.conf.p2ser0.route_localnet=1").Run()
	
	for _, portMap := range pod.Ports {
		parts := strings.Split(portMap, ":")
		if len(parts) == 2 {
			hostPort := parts[0]
			containerPort := parts[1]

			// H-7: Validate port numbers to prevent iptables argument injection
			hpInt, err1 := strconv.Atoi(hostPort)
			cpInt, err2 := strconv.Atoi(containerPort)
			if err1 != nil || err2 != nil || hpInt < 1 || hpInt > 65535 || cpInt < 1 || cpInt > 65535 {
				log.Printf("PortMap: попередження — некоректний порт: %s", portMap)
				continue
			}

			// DNAT з хоста в контейнер (для зовнішнього трафіку)
			exec.Command("iptables", "-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", cleanIP+":"+containerPort).Run()
			// DNAT для локального трафіку (localhost)
			exec.Command("iptables", "-t", "nat", "-A", "OUTPUT", "-p", "tcp", "-d", "127.0.0.1", "--dport", hostPort, "-j", "DNAT", "--to-destination", cleanIP+":"+containerPort).Run()
			// L-2: removed hardcoded 192.168.1.18 — use localIP from NetworkManager instead
			// MASQUERADE для виправлення зворотного маршруту (щоб контейнер не відповідав на власний 127.0.0.1)
			exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-p", "tcp", "-d", cleanIP, "--dport", containerPort, "-j", "MASQUERADE").Run()
			// Дозволяємо маршрутизацію
			exec.Command("iptables", "-A", "FORWARD", "-p", "tcp", "-d", cleanIP, "--dport", containerPort, "-j", "ACCEPT").Run()
			log.Printf("PortMap: прокинуто порт %s -> %s:%s (PREROUTING + OUTPUT)", hostPort, cleanIP, containerPort)
		}
	}

	// Запуск задачі
	err = task.Start(ctx)
	if err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return "", fmt.Errorf("помилка запуску задачі: %v", err)
	}

	log.Printf("ContainerManager: Контейнер %s успішно запущений!", pod.ID)
	return podIP, nil
}

// StopContainer зупиняє і видаляє контейнер
func (m *ContainerManager) StopContainer(ctx context.Context, podID string) error {
	ctx = namespaces.WithNamespace(ctx, "p2ser")

	container, err := m.client.LoadContainer(ctx, podID)
	if err != nil {
		return fmt.Errorf("помилка завантаження контейнера %s: %v", podID, err)
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		// Контейнер може не мати запущеної задачі
		log.Printf("ContainerManager: задача для %s не знайдена, видаляємо контейнер...", podID)
		TeardownCNINetwork(podID, "")
		return container.Delete(ctx, containerd.WithSnapshotCleanup)
	}

	netns := fmt.Sprintf("/proc/%d/ns/net", task.Pid())
	TeardownCNINetwork(podID, netns)

	log.Printf("ContainerManager: Зупинка задачі для %s...", podID)

	// Зупинка задачі
	exitStatusC, err := task.Wait(ctx)
	if err != nil {
		log.Printf("Помилка очікування задачі: %v", err)
	}

	if err := task.Kill(ctx, 9); err != nil { // SIGKILL
		return fmt.Errorf("помилка вбивства задачі: %v", err)
	}

	<-exitStatusC

	// Видалення задачі
	_, err = task.Delete(ctx)
	if err != nil {
		return fmt.Errorf("помилка видалення задачі: %v", err)
	}

	log.Printf("ContainerManager: Видалення контейнера %s...", podID)

	// Видалення контейнера та його снапшоту
	err = container.Delete(ctx, containerd.WithSnapshotCleanup)
	if err != nil {
		return fmt.Errorf("помилка видалення контейнера: %v", err)
	}

	return nil
}

// IsContainerRunning перевіряє, чи дійсно працює контейнер у containerd
func (m *ContainerManager) IsContainerRunning(ctx context.Context, podID string) (bool, error) {
	ctx = namespaces.WithNamespace(ctx, "p2ser")

	container, err := m.client.LoadContainer(ctx, podID)
	if err != nil {
		// Якщо контейнер не знайдено, значить він не працює
		return false, nil
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		// Якщо немає задачі (task), контейнер зупинено
		return false, nil
	}

	status, err := task.Status(ctx)
	if err != nil {
		return false, err
	}

	return status.Status == containerd.Running, nil
}
