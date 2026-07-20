package network

import (
	"fmt"
	"log"
	"os"

	"github.com/hashicorp/mdns"
	"github.com/hashicorp/memberlist"
)

// SetupMDNS відповідає за Пункт 1.2.1 (Локальний Bootstrap через mDNS).
// Ця функція одночасно ТРАНСЛЮЄ нашу присутність і ШУКАЄ сусідів.
func SetupMDNS(list *memberlist.Memberlist, token string, port int) error {
	// 1. Формуємо унікальне ім'я сервісу на базі секретного токена.
	// Таким чином вузли з іншого кластера (з іншим токеном) нас не побачать.
	serviceName := fmt.Sprintf("_p2ser_%s._tcp", token)

	host, _ := os.Hostname()
	info := []string{"P2SER Node"}

	// 2. Запускаємо mDNS сервер (трансляція в мережу)
	service, err := mdns.NewMDNSService(host, serviceName, "", "", port, nil, info)
	if err != nil {
		return err
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return err
	}
	_ = server // Сервер працюватиме у фоні, поки процес живий

	// 3. Запускаємо клієнт для пошуку інших вузлів
	entriesCh := make(chan *mdns.ServiceEntry, 10)
	go func() {
		for entry := range entriesCh {
			// Якщо знайшли когось — витягуємо IP та Порт
			peerAddr := fmt.Sprintf("%s:%d", entry.AddrV4, entry.Port)

			// Ми не хочемо приєднуватися самі до себе, але memberlist обробить це коректно
			log.Printf("mDNS: Знайдено сусідній вузол %s. Спроба приєднання...", peerAddr)

			// 4. Безпосереднє об'єднання Gossip-кілець
			_, err := list.Join([]string{peerAddr})
			if err != nil {
				log.Printf("mDNS: Не вдалося приєднатися до %s: %v", peerAddr, err)
			} else {
				log.Printf("mDNS: Успішно приєднано до кластера через %s!", peerAddr)
			}
		}
	}()

	log.Printf("mDNS: Транслюємо себе та шукаємо сусідів з токеном '%s'...", token)

	// Виконуємо запит у мережу на пошук сервісів
	err = mdns.Lookup(serviceName, entriesCh)
	if err != nil {
		return err
	}
	close(entriesCh)

	return nil
}
