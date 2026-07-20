package cluster

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"
)

type StreamLayer struct {
	net.Listener
}

func (s *StreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(address), timeout)
}

// SetupRaftWithListener відповідає за Пункт 2 (Консенсус та Реплікація)
// Ініціалізує Raft-вузол і його сховища, використовуючи наданий Listener (cmux).
func SetupRaftWithListener(localID string, advertiseAddr string, listener net.Listener, dataDir string, fsm raft.FSM, isBootstrap bool) (*raft.Raft, error) {
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(localID)

	// 8.3: Захист SSD/SD-карт (Edge-оптимізація)
	// Зменшуємо частоту дискового I/O, дозволяючи батчинг та агресивніше робимо Snapshotting
	config.SnapshotInterval = 30 * time.Minute
	config.SnapshotThreshold = 1024
	config.TrailingLogs = 1024

	// 2.1.3: Мережевий транспорт через Мультиплексор
	// Замість прямого TCP ми використовуємо Listener, який нам дав cmux
	transport := raft.NewNetworkTransportWithLogger(
		&StreamLayer{Listener: listener},
		3,
		10*time.Second,
		config.Logger,
	)

	// Створюємо директорію для даних
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}

	// 2.1.1: Сховища на базі bbolt
	// LogStore (журнал транзакцій) та StableStore (метадані)
	boltDBStore, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("помилка створення boltdb: %s", err)
	}

	// SnapshotStore (зрізки)
	snapshotStore, err := raft.NewFileSnapshotStore(dataDir, 1, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("помилка створення snapshot store: %s", err)
	}

	// Запускаємо Raft
	r, err := raft.NewRaft(config, fsm, boltDBStore, boltDBStore, snapshotStore, transport)
	if err != nil {
		return nil, fmt.Errorf("помилка створення raft вузла: %s", err)
	}

	// 9.1: Справжній P2P Bootstrap
	// Bootstrap виконується лише для команди 'init'. Команда 'start' просто приєднується.
	hasState, err := raft.HasExistingState(boltDBStore, boltDBStore, snapshotStore)
	if err != nil {
		return nil, err
	}

	if !hasState && isBootstrap {
		log.Println("Raft: Bootstrap нового кластера (команда init)...")
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      config.LocalID,
					Address: transport.LocalAddr(),
				},
			},
		}
		r.BootstrapCluster(configuration)
	} else if !hasState {
		log.Println("Raft: Вузол запущено (команда start). Очікуємо Auto-Discovery через mDNS для приєднання до лідера.")
	} else {
		log.Println("Raft: Завантажено існуючий стан")
	}

	return r, nil
}
