package engine

import (
	"log"
	"time"

	"github.com/hashicorp/raft"
)

// SetupFencing запускає фоновий процес для відстеження втрати кворуму (Пункт 2.4.3)
// Якщо вузол втрачає зв'язок з лідером на тривалий час (стає ізольованим),
// він має застосувати "Самоізоляцію" (Fencing), наприклад, зупинити Stateful контейнери.
func SetupFencing(raftNode *raft.Raft, onFence func()) {
	// Raft має канал для спостережень за лідером
	observationCh := make(chan raft.Observation, 10)
	observer := raft.NewObserver(observationCh, false, func(o *raft.Observation) bool {
		_, ok := o.Data.(raft.LeaderObservation)
		return ok
	})
	raftNode.RegisterObserver(observer)

	go func() {
		defer raftNode.DeregisterObserver(observer)

		// Таймер для Fencing. Якщо лідера немає занадто довго, ізолюємось.
		var fencingTimer *time.Timer

		for {
			select {
			case obs := <-observationCh:
				leaderObs := obs.Data.(raft.LeaderObservation)
				if leaderObs.Leader == "" {
					// Лідер втрачений (можливо вибори, а можливо Split-Brain і ми в меншості)
					log.Println("Fencing: Лідер втрачений. Очікуємо відновлення кворуму...")

					if fencingTimer == nil {
						// Даємо 5 секунд на обрання нового лідера. Якщо ні — ми в меншості.
						fencingTimer = time.AfterFunc(5*time.Second, func() {
							log.Println("!!! FENCING TIMER EXPIRED !!!")
							if onFence != nil {
								onFence()
							}
						})
					}
				} else {
					// З'явився лідер
					log.Printf("Fencing: Кворум в нормі. Поточний лідер: %s", leaderObs.Leader)
					if fencingTimer != nil {
						fencingTimer.Stop()
						fencingTimer = nil
						log.Println("Fencing: Скасовано (Зв'язок відновлено).")
					}
				}
			}
		}
	}()
}
