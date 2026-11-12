package ubot

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gotd/td/session"

	"cu/internal/secure"
)

// ParseGramJS membaca StringSession GramJS:
// "1" + base64( dc(1) | panjangAlamat(2, BE) | alamat(teks) | port(2, BE) | authKey(256) ).
// Bentuk Telethon lama (alamat IPv4 4 byte) juga dicoba sebagai cadangan.
func ParseGramJS(s string) (dc int, addr string, port int, key []byte, err error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '1' {
		return 0, "", 0, nil, errors.New("bukan sesi GramJS (harus diawali 1)")
	}
	buf, e := base64.StdEncoding.DecodeString(s[1:])
	if e != nil {
		return 0, "", 0, nil, fmt.Errorf("base64 sesi tidak valid: %w", e)
	}
	if len(buf) >= 1+2+2+256 {
		n := int(binary.BigEndian.Uint16(buf[1:3]))
		if 3+n+2+256 == len(buf) {
			return int(buf[0]), string(buf[3 : 3+n]), int(binary.BigEndian.Uint16(buf[3+n : 5+n])), buf[5+n:], nil
		}
	}
	if len(buf) == 1+4+2+256 { // format Telethon: IPv4 4 byte
		ip := net.IP(buf[1:5]).String()
		return int(buf[0]), ip, int(binary.BigEndian.Uint16(buf[5:7])), buf[7:], nil
	}
	return 0, "", 0, nil, fmt.Errorf("panjang data sesi tidak dikenali (%d byte)", len(buf))
}

// gramjsToGotd mengubah sesi GramJS menjadi JSON sesi gotd.
func gramjsToGotd(ctx context.Context, s string) ([]byte, error) {
	dc, addr, port, key, err := ParseGramJS(s)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(key)
	data := &session.Data{
		DC:        dc,
		Addr:      net.JoinHostPort(addr, strconv.Itoa(port)),
		AuthKey:   key,
		AuthKeyID: sum[12:20], // 8 byte rendah dari SHA-1(authKey)
	}
	mem := &session.StorageMemory{}
	loader := session.Loader{Storage: mem}
	if err := loader.Save(ctx, data); err != nil {
		return nil, err
	}
	return mem.LoadSession(ctx)
}

// fileStorage: session.Storage terenkripsi.
//   - session.gotd : sesi milik versi Go (JSON gotd), diperbarui otomatis oleh gotd.
//   - session.txt  : sesi lama versi Node (GramJS). Hanya DIBACA, tidak diubah, supaya bisa kembali ke Node.
type fileStorage struct {
	sealer *secure.Sealer
	gotd   string
	node   string
}

func newFileStorage(sealer *secure.Sealer, dir string) *fileStorage {
	return &fileStorage{sealer: sealer, gotd: filepath.Join(dir, "session.gotd"), node: filepath.Join(dir, "session.txt")}
}

var errKeyChanged = errors.New("sesi terenkripsi tidak bisa dibaca (SESSION_KEY berubah / kunci hilang), pengguna perlu /connect ulang")

func (f *fileStorage) read(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text, _, oerr := f.sealer.Open(string(b))
	if oerr != nil {
		return "", errKeyChanged
	}
	return strings.TrimSpace(text), nil
}

func (f *fileStorage) LoadSession(ctx context.Context) ([]byte, error) {
	text, err := f.read(f.gotd)
	if err == nil && text != "" {
		return []byte(text), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	text, err = f.read(f.node)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, session.ErrNotFound
		}
		return nil, err
	}
	if text == "" {
		return nil, session.ErrNotFound
	}
	if strings.HasPrefix(text, "{") {
		return []byte(text), nil
	}
	return gramjsToGotd(ctx, text)
}

func (f *fileStorage) StoreSession(ctx context.Context, data []byte) error {
	return f.write(f.gotd, string(data))
}

func (f *fileStorage) write(path, text string) error {
	sealed, err := f.sealer.Seal(strings.TrimSpace(text))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sealed), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (f *fileStorage) has() bool {
	for _, p := range []string{f.gotd, f.node} {
		if t, err := f.read(p); err == nil && t != "" {
			return true
		}
	}
	return false
}

func (f *fileStorage) clear() {
	_ = os.Remove(f.gotd)
	_ = os.Remove(f.node)
}
