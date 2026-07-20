package cluster

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

// FSM реалізує кінцевий автомат для Raft (Пункт 2.1.2)
// Всі зміни стану кластера проходять через цей FSM строго послідовно.
type FSM struct {
	db *bolt.DB
	secretKey []byte // 11.2: Ключ для Data-at-Rest Encryption
}

var bucketName = []byte("p2ser_state")

func NewFSM(dbPath string, token string) (*FSM, error) {
	db, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		return nil, err
	}

	// Ініціалізуємо бакет
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketName)
		return err
	})
	if err != nil {
		return nil, err
	}

	hash := sha256.Sum256([]byte(token))
	return &FSM{db: db, secretKey: hash[:]}, nil
}

// Apply викликається Raft-ом для застосування зафіксованої команди (Commit) до нашого локального стану.
func (f *FSM) Apply(l *raft.Log) interface{} {
	var c struct {
		Op              string `json:"op"`
		Key             string `json:"key"`
		Value           string `json:"value"`
		ExpectedVersion int    `json:"expected_version"` // 3.1.1: Для CAS
	}

	if err := json.Unmarshal(l.Data, &c); err != nil {
		log.Printf("FSM Apply: помилка парсингу: %v", err)
		return err
	}

	// 2.1.4: Транзакційність Bbolt (Write Tx)
	// Запис відбувається через одну активну Write Tx (сувора послідовність)
	err := f.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)

		if c.Op == "set" {
			encVal, err := f.encrypt([]byte(c.Value))
			if err != nil { return err }
			return b.Put([]byte(c.Key), encVal)
		} else if c.Op == "del" {
			// 6.4: Операція видалення для Zero-Downtime Deployment
			return b.Delete([]byte(c.Key))
		} else if c.Op == "cas" {
			// 3.1.1: Оптимістичне блокування (CAS)
			// Читаємо поточний стан (якщо є)
			existingEnc := b.Get([]byte(c.Key))
			existing, _ := f.decrypt(existingEnc)
			var currentVersion int = 0

			if existing != nil {
				// Спрощено: ми припускаємо, що Value зберігає JSON з полем resource_version
				// Для справжнього коду краще парсити це безпечно
				var parsed map[string]interface{}
				if err := json.Unmarshal(existing, &parsed); err == nil {
					if rv, ok := parsed["resource_version"].(float64); ok {
						currentVersion = int(rv)
					}
				}
			}

			if currentVersion != c.ExpectedVersion {
				return fmt.Errorf("CAS failed: expected version %d, got %d", c.ExpectedVersion, currentVersion)
			}

			encVal, err := f.encrypt([]byte(c.Value))
			if err != nil { return err }
			return b.Put([]byte(c.Key), encVal)
		}
		return nil
	})

	if err != nil {
		log.Printf("FSM Apply Update Error: %v", err)
		return err
	}

	// 2.2.1: Мікро-об'єм даних дозволяє нам надійно дублювати цей запис на всі вузли
	logValue := c.Value
	if strings.HasPrefix(c.Key, "pod:") {
		var podData map[string]interface{}
		if err := json.Unmarshal([]byte(c.Value), &podData); err == nil {
			if envSlice, ok := podData["env"].([]interface{}); ok {
				for i, e := range envSlice {
					if envStr, ok := e.(string); ok {
						envLower := strings.ToLower(envStr)
						if strings.Contains(envLower, "pass") || strings.Contains(envLower, "secret") || strings.Contains(envLower, "token") || strings.Contains(envLower, "url") {
							parts := strings.SplitN(envStr, "=", 2)
							if len(parts) == 2 {
								envSlice[i] = parts[0] + "=***HIDDEN***"
							}
						}
					}
				}
				podData["env"] = envSlice
				if maskedBytes, err := json.Marshal(podData); err == nil {
					logValue = string(maskedBytes)
				}
			}
		}
	}
	log.Printf("FSM Apply: [%s] = %s", c.Key, logValue)
	return nil
}

// GetState повертає значення за ключем, використовуючи Read-Only транзакцію (Пункт 2.1.4)
func (f *FSM) GetState(key string) (string, error) {
	var val []byte
	err := f.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		encVal := b.Get([]byte(key))
		val, _ = f.decrypt(encVal)
		return nil
	})
	if err != nil {
		return "", err
	}
	if val == nil {
		return "", nil
	}
	return string(val), nil
}

// GetAllPods повертає всі значення з префіксом "pod:"
func (f *FSM) GetAllPods() ([]string, error) {
	var pods []string
	err := f.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		c := b.Cursor()
		prefix := []byte("pod:")
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			decV, err := f.decrypt(v)
			if err == nil { pods = append(pods, string(decV)) }
		}
		return nil
	})
	return pods, err
}

// Snapshot створює зрізок стану бази даних (Пункт 2.2.3).

// GetAllBots повертає список динамічно доданих ботів (з префіксом "bot:")
func (f *FSM) GetAllBots() ([]string, error) {
	var bots []string
	err := f.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		c := b.Cursor()
		prefix := []byte("bot:")
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			decV, err := f.decrypt(v)
			if err == nil && string(decV) == "true" {
				botName := bytes.TrimPrefix(k, prefix)
				bots = append(bots, string(botName))
			}
		}
		return nil
	})
	return bots, err
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	// Клонуємо стан бази для зрізка через швидку Read-Only транзакцію
	clone := make(map[string]string)
	err := f.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		return b.ForEach(func(k, v []byte) error {
			decV, err := f.decrypt(v)
			if err == nil { clone[string(k)] = string(decV) }
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	return &fsmSnapshot{state: clone}, nil
}

// Restore завантажує стан із зрізка, отриманого від лідера (Пункт 2.2.4).
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var newState map[string]string
	if err := json.NewDecoder(rc).Decode(&newState); err != nil {
		return err
	}

	// Оновлюємо bbolt зі зрізка
	return f.db.Update(func(tx *bolt.Tx) error {
		// Очищаємо старий бакет
		tx.DeleteBucket(bucketName)
		b, err := tx.CreateBucket(bucketName)
		if err != nil {
			return err
		}
		for k, v := range newState {
			encV, err := f.encrypt([]byte(v))
			if err != nil { return err }
			if err := b.Put([]byte(k), encV); err != nil {
				return err
			}
		}
		return nil
	})
}

// fsmSnapshot реалізує збереження зрізка на диск.
type fsmSnapshot struct {
	state map[string]string
}

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	err := func() error {
		b, err := json.Marshal(s.state)
		if err != nil {
			return err
		}
		if _, err := sink.Write(b); err != nil {
			return err
		}
		return sink.Close()
	}()

	if err != nil {
		sink.Cancel()
	}
	return err
}

func (s *fsmSnapshot) Release() {}


// 11.2: Шифрування даних перед записом у bbolt
func (f *FSM) encrypt(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(f.secretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, nil), nil
}

// 11.2: Розшифрування даних після читання з bbolt
func (f *FSM) decrypt(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(f.secretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
