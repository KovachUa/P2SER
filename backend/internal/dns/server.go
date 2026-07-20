package dns

import (
	"log"
	"strings"

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
				for _, pod := range pods {
					// Nginx та інші сервіси можуть шукати "backend"
					if pod.App == name && pod.PodIP != "" {
						internalIP = strings.Split(pod.PodIP, "/")[0]
						break
					}
					// Або повне ім'я "backend-active-0"
					if pod.ID == name && pod.PodIP != "" {
						internalIP = strings.Split(pod.PodIP, "/")[0]
						break
					}
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

		// Якщо не знайшли внутрішній IP, форвардимо запит до 8.8.8.8
		c := new(dns.Client)
		in, _, err := c.Exchange(r, "8.8.8.8:53")
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
