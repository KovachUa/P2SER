package network

import (
	"fmt"
	"log"

	"github.com/hashicorp/memberlist"
	"github.com/hashicorp/raft"
	"github.com/vmihailenco/msgpack/v5"
)

type NodeMetadata struct {
	Arch     string `msgpack:"arch"`
	FreeRAM  int    `msgpack:"free_ram_mb"`
	FreeCPU  int    `msgpack:"free_cpu_cores"`
	Subnet   string `msgpack:"subnet"`    // 4.4: Динамічна таблиця маршрутизації
	RaftPort int    `msgpack:"raft_port"` // 9.1: Порт для автоматичного додавання Voter-а
}

// NodeMetrics (для пункту 1.3.1, 1.3.2 та 1.3.3)
type NodeMetrics struct {
	Node     string  `msgpack:"node"`
	Version  int64   `msgpack:"version"` // 1.3.3: Версіювання (Vector Clock / resourceVersion)
	CPUUsage float64 `msgpack:"cpu_usage"`
	RAMFree  int     `msgpack:"ram_free_mb"`
}

type MyDelegate struct {
	Meta         NodeMetadata
	Broadcasts   *memberlist.TransmitLimitedQueue
	nodeVersions map[string]int64 // 1.3.3: Зберігання останньої відомої версії для кожного вузла
}

func (d *MyDelegate) NodeMeta(limit int) []byte {
	// 1.3.2: Використовуємо MessagePack замість JSON для компактності
	b, _ := msgpack.Marshal(d.Meta)
	return b
}

// NotifyMsg викликається, коли хтось надсилає Broadcast (Пункт 1.3.1)
func (d *MyDelegate) NotifyMsg(b []byte) {
	var metrics NodeMetrics
	err := msgpack.Unmarshal(b, &metrics)
	if err == nil {
		// 1.3.3: Перевіряємо актуальність пакета
		if d.nodeVersions == nil {
			d.nodeVersions = make(map[string]int64)
		}

		lastVersion := d.nodeVersions[metrics.Node]
		if metrics.Version <= lastVersion {
			// Пакет застарілий або дублюється, відкидаємо
			return
		}

		// Оновлюємо версію та обробляємо дані
		d.nodeVersions[metrics.Node] = metrics.Version
		log.Printf("Gossip: Отримано метрики від [%s] (v%d) -> CPU: %.1f%%, RAM: %d MB", metrics.Node, metrics.Version, metrics.CPUUsage, metrics.RAMFree)
	}
}

// GetBroadcasts віддає повідомлення з черги для їхньої розсилки
func (d *MyDelegate) GetBroadcasts(overhead, limit int) [][]byte {
	return d.Broadcasts.GetBroadcasts(overhead, limit)
}

func (d *MyDelegate) LocalState(join bool) []byte            { return []byte{} }
func (d *MyDelegate) MergeRemoteState(buf []byte, join bool) {}

// MetricsBroadcast реалізує memberlist.Broadcast для розсилки метрик (Пункт 1.3.1)
type MetricsBroadcast struct {
	Msg []byte
}

func (b *MetricsBroadcast) Invalidates(other memberlist.Broadcast) bool { return false }
func (b *MetricsBroadcast) Message() []byte                             { return b.Msg }
func (b *MetricsBroadcast) Finished()                                   {}

// MyEventDelegate реалізує memberlist.EventDelegate для відстеження стану вузлів (Пункт 1.4.3 та 4.4)
type MyEventDelegate struct {
	NetManager *NetworkManager
	RaftNode   *raft.Raft
}

func (e *MyEventDelegate) NotifyJoin(node *memberlist.Node) {
	log.Printf("Подія: Вузол [%s] приєднався до кластера", node.Name)
	e.updateRoute(node)

	// 9.1: Автоматичне формування кластера (Auto-Discovery)
	// Якщо ми є Лідером, ми автоматично додаємо новий вузол до Raft-кластера
	if e.RaftNode != nil && e.RaftNode.State() == raft.Leader {
		var meta NodeMetadata
		if err := msgpack.Unmarshal(node.Meta, &meta); err == nil && meta.RaftPort != 0 {
			raftAddr := fmt.Sprintf("%s:%d", node.Addr.String(), meta.RaftPort)
			log.Printf("Auto-Discovery: Додаємо %s (%s) як Raft Voter-а", node.Name, raftAddr)

			// Додаємо його як голосуючого члена
			future := e.RaftNode.AddVoter(raft.ServerID(node.Name), raft.ServerAddress(raftAddr), 0, 0)
			if err := future.Error(); err != nil {
				log.Printf("Помилка додавання Voter-а: %v", err)
			}
		}
	}
}

func (e *MyEventDelegate) NotifyUpdate(node *memberlist.Node) {
	// Викликається при оновленні метаданих вузла
	e.updateRoute(node)
}

func (e *MyEventDelegate) updateRoute(node *memberlist.Node) {
	if e.NetManager == nil || node.Meta == nil {
		return
	}
	var meta NodeMetadata
	if err := msgpack.Unmarshal(node.Meta, &meta); err == nil && meta.Subnet != "" {
		// 4.4: Динамічне оновлення таблиці маршрутизації через Gossip
		e.NetManager.UpdateRouteTable(node.Name, node.Addr.String(), meta.Subnet)
	}
}

func (e *MyEventDelegate) NotifyLeave(node *memberlist.Node) {
	// 1.4.3: Graceful Shutdown vs Відмова
	if node.State == memberlist.StateLeft {
		log.Printf("Подія: Вузол [%s] планово покинув кластер (Graceful Shutdown). Можна безпечно перерозподілити Pod-и.", node.Name)
	} else if node.State == memberlist.StateDead {
		log.Printf("Подія: Вузол [%s] впав або відключився (Dead). Потрібне екстрене відновлення!", node.Name)
	} else {
		log.Printf("Подія: Вузол [%s] покинув кластер зі станом: %d", node.Name, node.State)
	}
}
