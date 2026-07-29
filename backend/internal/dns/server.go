package dns

import (
	"log"
	"math/rand"
	"strings"

	"github.com/kovach/p2ser/internal/config"
	"github.com/kovach/p2ser/internal/scheduler"
	"github.com/miekg/dns"
)

type Server struct {
	scheduler *scheduler.Scheduler
}

func NewServer(s *scheduler.Scheduler) *Server {
	return &Server{scheduler: s}
}

func (s *Server) handleRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Compress = false

	if r.Opcode == dns.OpcodeQuery && len(m.Question) > 0 {
		q := m.Question[0]
		name := strings.TrimSuffix(q.Name, ".")

		var internalIP string

		if q.Qtype == dns.TypeA || q.Qtype == dns.TypeAAAA {
			// Перевіряємо, чи це внутрішній сервіс
			pods, err := s.scheduler.FetchPods()
			if err == nil {
				var candidates []string
				for _, pod := range pods {
					// H-14: Фільтруємо лише запущені та готові контейнери (readiness probe passed)
					if pod.Status != "Running" || !pod.Ready {
						continue
					}
					
					// Nginx та інші сервіси можуть шукати "backend" або повне ім'я "backend-active-0"
					if (pod.App == name || pod.ID == name) && pod.PodIP != "" {
						candidates = append(candidates, strings.Split(pod.PodIP, "/")[0])
					}
				}
				if len(candidates) > 0 {
					// H-14: Load-balance across multiple replicas (random choice)
					internalIP = candidates[rand.Intn(len(candidates))]
				}
			}
		}

		if internalIP != "" {
			if q.Qtype == dns.TypeA {
				rr, err := dns.NewRR(q.Name + " A " + internalIP)
				if err == nil {
					m.Answer = append(m.Answer, rr)
				}
			}
			w.WriteMsg(m)
			return
		}

		// Якщо не знайшли внутрішній IP, форвардимо запит
		upstreamDNS := "1.1.1.1"
		if config.GlobalConfig != nil && config.GlobalConfig.UpstreamDNS != "" {
			upstreamDNS = config.GlobalConfig.UpstreamDNS
		}
		c := new(dns.Client)
		in, _, err := c.Exchange(r, upstreamDNS+":53")
		if err == nil && in != nil {
			w.WriteMsg(in)
			return
		}
	}

	w.WriteMsg(m)
}

func (s *Server) Start() {
	dns.HandleFunc(".", s.handleRequest)

	server := &dns.Server{Addr: "10.88.0.1:53", Net: "udp"}
	log.Printf("DNS: Запуск внутрішнього DNS-сервера на 10.88.0.1:53")

	if err := server.ListenAndServe(); err != nil {
		log.Printf("DNS: Помилка запуску сервера: %v", err)
	}
}
