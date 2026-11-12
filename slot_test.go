package slot

import (
	"encoding/json"
	"strings"
	"testing"
)

// Nilai harapan di tes parsing diambil dengan menjalankan fungsi asli di ubot.js (Node).

func TestParsePerak(t *testing.T) {
	cases := []struct {
		n, s string
		want int
	}{
		{"10", "", 10000}, {"10,9", "", 10900}, {"3,6", "", 3600}, {"1", "k", 1000},
		{"1000", "", 1000}, {"300", "", 300}, {"1.000", "", 1000}, {"50", "", 50000},
		{"0", "", 0}, {"2,5", "rb", 2500}, {"99", "", 99000}, {"100", "", 100},
	}
	for _, c := range cases {
		if got := ParsePerak(c.n, c.s); got != c.want {
			t.Errorf("ParsePerak(%q,%q) = %d, harapan %d", c.n, c.s, got, c.want)
		}
	}
}

func TestCleanNameAndNick(t *testing.T) {
	cases := []struct{ in, clean, nick string }{
		{"CAPO 19/20", "CAPO", "CAPO"}, {"ALBOT GANTENG 07#09", "ALBOT", "ALBOT"},
		{"kazel", "kazel", "KAZEL"}, {"AIDIP 19/20", "AIDIP", "AIDIP"}, {"  ", "  ", ""},
		{"X-Y", "X-Y", "XY"}, {"NAMA - 12/34", "NAMA", "NAMA"}, {"A.B 5/6 C", "A.B", "AB"},
	}
	for _, c := range cases {
		if got := CleanName(c.in); got != c.clean {
			t.Errorf("CleanName(%q) = %q, harapan %q", c.in, got, c.clean)
		}
		if got := Nick(c.in); got != c.nick {
			t.Errorf("Nick(%q) = %q, harapan %q", c.in, got, c.nick)
		}
	}
}

func TestParseDirect(t *testing.T) {
	type r struct {
		side   string
		amount int
		ok     bool
	}
	cases := []struct {
		in     string
		plain  r
		perak  r
		isAll  bool
	}{
		{"k50", r{"K", 50, true}, r{"K", 50000, true}, false},
		{"K 50 cek", r{"K", 50, true}, r{"K", 50000, true}, false},
		{"b20", r{"B", 20, true}, r{"B", 20000, true}, false},
		{"k40cek", r{}, r{}, false},
		{"b2b", r{}, r{}, false},
		{"kall", r{}, r{}, true},
		{"k50 ", r{"K", 50, true}, r{"K", 50000, true}, false},
		{"  b 7!", r{"B", 7, true}, r{"B", 7000, true}, false},
		{"k", r{}, r{}, false},
		{"50k", r{}, r{}, false},
		{"B50,", r{"B", 50, true}, r{"B", 50000, true}, false},
		{"k5é", r{}, r{}, false},
	}
	for _, c := range cases {
		s, a, ok := ParseDirect(c.in)
		if (r{s, a, ok}) != c.plain {
			t.Errorf("ParseDirect(%q) = %v %v %v", c.in, s, a, ok)
		}
		s, a, ok = ParsePerakText(c.in)
		if (r{s, a, ok}) != c.perak {
			t.Errorf("ParsePerakText(%q) = %v %v %v", c.in, s, a, ok)
		}
		if _, got := ParseAll(c.in); got != c.isAll {
			t.Errorf("ParseAll(%q) = %v", c.in, got)
		}
	}
}

func TestParsePick(t *testing.T) {
	s, a, n, ok := ParsePick("✅ Pik: K 30 kazel\nbaris kedua")
	if !ok || s != "K" || a != 30 || n != "KAZEL" {
		t.Fatalf("%v %v %v %v", s, a, n, ok)
	}
	if _, _, _, ok := ParsePick("Pik K 30"); ok {
		t.Fatal("tanpa titik dua seharusnya ditolak")
	}
}

func TestPinPlayers(t *testing.T) {
	txt := "GAME 1 : K 2-0 57\nDEV: IP 15 JOSJIS\nPEMAIN : (2)\nAIDIL 22\nBIANZ - 33\nAIDIL 25\nbaris bebas"
	p := ParsePinPlayers(txt)
	if p["AIDIL"] != 25 || p["BIANZ"] != 33 || len(p) != 2 {
		t.Fatalf("%v", p)
	}
	if ParsePinPlayers("tidak ada angka") != nil {
		t.Fatal("seharusnya nil")
	}
}

func TestChunkLines(t *testing.T) {
	text := "aaaa\nbbbb\ncccc"
	got := ChunkLines(text, 9)
	if len(got) != 2 || got[0] != "aaaa\nbbbb" || got[1] != "cccc" {
		t.Fatalf("%q", got)
	}
}

func newBook() *Book { return &Book{K: []*Pick{}, B: []*Pick{}, Started: true} }

func TestRecordAndRk(t *testing.T) {
	b := newBook()
	if ok, _ := b.Record("K", 50, "AIDIL", &Player{UID: "1"}, RecordOpts{Direct: true}); !ok {
		t.Fatal("K 50 harus tercatat")
	}
	b.Record("B", 30, "BIANZ", &Player{UID: "2"}, RecordOpts{Direct: true})
	want := "⭐ 🔒 K: 50 = 50\n\n⭐ B: 30 = 30\n\n🐠 B kurang 20 untuk samakan K\n\n💰 Saldo seharusnya: 80 K"
	if got := b.FormatRk(KKCfg{}); got != want {
		t.Fatalf("rk:\n%s", got)
	}
	// seimbang: tanpa gembok dan tanpa baris selisih
	b.Record("B", 50, "BIANZ", &Player{UID: "2"}, RecordOpts{Direct: true})
	if got := b.FormatRk(KKCfg{}); strings.Contains(got, "🔒") || strings.Contains(got, "🐠") {
		t.Fatalf("seimbang:\n%s", got)
	}
}

func TestMoveBetweenSides(t *testing.T) {
	b := newBook()
	p := &Player{UID: "1"}
	b.Record("K", 50, "AIDIL", p, RecordOpts{Direct: true})
	b.Record("B", 20, "AIDIL", p, RecordOpts{Direct: true})
	if len(b.K) != 0 || len(b.B) != 1 || b.B[0].Amount != 20 {
		t.Fatalf("K=%d B=%d", len(b.K), len(b.B))
	}
	// pik ulang di sisi yang sama menimpa jumlah, urutan tetap
	b.Record("B", 25, "AIDIL", p, RecordOpts{Direct: true})
	if len(b.B) != 1 || b.B[0].Amount != 25 {
		t.Fatal("seharusnya ditimpa")
	}
}

func TestClosedAndRkRules(t *testing.T) {
	b := newBook()
	b.Closed = map[string]bool{"K": true}
	if ok, why := b.Record("K", 10, "A", &Player{UID: "1"}, RecordOpts{Direct: true}); ok || why != RejectClosed {
		t.Fatalf("%v %v", ok, why)
	}
	if ok, _ := b.Record("K", 10, "A", &Player{UID: "1"}, RecordOpts{Direct: true, Force: true}); !ok {
		t.Fatal(".sk (force) harus lolos")
	}
	b.Closed = nil
	b.RK = true
	b.Record("B", 5, "C", &Player{UID: "3"}, RecordOpts{Direct: true}) // K=10 > B=5 -> B boleh, K ditolak
	if ok, why := b.Record("K", 40, "D", &Player{UID: "4"}, RecordOpts{Direct: true}); ok || why != RejectRK {
		t.Fatalf("K sudah lebih besar setelah .rk: %v %v", ok, why)
	}
}

func TestAntiClone(t *testing.T) {
	b := newBook()
	b.Record("K", 20, "AIDIL", &Player{UID: "1"}, RecordOpts{Direct: true})
	// nama sama, akun lain: tetap tercatat sebagai pemain berbeda, tapi di .list bertitik
	if ok, _ := b.Record("K", 30, "AIDIL", &Player{UID: "9"}, RecordOpts{Direct: true}); !ok {
		t.Fatal("clone tetap dicatat")
	}
	if len(b.K) != 2 {
		t.Fatalf("K=%d", len(b.K))
	}
	players := map[string]int{"AIDIL": 100}
	out := b.FormatPicks(FormatCtx{Suffix: "P"}, players, nil)
	if !strings.Contains(out, "AIDIL 20 P") || !strings.Contains(out, "AIDIL 30.") {
		t.Fatalf("list:\n%s", out)
	}
	// nama terikat ke akun lain, pengirim tidak terbaca -> ditolak
	if ok, why := b.Record("B", 5, "AIDIL", nil, RecordOpts{Direct: true}); ok || why != RejectOwner {
		t.Fatalf("%v %v", ok, why)
	}
}

func TestFormatPicksPin(t *testing.T) {
	b := newBook()
	b.Record("K", 20, "AIDIL", &Player{UID: "1"}, RecordOpts{Direct: true})
	b.Record("K", 30, "BIANZ", &Player{UID: "2"}, RecordOpts{Direct: true})
	fc := FormatCtx{Suffix: "P"}
	var dots []Dot
	out := b.FormatPicks(fc, map[string]int{"AIDIL": 22, "BIANZ": 25}, &dots)
	want := "K:\nAIDIL 20 P\nBIANZ 30.\n\nB:\n-"
	if out != want || len(dots) != 1 || dots[0].P.Name != "BIANZ" {
		t.Fatalf("%q dots=%d", out, len(dots))
	}
	// pin tidak terbaca: semua bertitik
	if got := b.FormatPicks(fc, nil, nil); got != "K:\nAIDIL 20.\nBIANZ 30.\n\nB:\n-" {
		t.Fatalf("%q", got)
	}
	// override .p / .c dan .svown
	b.Over = map[string]string{"K:BIANZ": "P"}
	if got := b.FormatPicks(fc, nil, nil); !strings.Contains(got, "BIANZ 30 P") {
		t.Fatalf(".p: %q", got)
	}
	if got := b.FormatPicks(FormatCtx{Suffix: "P", Svown: map[string]bool{"1": true}}, nil, nil); !strings.Contains(got, "AIDIL 20 P") {
		t.Fatalf(".svown: %q", got)
	}
}

func TestKkAndReset(t *testing.T) {
	def := KKCfg{K: &KKSide{Name: "ALBOTZ", Amount: 15}, B: &KKSide{Name: "KWINGZ", Amount: 15}}
	b := newBook()
	b.KK = true
	b.Record("K", 40, "AIDIL", &Player{UID: "1"}, RecordOpts{Direct: true, KK: def})
	if len(b.K) != 2 || !b.K[0].KK || b.K[0].Name != "ALBOTZ" || len(b.B) != 1 {
		t.Fatalf("kk: K=%d B=%d", len(b.K), len(b.B))
	}
	if !b.HasPicks() {
		t.Fatal("ada pik pemain")
	}
	nb := b.Reset(def)
	if nb == nil || !nb.Started || !nb.KK || len(nb.K) != 1 || nb.K[0].Name != "ALBOTZ" {
		t.Fatalf("reset: %+v", nb)
	}
	nb.KK = false
	nb.RemoveKk()
	if len(nb.K) != 0 || len(nb.B) != 0 {
		t.Fatal("RemoveKk")
	}
	// .rs pada grup yang tidak aktif dan tanpa pengaturan -> buku dihapus
	if (&Book{K: []*Pick{{Name: "A", Amount: 1}}}).Reset(def) != nil {
		t.Fatal("seharusnya nil")
	}
}

func TestParseKK(t *testing.T) {
	if s := ParseKK("kwingz 20", "ALBOTZ 15"); s.Name != "KWINGZ" || s.Amount != 20 {
		t.Fatalf("%+v", s)
	}
	if s := ParseKK("rusak", "ALBOTZ 15"); s.Name != "ALBOTZ" || s.Amount != 15 {
		t.Fatalf("%+v", s)
	}
}

func TestJSONKeepsUnknownFields(t *testing.T) {
	in := `{"K":[{"name":"A","amount":5,"uid":"1","ids":[7,8],"pmid":9}],"B":[],"started":true,"fieldBaru":{"x":1},"rk":true}`
	var b Book
	if err := json.Unmarshal([]byte(in), &b); err != nil {
		t.Fatal(err)
	}
	if !b.Started || !b.RK || b.K[0].PMID != 9 || len(b.K[0].IDs) != 2 {
		t.Fatalf("%+v", b)
	}
	out, err := json.Marshal(&b)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(out, &m)
	if string(m["fieldBaru"]) != `{"x":1}` || string(m["rk"]) != "true" {
		t.Fatalf("hasil: %s", out)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := OpenStore(dir)
	s.Edit("-1001234", func(b *Book) { b.Started = true; b.Record("K", 5, "A", &Player{UID: "1"}, RecordOpts{Direct: true}) })
	s.Flush()
	s2 := OpenStore(dir)
	s2.View("1234", func(b *Book) { // -100 dan - dibuang: kunci sama
		if b == nil || !b.Started || len(b.K) != 1 {
			t.Fatalf("data tidak terbaca ulang: %+v", b)
		}
	})
}
