package manager

import (
	"context"
	"testing"
)

type fake struct{ running, session bool }

func (f *fake) HasSession() bool                    { return f.session }
func (f *fake) IsRunning() bool                     { return f.running }
func (f *fake) Start(context.Context) error         { f.running = true; return nil }
func (f *fake) StartSession(context.Context, string) (string, error) {
	f.running, f.session = true, true
	return "tes", nil
}
func (f *fake) SaveSession(string) error { f.session = true; return nil }
func (f *fake) Stop()                               { f.running = false }
func (f *fake) LogoutRemote(context.Context) error  { return nil }
func (f *fake) ClearSession()                       { f.session = false }

func newTest(t *testing.T) *Manager {
	m, err := New(Config{DataDir: t.TempDir(), OwnerID: "1", TrialDays: 1},
		func(string) Userbot { return &fake{} })
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAddAndExpire(t *testing.T) {
	m := newTest(t)
	ctx := context.Background()
	if m.IsAllowed("2") {
		t.Fatal("pengguna baru seharusnya belum punya akses")
	}
	m.AddUser(ctx, "2", 3, "budi")
	if !m.IsAllowed("2") {
		t.Fatal("seharusnya aktif")
	}
	if !m.IsAllowed("1") {
		t.Fatal("owner selalu boleh")
	}
}

func TestExtendAddsToRemaining(t *testing.T) {
	m := newTest(t)
	ctx := context.Background()
	u1 := m.AddUser(ctx, "2", 5, "")
	u2 := m.ExtendUser(ctx, "2", 5, "")
	if *u2.Expires <= *u1.Expires {
		t.Fatal("perpanjang harus menambah ke sisa masa aktif")
	}
	perm := m.AddUser(ctx, "3", 0, "")
	if perm.Expires != nil {
		t.Fatal("days=0 harus permanen")
	}
	after := m.ExtendUser(ctx, "3", 5, "")
	if after.Expires != nil {
		t.Fatal("pengguna permanen tidak boleh berubah")
	}
}

func TestTrialOncePerAccountUntilReset(t *testing.T) {
	m := newTest(t)
	ctx := context.Background()
	if r := m.ClaimTrial(ctx, "9", "x"); !r.OK {
		t.Fatalf("klaim pertama harus berhasil: %+v", r)
	}
	if r := m.ClaimTrial(ctx, "9", "x"); r.OK || r.Reason != "active" {
		t.Fatalf("saat masih aktif: %+v", r)
	}
	if r := m.ClaimTrial(ctx, "1", "owner"); r.Reason != "owner" {
		t.Fatalf("owner: %+v", r)
	}
	if n := m.ResetTrial("9"); n != 1 {
		t.Fatalf("reset: %d", n)
	}
}

func TestDelUserClearsSession(t *testing.T) {
	m := newTest(t)
	ctx := context.Background()
	m.AddUser(ctx, "5", 1, "")
	b := m.Bot("5").(*fake)
	b.session = true
	if !m.DelUser(ctx, "5") {
		t.Fatal("seharusnya ada")
	}
	if b.session || m.IsAllowed("5") {
		t.Fatal("sesi dan akses harus hilang")
	}
}

func TestPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	f := func(string) Userbot { return &fake{} }
	m1, _ := New(Config{DataDir: dir, OwnerID: "1"}, f)
	m1.AddUser(context.Background(), "7", 2, "ani")
	m2, _ := New(Config{DataDir: dir, OwnerID: "1"}, f)
	if !m2.IsAllowed("7") {
		t.Fatal("data harus terbaca ulang dari users.json")
	}
}
