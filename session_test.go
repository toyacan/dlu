package ubot

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/gotd/td/session"
)

func fakeGramJS(dc int, addr string, port int) (string, []byte) {
	key := make([]byte, 256)
	for i := range key {
		key[i] = byte(i * 7)
	}
	buf := []byte{byte(dc)}
	n := make([]byte, 2)
	binary.BigEndian.PutUint16(n, uint16(len(addr)))
	buf = append(buf, n...)
	buf = append(buf, addr...)
	p := make([]byte, 2)
	binary.BigEndian.PutUint16(p, uint16(port))
	buf = append(buf, p...)
	buf = append(buf, key...)
	return "1" + base64.StdEncoding.EncodeToString(buf), key
}

func TestParseGramJS(t *testing.T) {
	s, key := fakeGramJS(2, "149.154.167.51", 443)
	dc, addr, port, got, err := ParseGramJS(s)
	if err != nil || dc != 2 || addr != "149.154.167.51" || port != 443 || string(got) != string(key) {
		t.Fatalf("hasil: %d %s %d err=%v", dc, addr, port, err)
	}
}

func TestConvertToGotd(t *testing.T) {
	s, key := fakeGramJS(4, "149.154.167.91", 443)
	js, err := gramjsToGotd(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	mem := &session.StorageMemory{}
	if err := mem.StoreSession(context.Background(), js); err != nil {
		t.Fatal(err)
	}
	d, err := (&session.Loader{Storage: mem}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.DC != 4 || string(d.AuthKey) != string(key) || d.Addr != "149.154.167.91:443" {
		t.Fatalf("sesi gotd tidak cocok: dc=%d addr=%s", d.DC, d.Addr)
	}
}

func TestParseGramJSInvalid(t *testing.T) {
	for _, s := range []string{"", "2abc", "1!!!", "1AAAA"} {
		if _, _, _, _, err := ParseGramJS(s); err == nil {
			t.Fatalf("%q seharusnya ditolak", s)
		}
	}
}
