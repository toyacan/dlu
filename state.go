package bot

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cu/internal/tg"
	"cu/internal/util"
)

// ---------- Daftar pengguna yang pernah chat ke bot (untuk /bcpm) ----------

type pmEntry struct {
	Name string `json:"name"`
	At   int64  `json:"at"`
}

type pmStore struct {
	mu    sync.Mutex
	path  string
	users map[string]pmEntry
	timer *time.Timer
}

func newPmStore(dataDir string) *pmStore {
	s := &pmStore{path: filepath.Join(dataDir, "bot_pm.json"), users: map[string]pmEntry{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.users)
		if s.users == nil {
			s.users = map[string]pmEntry{}
		}
	}
	return s
}

func (s *pmStore) scheduleSaveLocked() {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(500*time.Millisecond, s.flush)
}

func (s *pmStore) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(s.users)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		log.Printf("Gagal simpan daftar PM: %v", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("Gagal simpan daftar PM: %v", err)
		return
	}
	_ = os.Rename(tmp, s.path)
}

func (s *pmStore) add(id, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[id]; ok {
		return false
	}
	s.users[id] = pmEntry{Name: name, At: time.Now().UnixMilli()}
	s.scheduleSaveLocked()
	return true
}

func (s *pmStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, id)
	s.scheduleSaveLocked()
}

func (s *pmStore) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.users))
	for id := range s.users {
		out = append(out, id)
	}
	return out
}

func (s *pmStore) track(chat tg.Chat, from *tg.User) {
	if chat.Type != "private" || from == nil || from.IsBot {
		return
	}
	s.add(util.Itoa(chat.ID), from.FullName())
}

// ---------- Proses login yang sedang berjalan ----------

// LoginFlow dipenuhi oleh userbot gotd (tahap 3).
type LoginFlow interface {
	SendCode(ctx context.Context, phone string) error
	SubmitCode(ctx context.Context, code string) (needPassword bool, session string, err error)
	SubmitPassword(ctx context.Context, password string) (session string, err error)
	Cancel()
}

type loginState struct {
	mu        sync.Mutex // satu pesan login diproses satu per satu
	flow      LoginFlow
	state     string // phone | code | password
	startedAt time.Time
	tries     int
}
