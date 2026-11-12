package ubot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// Login: login akun lewat chat bot (nomor HP -> OTP -> 2FA). Memenuhi bot.LoginFlow.
type Login struct {
	cfg Config

	mu     sync.Mutex
	mem    *session.StorageMemory
	client *telegram.Client
	cancel context.CancelFunc
	done   chan error
	phone  string
	hash   string
}

func NewLogin(cfg Config) *Login { return &Login{cfg: cfg} }

func (l *Login) SendCode(ctx context.Context, phone string) error {
	if l.cfg.AppID == 0 || l.cfg.AppHash == "" {
		return errors.New("API_ID dan API_HASH belum diisi di .env")
	}
	mem := &session.StorageMemory{}
	client := telegram.NewClient(l.cfg.AppID, l.cfg.AppHash, telegram.Options{SessionStorage: mem})
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- client.Run(runCtx, func(ctx context.Context) error {
			close(ready)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	l.mu.Lock()
	l.mem, l.client, l.cancel, l.done = mem, client, cancel, done
	l.mu.Unlock()

	select {
	case <-ready:
	case err := <-done:
		l.mu.Lock()
		l.cancel, l.done = nil, nil
		l.mu.Unlock()
		cancel()
		return fmt.Errorf("gagal menyambung ke Telegram: %w", err)
	case <-time.After(30 * time.Second):
		l.Cancel()
		return errors.New("timeout menyambung ke Telegram")
	case <-ctx.Done():
		l.Cancel()
		return ctx.Err()
	}

	sent, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		l.Cancel()
		return err
	}
	sc, ok := sent.(*tg.AuthSentCode)
	if !ok {
		l.Cancel()
		return errors.New("Telegram tidak mengirim kode (akun mungkin sudah login di sini)")
	}
	l.mu.Lock()
	l.phone, l.hash = phone, sc.PhoneCodeHash
	l.mu.Unlock()
	return nil
}

func (l *Login) snapshot() (*telegram.Client, string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.client, l.phone, l.hash
}

// SubmitCode: needPassword=true bila akun memakai verifikasi 2 langkah.
func (l *Login) SubmitCode(ctx context.Context, code string) (bool, string, error) {
	client, phone, hash := l.snapshot()
	if client == nil {
		return false, "", errors.New("proses login belum dimulai")
	}
	_, err := client.Auth().SignIn(ctx, phone, code, hash)
	if errors.Is(err, auth.ErrPasswordAuthNeeded) {
		return true, "", nil
	}
	var su *auth.SignUpRequired
	if errors.As(err, &su) {
		return false, "", errors.New("PHONE_NUMBER_UNOCCUPIED: nomor ini belum terdaftar di Telegram")
	}
	if err != nil {
		return false, "", err
	}
	s, err := l.export(ctx)
	return false, s, err
}

func (l *Login) SubmitPassword(ctx context.Context, password string) (string, error) {
	client, _, _ := l.snapshot()
	if client == nil {
		return "", errors.New("proses login belum dimulai")
	}
	if _, err := client.Auth().Password(ctx, password); err != nil {
		if errors.Is(err, auth.ErrPasswordInvalid) {
			return "", errors.New("PASSWORD_HASH_INVALID")
		}
		return "", err
	}
	return l.export(ctx)
}

// export mengambil sesi (JSON gotd) lalu menutup koneksi sementara.
func (l *Login) export(ctx context.Context) (string, error) {
	l.mu.Lock()
	mem := l.mem
	l.mu.Unlock()
	b, err := mem.LoadSession(ctx)
	if err != nil {
		return "", fmt.Errorf("gagal membaca sesi: %w", err)
	}
	l.Cancel()
	return string(b), nil
}

func (l *Login) Cancel() {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel, l.done = nil, nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}
