package slot

import (
	"encoding/json"
	"strings"
)

type Pick struct {
	Name   string `json:"name"`
	Amount int    `json:"amount"`
	UID    string `json:"uid,omitempty"`
	AH     string `json:"ah,omitempty"`
	KK     bool   `json:"kk,omitempty"`
	IDs    []int  `json:"ids,omitempty"`
	PMID   int    `json:"pmid,omitempty"`
}

type Owner struct {
	UID string `json:"uid"`
	AH  string `json:"ah,omitempty"`
}

type KKSide struct {
	Name   string `json:"name"`
	Amount int    `json:"amount"`
}

type KKCfg struct {
	K *KKSide `json:"K,omitempty"`
	B *KKSide `json:"B,omitempty"`
}

// Book: data satu chat. Bidang yang belum dikenal (mis. ditambah versi Node) dipertahankan apa adanya.
type Book struct {
	K       []*Pick           `json:"K"`
	B       []*Pick           `json:"B"`
	Started bool              `json:"started,omitempty"`
	Off     bool              `json:"off,omitempty"`
	Closed  map[string]bool   `json:"closed,omitempty"`
	RK      bool              `json:"rk,omitempty"`
	KK      bool              `json:"kk,omitempty"`
	KKCfg   *KKCfg            `json:"kkCfg,omitempty"`
	Alert   bool              `json:"alert,omitempty"`
	Perak   bool              `json:"perak,omitempty"`
	AutoRS  bool              `json:"autors,omitempty"`
	Owners  map[string]*Owner `json:"owners,omitempty"`
	Marked  map[string]any    `json:"marked,omitempty"`
	Over    map[string]string `json:"over,omitempty"` // "K:NAMA" -> "P" (paksa P) | "C" (tanpa titik)

	extra map[string]json.RawMessage
}

var knownBookKeys = map[string]bool{
	"K": true, "B": true, "started": true, "off": true, "closed": true, "rk": true, "kk": true,
	"kkCfg": true, "alert": true, "perak": true, "autors": true, "owners": true, "marked": true, "over": true,
}

type bookAlias Book

func (b *Book) UnmarshalJSON(data []byte) error {
	var a bookAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*b = Book(a)
	b.extra = map[string]json.RawMessage{}
	for k, v := range raw {
		if !knownBookKeys[k] {
			b.extra[k] = v
		}
	}
	return nil
}

func (b Book) MarshalJSON() ([]byte, error) {
	if b.K == nil {
		b.K = []*Pick{}
	}
	if b.B == nil {
		b.B = []*Pick{}
	}
	main, err := json.Marshal(bookAlias(b))
	if err != nil {
		return nil, err
	}
	if len(b.extra) == 0 {
		return main, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(main, &m); err != nil {
		return nil, err
	}
	for k, v := range b.extra {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// Defaults .kk on (pemain tetap), dari .env KK_K / KK_B ("NAMA JUMLAH").
type Defaults struct {
	KK KKCfg
}

func (b *Book) Side(side string) *[]*Pick {
	if side == "K" {
		return &b.K
	}
	return &b.B
}

func opposite(side string) string {
	if side == "K" {
		return "B"
	}
	return "K"
}

func total(a []*Pick) int {
	n := 0
	for _, p := range a {
		n += p.Amount
	}
	return n
}

func (b *Book) hasKk(list []*Pick) bool {
	for _, p := range list {
		if p.KK {
			return true
		}
	}
	return false
}

// ApplyKk: bila .kk on, pastikan pemain tetap ada di depan tiap sisi.
func (b *Book) ApplyKk(def KKCfg) {
	if b == nil || !b.KK {
		return
	}
	cfg := def
	if b.KKCfg != nil {
		if b.KKCfg.K != nil {
			cfg.K = b.KKCfg.K
		}
		if b.KKCfg.B != nil {
			cfg.B = b.KKCfg.B
		}
	}
	if !b.hasKk(b.K) && cfg.K != nil {
		b.K = append([]*Pick{{Name: cfg.K.Name, Amount: cfg.K.Amount, KK: true}}, b.K...)
	}
	if !b.hasKk(b.B) && cfg.B != nil {
		b.B = append([]*Pick{{Name: cfg.B.Name, Amount: cfg.B.Amount, KK: true}}, b.B...)
	}
}

// RemoveKk: .kk off.
func (b *Book) RemoveKk() {
	if b == nil {
		return
	}
	for _, side := range []string{"K", "B"} {
		list := b.Side(side)
		var keep []*Pick
		for _, p := range *list {
			if p.KK {
				delete(b.Over, side+":"+NormName(p.Name))
			} else {
				keep = append(keep, p)
			}
		}
		*list = keep
	}
}

// Player: akun pengirim pik (bila terbaca).
type Player struct {
	UID string
	AH  string
}

type RecordOpts struct {
	Direct bool // pik langsung "k50" (bukan pesan konfirmasi "Pik:")
	Force  bool // .sk / .sb: paksa catat walau sisi ditutup / aturan .rk
	MsgIDs []int
	PMID   int
	KK     KKCfg // bawaan KK_K / KK_B
}

// Alasan penolakan Record.
const (
	RejectClosed = "closed" // sisi ditutup (.ck/.cb)
	RejectRK     = "rk"     // setelah .rk, sisi ini sudah lebih besar
	RejectOwner  = "owner"  // nama terikat ke akun lain dan pengirim tidak terbaca
)

func (b *Book) refused(side string, force bool) string {
	if force {
		return ""
	}
	if b.Closed != nil && b.Closed[side] {
		return RejectClosed
	}
	if b.RK && total(*b.Side(side)) > total(*b.Side(opposite(side))) {
		return RejectRK
	}
	return ""
}

// Record mencatat satu pik. Mengembalikan (true, "") bila tercatat, atau (false, alasan).
// name harus sudah huruf besar bila berasal dari konfirmasi "Pik:".
func (b *Book) Record(side string, amount int, name string, player *Player, o RecordOpts) (bool, string) {
	name = CleanName(name)
	if why := b.refused(side, o.Force); why != "" {
		return false, why
	}
	b.ApplyKk(o.KK)

	// Anti-clone (ketat): nama terikat ke satu akun tetapi pengirim tidak terbaca -> ditolak.
	var ownTop *Owner
	if b.Owners != nil {
		ownTop = b.Owners[Nick(name)]
	}
	if ownTop != nil && player == nil {
		return false, RejectOwner
	}
	cloneNow := player != nil && ownTop != nil && ownTop.UID != player.UID

	// Pemain yang sama pik di sisi lawan -> pindah (slot lama dikosongkan).
	opp := opposite(side)
	same := func(p *Pick) bool {
		if p.KK {
			return false
		}
		if player != nil {
			return p.UID == player.UID || (p.UID == "" && p.Name == name && !cloneNow)
		}
		return p.Name == name
	}
	oppList := b.Side(opp)
	var keep, gone []*Pick
	for _, p := range *oppList {
		if same(p) {
			gone = append(gone, p)
		} else {
			keep = append(keep, p)
		}
	}
	if len(gone) > 0 {
		*oppList = keep
		for _, p := range gone {
			delete(b.Over, opp+":"+NormName(p.Name))
		}
	}

	// Akun yang sama di sisi ini = pemain yang sama; kalau tidak, cocokkan nama.
	list := b.Side(side)
	var found *Pick
	if player != nil {
		for _, p := range *list {
			if !p.KK && p.UID != "" && p.UID == player.UID {
				found = p
				break
			}
		}
	}
	if found == nil {
		for _, p := range *list {
			if p.KK || CleanName(p.Name) != name {
				continue
			}
			ok := !cloneNow
			if p.UID != "" {
				ok = player == nil || p.UID == player.UID
			}
			if ok {
				found = p
				break
			}
		}
	}
	if found != nil {
		found.Amount = amount
		if !o.Direct {
			found.Name = name // nama dari konfirmasi "Pik:" lebih akurat
		}
	} else {
		found = &Pick{Name: name, Amount: amount}
		*list = append(*list, found)
	}

	if player != nil {
		found.UID = player.UID
		if player.AH != "" {
			found.AH = player.AH
		}
		// akun pertama yang memakai nama ini jadi pemilik nama
		if nk := Nick(name); nk != "" {
			if b.Owners == nil {
				b.Owners = map[string]*Owner{}
			}
			if ow := b.Owners[nk]; ow == nil {
				b.Owners[nk] = &Owner{UID: player.UID, AH: player.AH}
			} else if ow.UID == player.UID && player.AH != "" && ow.AH == "" {
				ow.AH = player.AH
			}
		}
	}

	// id pesan (dipakai .p / .c), unik, simpan 20 terakhir
	seen := map[int]bool{}
	var ids []int
	for _, id := range append(append([]int{}, found.IDs...), o.MsgIDs...) {
		if id != 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > 20 {
		ids = ids[len(ids)-20:]
	}
	found.IDs = ids
	if o.PMID != 0 {
		found.PMID = o.PMID
	}
	return true, ""
}

// HasPicks: ada pik pemain sungguhan (bukan pemain tetap .kk).
func (b *Book) HasPicks() bool {
	if b == nil {
		return false
	}
	for _, p := range append(append([]*Pick{}, b.K...), b.B...) {
		if !p.KK {
			return true
		}
	}
	return false
}

// Reset (.rs): kembalikan data ronde baru. nil = tidak ada yang perlu disimpan (buku dihapus).
// Status .on/.off, .kk, .alert, .perak, owners, dll tetap; pik, sisi tertutup, aturan .rk, dan .p/.c dibersihkan.
func (b *Book) Reset(def KKCfg) *Book {
	if b == nil {
		return nil
	}
	wasOn := b.Started && !b.Off // .rs tidak boleh mengaktifkan grup
	if !(wasOn || b.Off || b.KK || b.Alert || b.Perak || b.AutoRS || b.KKCfg != nil || len(b.Owners) > 0 || len(b.Marked) > 0) {
		return nil
	}
	nb := &Book{
		K: []*Pick{}, B: []*Pick{},
		Started: wasOn, Off: b.Off, Owners: b.Owners, Marked: b.Marked,
		KK: b.KK, Alert: b.Alert, Perak: b.Perak, AutoRS: b.AutoRS, KKCfg: b.KKCfg,
		extra: b.extra,
	}
	nb.ApplyKk(def)
	return nb
}

// Recording: grup sedang aktif (.on) dan tidak .off.
func (b *Book) Recording() bool { return b != nil && b.Started && !b.Off }

// ParseKK membaca "NAMA JUMLAH" (mis. dari .env KK_K); cadangan dipakai bila tidak valid.
func ParseKK(v, def string) KKSide {
	try := func(s string) (KKSide, bool) {
		s = strings.TrimSpace(s)
		i := strings.LastIndexAny(s, " \t")
		if i <= 0 {
			return KKSide{}, false
		}
		n := 0
		for _, r := range s[i+1:] {
			if r < '0' || r > '9' {
				return KKSide{}, false
			}
			n = n*10 + int(r-'0')
		}
		name := strings.ToUpper(strings.TrimSpace(s[:i]))
		if name == "" || s[i+1:] == "" {
			return KKSide{}, false
		}
		return KKSide{Name: name, Amount: n}, true
	}
	if r, ok := try(v); ok {
		return r
	}
	r, _ := try(def)
	return r
}
