// Package manager: port manager.js. Daftar pengguna, masa aktif, trial, dan pengawas kedaluwarsa.
// Format users.json dan trials.json SAMA dengan versi Node (expires & at dalam milidetik).
package manager

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const day = 24 * time.Hour

// Userbot adalah kontrak yang nanti dipenuhi oleh userbot gotd (tahap 2).
type Userbot interface {
	HasSession() bool
	IsRunning() bool
	Start(ctx context.Context) error
	// StartSession menyimpan sesi baru lalu menyalakan; mengembalikan nama depan akun.
	StartSession(ctx context.Context, session string) (name string, err error)
	SaveSession(session string) error
	Stop()
	LogoutRemote(ctx context.Context) error
	ClearSession()
}

// Factory membuat userbot untuk satu pengguna.
type Factory func(id string) Userbot

type User struct {
	Name     string `json:"name"`
	Expires  *int64 `json:"expires"`            // ms epoch; null = tanpa batas
	Reminded int64  `json:"reminded,omitempty"` // nilai Expires yang sudah diberi pengingat
}

type Trial struct {
	At   int64  `json:"at"`
	Name string `json:"name"`
}

type Config struct {
	DataDir    string
	OwnerID    string
	TrialDays  int           // TRIAL_DAYS (bawaan 1)
	RemindIn   time.Duration // REMIND_HOURS (bawaan 2 jam)
	PurgeAfter time.Duration // PURGE_EXPIRED_DAYS (bawaan 30 hari, 0 = mati)
}

type Manager struct {
	cfg     Config
	factory Factory

	mu     sync.Mutex
	users  map[string]*User
	trials map[string]*Trial
	bots   map[string]Userbot
}

func New(cfg Config, f Factory) (*Manager, error) {
	if cfg.TrialDays <= 0 {
		cfg.TrialDays = 1
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{
		cfg:     cfg,
		factory: f,
		users:   map[string]*User{},
		trials:  map[string]*Trial{},
		bots:    map[string]Userbot{},
	}
	m.load(m.usersFile(), &m.users)
	m.load(m.trialsFile(), &m.trials)
	return m, nil
}

func (m *Manager) usersFile() string  { return filepath.Join(m.cfg.DataDir, "users.json") }
func (m *Manager) trialsFile() string { return filepath.Join(m.cfg.DataDir, "trials.json") }

func (m *Manager) load(path string, v any) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, v); err != nil {
		log.Printf("peringatan: %s rusak, dimulai kosong: %v", filepath.Base(path), err)
	}
}

// save menulis atomik (file sementara lalu rename). Dipanggil saat m.mu dipegang.
func (m *Manager) save(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("gagal menyimpan %s: %v", filepath.Base(path), err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("gagal menyimpan %s: %v", filepath.Base(path), err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("gagal menyimpan %s: %v", filepath.Base(path), err)
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

func (m *Manager) IsOwner(id string) bool { return m.cfg.OwnerID != "" && id == m.cfg.OwnerID }

func (m *Manager) isExpiredLocked(id string) bool {
	if m.IsOwner(id) {
		return false
	}
	u, ok := m.users[id]
	return !ok || (u.Expires != nil && *u.Expires < nowMs())
}

func (m *Manager) IsAllowed(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.IsOwner(id) || !m.isExpiredLocked(id)
}

// Bot mengambil (atau membuat) userbot milik satu pengguna.
func (m *Manager) Bot(id string) Userbot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.botLocked(id)
}

func (m *Manager) botLocked(id string) Userbot {
	b, ok := m.bots[id]
	if !ok {
		b = m.factory(id)
		m.bots[id] = b
	}
	return b
}

func (m *Manager) restartIfNeeded(ctx context.Context, id string) {
	b := m.Bot(id)
	if b.HasSession() && !b.IsRunning() {
		if err := b.Start(ctx); err != nil {
			log.Printf("[%s] Gagal menyalakan ulang: %v", id, err)
		}
	}
}

// AddUser memberi akses. days <= 0 = tanpa batas waktu.
func (m *Manager) AddUser(ctx context.Context, id string, days int, name string) User {
	m.mu.Lock()
	prev := m.users[id]
	u := &User{}
	if prev != nil {
		u.Name = prev.Name
	}
	if name != "" {
		u.Name = name
	}
	if days > 0 {
		e := time.Now().Add(time.Duration(days) * day).UnixMilli()
		u.Expires = &e
	}
	m.users[id] = u
	m.save(m.usersFile(), m.users)
	out := *u
	m.mu.Unlock()

	m.restartIfNeeded(ctx, id)
	return out
}

// ExtendUser menambah hari ke sisa masa aktif (dipakai /buy). Pengguna permanen tidak diubah.
func (m *Manager) ExtendUser(ctx context.Context, id string, days int, name string) User {
	m.mu.Lock()
	prev := m.users[id]
	if prev == nil || prev.Expires != nil {
		base := nowMs()
		if prev != nil && prev.Expires != nil && *prev.Expires > base {
			base = *prev.Expires
		}
		e := base + int64(days)*int64(day/time.Millisecond)
		nm := name
		if nm == "" && prev != nil {
			nm = prev.Name
		}
		m.users[id] = &User{Name: nm, Expires: &e}
		m.save(m.usersFile(), m.users)
	}
	out := *m.users[id]
	m.mu.Unlock()

	m.restartIfNeeded(ctx, id)
	return out
}

type TrialResult struct {
	OK      bool
	Reason  string // owner | active | used
	At      int64
	Expires *int64
	Days    int
}

// ClaimTrial: 1 akun Telegram hanya bisa klaim 1x sampai di-reset admin.
func (m *Manager) ClaimTrial(ctx context.Context, id, name string) TrialResult {
	m.mu.Lock()
	if m.IsOwner(id) {
		m.mu.Unlock()
		return TrialResult{Reason: "owner"}
	}
	if !m.isExpiredLocked(id) {
		m.mu.Unlock()
		return TrialResult{Reason: "active"}
	}
	if t, ok := m.trials[id]; ok {
		at := t.At
		m.mu.Unlock()
		return TrialResult{Reason: "used", At: at}
	}
	m.trials[id] = &Trial{At: nowMs(), Name: name}
	m.save(m.trialsFile(), m.trials)
	m.mu.Unlock()

	u := m.AddUser(ctx, id, m.cfg.TrialDays, name)
	return TrialResult{OK: true, Expires: u.Expires, Days: m.cfg.TrialDays}
}

// ResetTrial: satu ID, atau "all". Mengembalikan jumlah yang direset.
func (m *Manager) ResetTrial(target string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.EqualFold(target, "all") {
		n := len(m.trials)
		m.trials = map[string]*Trial{}
		m.save(m.trialsFile(), m.trials)
		return n
	}
	_, had := m.trials[target]
	delete(m.trials, target)
	m.save(m.trialsFile(), m.trials)
	if had {
		return 1
	}
	return 0
}

type TrialInfo struct {
	ID   string
	Name string
	At   int64
}

func (m *Manager) ListTrials() []TrialInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TrialInfo, 0, len(m.trials))
	for id, t := range m.trials {
		out = append(out, TrialInfo{ID: id, Name: t.Name, At: t.At})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// DelUser mencabut akses: sesi di server Telegram diakhiri, userbot dimatikan, file sesi dihapus.
func (m *Manager) DelUser(ctx context.Context, id string) bool {
	m.mu.Lock()
	_, existed := m.users[id]
	delete(m.users, id)
	m.save(m.usersFile(), m.users)
	b := m.botLocked(id)
	m.mu.Unlock()

	_ = b.LogoutRemote(ctx)
	b.Stop()
	b.ClearSession()
	return existed
}

type UserInfo struct {
	ID         string
	Name       string
	Expires    *int64
	Expired    bool
	Running    bool
	HasSession bool
}

func (m *Manager) ListUsers() []UserInfo {
	m.mu.Lock()
	ids := make([]string, 0, len(m.users))
	for id := range m.users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]UserInfo, 0, len(ids))
	for _, id := range ids {
		u := m.users[id]
		b := m.botLocked(id)
		out = append(out, UserInfo{
			ID: id, Name: u.Name, Expires: u.Expires,
			Expired:    u.Expires != nil && *u.Expires < nowMs(),
			Running:    b.IsRunning(),
			HasSession: b.HasSession(),
		})
	}
	m.mu.Unlock()
	return out
}

// LoadAll menyalakan semua userbot yang punya sesi dan masih aktif, dengan jeda 1,5 detik.
func (m *Manager) LoadAll(ctx context.Context) {
	m.mu.Lock()
	seen := map[string]bool{}
	var ids []string
	if m.cfg.OwnerID != "" {
		seen[m.cfg.OwnerID] = true
		ids = append(ids, m.cfg.OwnerID)
	}
	keys := make([]string, 0, len(m.users))
	for id := range m.users {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	for _, id := range ids {
		if !m.IsAllowed(id) {
			continue
		}
		b := m.Bot(id)
		if !b.HasSession() {
			continue
		}
		if err := b.Start(ctx); err != nil {
			log.Printf("[%s] Gagal menyalakan userbot: %v", id, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// Watcher memeriksa tiap menit: pengingat, matikan yang kedaluwarsa, bersihkan sesi lama.
type Watcher struct {
	OnExpire func(id string)
	OnWarn   func(id string, left time.Duration)
	OnPurge  func(id string)
}

func (m *Manager) StartWatcher(ctx context.Context, w Watcher) {
	remind := m.cfg.RemindIn
	if remind <= 0 {
		remind = 2 * time.Hour
	}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.tick(ctx, w, remind)
			}
		}
	}()
}

func (m *Manager) tick(ctx context.Context, w Watcher, remind time.Duration) {
	type item struct {
		id      string
		expires *int64
	}
	m.mu.Lock()
	snap := make([]item, 0, len(m.users))
	for id, u := range m.users {
		snap = append(snap, item{id, u.Expires})
	}
	m.mu.Unlock()

	for _, it := range snap {
		now := nowMs()
		if it.expires != nil && w.OnWarn != nil {
			left := *it.expires - now
			if left > 0 && left <= remind.Milliseconds() {
				m.mu.Lock()
				u := m.users[it.id]
				fire := u != nil && u.Reminded != *it.expires
				if fire {
					u.Reminded = *it.expires
					m.save(m.usersFile(), m.users)
				}
				m.mu.Unlock()
				if fire {
					w.OnWarn(it.id, time.Duration(left)*time.Millisecond)
				}
			}
		}
		if m.cfg.PurgeAfter > 0 && it.expires != nil && *it.expires < now-m.cfg.PurgeAfter.Milliseconds() {
			b := m.Bot(it.id)
			if b.HasSession() {
				_ = b.LogoutRemote(ctx)
				b.Stop()
				b.ClearSession()
				if w.OnPurge != nil {
					w.OnPurge(it.id)
				}
			}
			continue
		}
		if it.expires != nil && *it.expires < now {
			b := m.Bot(it.id)
			if b.IsRunning() {
				b.Stop()
				if w.OnExpire != nil {
					w.OnExpire(it.id)
				}
			}
		}
	}
}

// StopUser mematikan userbot satu pengguna.
func (m *Manager) StopUser(id string) {
	m.Bot(id).Stop()
}

// Info ringkasan akses satu pengguna (dipakai /status).
type Info struct {
	Owner      bool
	Allowed    bool
	Expires    *int64
	Running    bool
	HasSession bool
}

func (m *Manager) Info(id string) Info {
	m.mu.Lock()
	owner := m.IsOwner(id)
	allowed := owner || !m.isExpiredLocked(id)
	var exp *int64
	if u := m.users[id]; u != nil && !owner {
		exp = u.Expires
	}
	b := m.botLocked(id)
	m.mu.Unlock()
	return Info{Owner: owner, Allowed: allowed, Expires: exp, Running: b.IsRunning(), HasSession: b.HasSession()}
}
