// Package secure: enkripsi sesi di disk (AES-256-GCM), format SAMA dengan secure.js versi Node.
// File sesi lama hasil Node ("enc1:" + base64(iv|tag|data)) bisa langsung dibaca.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const prefix = "enc1:"

var hex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Sealer memegang lokasi file kunci cadangan (data/.sessionkey) dan SESSION_KEY dari .env.
type Sealer struct {
	keyFile    string
	sessionKey string // isi SESSION_KEY (boleh kosong)

	mu        sync.Mutex
	fileCache []byte
}

func New(dataDir, sessionKey string) *Sealer {
	return &Sealer{
		keyFile:    filepath.Join(dataDir, ".sessionkey"),
		sessionKey: strings.TrimSpace(sessionKey),
	}
}

func (s *Sealer) envKey() []byte {
	if s.sessionKey == "" {
		return nil
	}
	h := sha256.Sum256([]byte(s.sessionKey))
	return h[:]
}

func (s *Sealer) fileKey(create bool) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fileCache != nil {
		return s.fileCache, nil
	}
	if b, err := os.ReadFile(s.keyFile); err == nil {
		h := strings.TrimSpace(string(b))
		if hex64.MatchString(h) {
			k, _ := hex.DecodeString(h)
			s.fileCache = k
			return k, nil
		}
	}
	if !create {
		return nil, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.keyFile), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.keyFile, []byte(hex.EncodeToString(k)), 0o600); err != nil {
		return nil, err
	}
	_ = os.Chmod(s.keyFile, 0o600)
	s.fileCache = k
	return k, nil
}

func (s *Sealer) primaryKey() ([]byte, error) {
	if k := s.envKey(); k != nil {
		return k, nil
	}
	return s.fileKey(true)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block) // nonce 12 byte, tag 16 byte
}

// Seal mengenkripsi teks. Keluaran: "enc1:" + base64(iv | tag | ciphertext), sama dengan Node.
func (s *Sealer) Seal(text string) (string, error) {
	key, err := s.primaryKey()
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, []byte(text), nil) // Go: ciphertext || tag
	n := len(sealed) - gcm.Overhead()
	ct, tag := sealed[:n], sealed[n:]
	out := make([]byte, 0, len(iv)+len(tag)+len(ct))
	out = append(out, iv...)
	out = append(out, tag...)
	out = append(out, ct...)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// ErrKey: file terenkripsi tetapi tidak ada kunci yang cocok (SESSION_KEY berubah / kunci hilang).
var ErrKey = errors.New("kunci sesi tidak cocok")

// Open membaca blob. rewrite=true bila isi perlu ditulis ulang (masih teks polos / beda kunci).
func (s *Sealer) Open(blob string) (text string, rewrite bool, err error) {
	b := strings.TrimSpace(blob)
	if b == "" {
		return "", false, nil
	}
	if !strings.HasPrefix(b, prefix) {
		return b, true, nil // teks polos lama
	}
	buf, derr := base64.StdEncoding.DecodeString(b[len(prefix):])
	if derr != nil || len(buf) < 28 {
		return "", false, ErrKey
	}
	iv, tag, data := buf[:12], buf[12:28], buf[28:]
	ek := s.envKey()
	fk, _ := s.fileKey(false)
	for _, k := range [][]byte{ek, fk} {
		if k == nil {
			continue
		}
		gcm, gerr := newGCM(k)
		if gerr != nil {
			continue
		}
		ctTag := make([]byte, 0, len(data)+len(tag))
		ctTag = append(ctTag, data...)
		ctTag = append(ctTag, tag...)
		plain, oerr := gcm.Open(nil, iv, ctTag, nil)
		if oerr == nil {
			return string(plain), ek != nil && !equal(k, ek), nil
		}
	}
	return "", false, ErrKey
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
