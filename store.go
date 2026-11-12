package slot

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store: picks.json satu pengguna, { "<chatId>": Book }. Semua akses lewat kunci; tulis ditunda 300 ms.
type Store struct {
	mu    sync.Mutex
	path  string
	books map[string]*Book
	timer *time.Timer
}

func OpenStore(userDir string) *Store {
	s := &Store{path: filepath.Join(userDir, "picks.json"), books: map[string]*Book{}}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.books); err != nil || s.books == nil {
			if err != nil {
				_ = os.WriteFile(s.path+".rusak", b, 0o600) // jangan menimpa file yang tak terbaca
				log.Printf("peringatan: %s tidak terbaca (%v), salinan di .rusak", s.path, err)
			}
			s.books = map[string]*Book{}
		}
		for k, v := range s.books {
			if v == nil {
				delete(s.books, k)
			}
		}
	}
	return s
}

func (s *Store) scheduleLocked() {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(300*time.Millisecond, s.Flush)
}

// Flush menulis ke disk sekarang (atomik).
func (s *Store) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := json.Marshal(s.books)
	if err != nil {
		log.Printf("slot: gagal menyimpan: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		log.Printf("slot: gagal menyimpan: %v", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, j, 0o600); err != nil {
		log.Printf("slot: gagal menyimpan: %v", err)
		return
	}
	_ = os.Rename(tmp, s.path)
}

// View memberi akses baca (b bisa nil bila chat belum punya data). Jangan mengubah b di sini.
func (s *Store) View(chatID string, fn func(b *Book)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.books[NormID(chatID)])
}

// Edit memberi akses ubah (buku dibuat bila belum ada) lalu menyimpan.
func (s *Store) Edit(chatID string, fn func(b *Book)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := NormID(chatID)
	b := s.books[k]
	if b == nil {
		b = &Book{K: []*Pick{}, B: []*Pick{}}
		s.books[k] = b
	}
	fn(b)
	s.scheduleLocked()
}

// Replace mengganti buku satu chat; nil = hapus.
func (s *Store) Replace(chatID string, b *Book) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := NormID(chatID)
	if b == nil {
		delete(s.books, k)
	} else {
		s.books[k] = b
	}
	s.scheduleLocked()
}
