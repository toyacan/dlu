// Package ubot: userbot Telegram (MTProto lewat gotd), pengganti ubot.js.
// Tahap 3a (kerangka): sambung/putus, sesi terenkripsi (+ baca sesi lama GramJS), login OTP/2FA,
// logout, dan perintah dasar .ping / .id. Perintah lain (.list, .rk, .sk, ...) menyusul satu per satu.
package ubot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"cu/internal/manager"
	"cu/internal/secure"
)

type Config struct {
	AppID   int
	AppHash string
	DataDir string
	Sealer  *secure.Sealer
}

type Userbot struct {
	cfg   Config
	id    string
	store *fileStorage

	mu      sync.Mutex
	want    bool // seharusnya berjalan (dimatikan oleh Stop / logout / sesi tidak berlaku)
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	api     *tg.Client
	meID    int64
	meName  string
}

var _ manager.Userbot = (*Userbot)(nil)

// NewFactory dipakai manager: satu Userbot per pengguna, data di data/<id>/.
func NewFactory(cfg Config) func(id string) manager.Userbot {
	return func(id string) manager.Userbot { return New(cfg, id) }
}

func New(cfg Config, id string) *Userbot {
	dir := filepath.Join(cfg.DataDir, id)
	_ = os.MkdirAll(dir, 0o700)
	return &Userbot{cfg: cfg, id: id, store: newFileStorage(cfg.Sealer, dir)}
}

func (u *Userbot) HasSession() bool { return u.store.has() }

func (u *Userbot) IsRunning() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running
}

func (u *Userbot) wanted() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.want
}

func (u *Userbot) setWant(v bool) {
	u.mu.Lock()
	u.want = v
	u.mu.Unlock()
}

// once: hasil sambungan pertama dikirim tepat sekali.
type once struct {
	mu   sync.Mutex
	sent bool
	ch   chan error
}

func (o *once) send(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sent {
		return
	}
	o.sent = true
	o.ch <- err
}

// Start menyambung dan menunggu sampai tersambung (atau gagal). Setelah tersambung, bila koneksi
// terputus karena galat sementara, otomatis dicoba lagi (jeda bertambah sampai 1 menit).
func (u *Userbot) Start(ctx context.Context) error {
	if u.cfg.AppID == 0 || u.cfg.AppHash == "" {
		return errors.New("API_ID dan API_HASH belum diisi di .env")
	}
	if !u.store.has() {
		return errors.New("belum ada sesi, ketik /connect")
	}
	u.Stop()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	first := &once{ch: make(chan error, 1)}
	u.mu.Lock()
	u.want, u.cancel, u.done = true, cancel, done
	u.mu.Unlock()
	go func() {
		defer close(done)
		u.loop(runCtx, first)
	}()
	select {
	case err := <-first.ch:
		return err
	case <-time.After(60 * time.Second):
		return errors.New("timeout menyambung ke Telegram (akan dicoba lagi otomatis)")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *Userbot) loop(ctx context.Context, first *once) {
	delay := 5 * time.Second
	for {
		err := u.runOnce(ctx, first)
		first.send(err) // bila gagal sebelum tersambung
		if ctx.Err() != nil || !u.wanted() {
			return
		}
		if isFatalAuth(err) {
			log.Printf("[%s] sesi tidak berlaku lagi (%v). Pengguna perlu /connect ulang.", u.id, err)
			u.setWant(false)
			return
		}
		log.Printf("[%s] userbot berhenti (%v), mencoba lagi dalam %v", u.id, err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < time.Minute {
			delay *= 2
		}
	}
}

func isFatalAuth(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, c := range []string{"AUTH_KEY_UNREGISTERED", "AUTH_KEY_DUPLICATED", "USER_DEACTIVATED", "SESSION_REVOKED", "SESSION_EXPIRED"} {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

func (u *Userbot) runOnce(ctx context.Context, first *once) error {
	disp := tg.NewUpdateDispatcher()
	client := telegram.NewClient(u.cfg.AppID, u.cfg.AppHash, telegram.Options{
		SessionStorage: u.store,
		UpdateHandler:  disp,
	})
	api := client.API()
	disp.OnNewMessage(func(ctx context.Context, e tg.Entities, upd *tg.UpdateNewMessage) error {
		u.onMessage(ctx, api, e, upd.Message)
		return nil
	})
	disp.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, upd *tg.UpdateNewChannelMessage) error {
		u.onMessage(ctx, api, e, upd.Message)
		return nil
	})
	defer func() {
		u.mu.Lock()
		u.running, u.api = false, nil
		u.mu.Unlock()
	}()
	return client.Run(ctx, func(ctx context.Context) error {
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		u.mu.Lock()
		u.running, u.api, u.meID, u.meName = true, api, self.ID, self.FirstName
		u.mu.Unlock()
		log.Printf("[%s] userbot tersambung sebagai %s", u.id, self.FirstName)
		first.send(nil)
		<-ctx.Done()
		return ctx.Err()
	})
}

// Stop mematikan userbot dan menunggu berhenti (maks 5 dtk).
func (u *Userbot) Stop() {
	u.mu.Lock()
	u.want = false
	cancel, done := u.cancel, u.done
	u.cancel, u.done = nil, nil
	u.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// LogoutRemote mengakhiri sesi ini di server Telegram (auth.logOut). nil = berhasil.
func (u *Userbot) LogoutRemote(ctx context.Context) error {
	u.mu.Lock()
	u.want = false // jangan disambung ulang
	api := u.api
	u.mu.Unlock()
	if api != nil {
		_, err := api.AuthLogOut(ctx)
		return err
	}
	if !u.store.has() {
		return errors.New("tidak ada sesi")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := telegram.NewClient(u.cfg.AppID, u.cfg.AppHash, telegram.Options{SessionStorage: u.store})
	return client.Run(ctx, func(ctx context.Context) error {
		_, err := client.API().AuthLogOut(ctx)
		return err
	})
}

func (u *Userbot) ClearSession() { u.store.clear() }

// SaveSession: JSON gotd -> session.gotd. String GramJS -> session.txt (gotd lama dihapus).
func (u *Userbot) SaveSession(s string) error {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		return u.store.write(u.store.gotd, s)
	}
	_ = os.Remove(u.store.gotd)
	return u.store.write(u.store.node, s)
}

func (u *Userbot) StartSession(ctx context.Context, s string) (string, error) {
	if err := u.SaveSession(s); err != nil {
		return "", err
	}
	if err := u.Start(ctx); err != nil {
		return "", err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.meName, nil
}

// ---------- Pesan masuk ----------

func inputPeer(e tg.Entities, p tg.PeerClass, meID int64) (tg.InputPeerClass, bool) {
	switch v := p.(type) {
	case *tg.PeerUser:
		if v.UserID == meID {
			return &tg.InputPeerSelf{}, true
		}
		if us, ok := e.Users[v.UserID]; ok {
			return &tg.InputPeerUser{UserID: v.UserID, AccessHash: us.AccessHash}, true
		}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: v.ChatID}, true
	case *tg.PeerChannel:
		if ch, ok := e.Channels[v.ChannelID]; ok {
			return &tg.InputPeerChannel{ChannelID: v.ChannelID, AccessHash: ch.AccessHash}, true
		}
	}
	return nil, false
}

// chatIDText: format ID chat seperti di Telegram (grup -123, channel/supergrup -100123).
func chatIDText(p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		return fmt.Sprint(v.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("-%d", v.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("-100%d", v.ChannelID)
	}
	return "?"
}

func (u *Userbot) edit(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, id int, text string) {
	_, err := api.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: peer, ID: id, Message: text})
	if err != nil && !strings.Contains(err.Error(), "MESSAGE_NOT_MODIFIED") {
		log.Printf("[%s] gagal mengedit pesan: %v", u.id, err)
	}
}

func (u *Userbot) onMessage(ctx context.Context, api *tg.Client, e tg.Entities, m tg.MessageClass) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%s] panic di handler pesan: %v", u.id, r)
		}
	}()
	msg, ok := m.(*tg.Message)
	if !ok || !msg.Out { // sementara hanya pesan keluar milik akun sendiri (sudo menyusul)
		return
	}
	text := strings.TrimSpace(msg.Message)
	if len(text) < 2 || text[0] != '.' {
		return
	}
	cmd := text[1:]
	if i := strings.IndexAny(cmd, " \t\r\n"); i >= 0 {
		cmd = cmd[:i]
	}
	u.mu.Lock()
	meID := u.meID
	u.mu.Unlock()
	peer, ok := inputPeer(e, msg.PeerID, meID)
	if !ok {
		return
	}
	switch strings.ToLower(cmd) {
	case "ping":
		t := time.Now()
		u.edit(ctx, api, peer, msg.ID, "🏓 ...")
		u.edit(ctx, api, peer, msg.ID, fmt.Sprintf("🏓 Pong! %d ms", time.Since(t).Milliseconds()))
	case "id":
		u.edit(ctx, api, peer, msg.ID, fmt.Sprintf("ID: %d\nChat ID: %s", meID, chatIDText(msg.PeerID)))
	}
}
