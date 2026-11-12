// Package rolls: permintaan roll yang menunggu persetujuan admin (dipakai bersama oleh bot dan userbot).
// Userbot membuat permintaan -> mengirimnya lewat mode inline bot ("via @bot", tombol Konfirm / Tolak)
// -> bot menerima klik tombol dan memanggil Resolve milik userbot.
package rolls

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type Mention struct {
	UserID string
	Offset int
	Length int
}

type Result struct {
	Text  string // bila diisi, pesan inline diganti teks ini
	Alert string // bila diisi, ditampilkan sebagai notifikasi / popup
}

type Entry struct {
	UserID    string
	OwnerIDs  []string
	Text      string
	Mention   *Mention
	Resolve   func(action, fromID string) Result
	CreatedAt time.Time
	done      bool
}

var (
	mu    sync.Mutex
	items = map[string]*Entry{}

	botUsername string
	lastReject  string
)

func Create(e *Entry) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	mu.Lock()
	defer mu.Unlock()
	e.CreatedAt = time.Now()
	items[token] = e
	return token
}

func Get(token string) *Entry {
	mu.Lock()
	defer mu.Unlock()
	return items[token]
}

func Remove(token string) {
	mu.Lock()
	defer mu.Unlock()
	delete(items, token)
}

func (e *Entry) IsDone() bool {
	mu.Lock()
	defer mu.Unlock()
	return e.done
}

func (e *Entry) MarkDone() {
	mu.Lock()
	defer mu.Unlock()
	e.done = true
}

func SetBotUsername(u string) {
	mu.Lock()
	botUsername = u
	mu.Unlock()
}

func BotUsername() string {
	mu.Lock()
	defer mu.Unlock()
	return botUsername
}

// SetLastReject / LastReject: alasan penolakan inline terakhir (dibaca userbot supaya tampil di chat).
func SetLastReject(s string) {
	mu.Lock()
	lastReject = s
	mu.Unlock()
}

func LastReject() string {
	mu.Lock()
	defer mu.Unlock()
	return lastReject
}

// StartCleanup membuang permintaan yang sudah lebih dari 6 jam.
func StartCleanup(ctx context.Context) {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				limit := time.Now().Add(-6 * time.Hour)
				mu.Lock()
				for k, e := range items {
					if e.CreatedAt.Before(limit) {
						delete(items, k)
					}
				}
				mu.Unlock()
			}
		}
	}()
}
