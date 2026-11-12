package secure

import "testing"

// Vektor ini dibuat oleh secure.js (Node) dengan SESSION_KEY di bawah.
// Kalau tes ini lulus, sesi terenkripsi lama bisa dibaca versi Go tanpa login ulang.
const (
	testKey  = "kunci-uji-12345678901234567890123456"
	nodeBlob = "enc1:rfh2G/jk0SCGCW28XfQ0ASxP/qUajIBJHEv5RfaWNUaMcl7fzTi/pXu69rAFZyboiXV7oPsndw=="
	nodePlain = "1BQANOTEzrandomStringSesi=="
)

func TestOpenNodeBlob(t *testing.T) {
	s := New(t.TempDir(), testKey)
	got, rewrite, err := s.Open(nodeBlob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != nodePlain || rewrite {
		t.Fatalf("got %q rewrite=%v", got, rewrite)
	}
}

func TestRoundTrip(t *testing.T) {
	s := New(t.TempDir(), testKey)
	blob, err := s.Seal("halo sesi")
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Open(blob)
	if err != nil || got != "halo sesi" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestWrongKey(t *testing.T) {
	s := New(t.TempDir(), "kunci-lain-yang-salah-123456789012345")
	if _, _, err := s.Open(nodeBlob); err == nil {
		t.Fatal("seharusnya gagal dengan kunci salah")
	}
}

func TestPlainTextNeedsRewrite(t *testing.T) {
	s := New(t.TempDir(), testKey)
	got, rewrite, err := s.Open("sesi-polos")
	if err != nil || got != "sesi-polos" || !rewrite {
		t.Fatalf("got %q rewrite=%v err=%v", got, rewrite, err)
	}
}
