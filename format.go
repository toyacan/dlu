package slot

import (
	"fmt"
	"strings"
)

type FormatCtx struct {
	Suffix   string            // LIST_SUFFIX, bawaan "P"
	Contacts map[string]string // uid -> nama kontak hasil .sv
	Svown    map[string]bool   // uid yang selalu bertanda P (.svown)
	KK       KKCfg             // bawaan KK_K / KK_B
}

// Dot: pemain bertanda titik (bukan P) di .list.
type Dot struct {
	Side string
	P    *Pick
}

// FormatPicks menyusun .list. players = hasil ParsePinPlayers (nil bila pin tidak terbaca).
// Tanda P = nama ada di pin dan jumlah pin >= jumlah pik; selain itu diberi titik. Kosong -> "".
func (b *Book) FormatPicks(fc FormatCtx, players map[string]int, dots *[]Dot) string {
	if b == nil {
		return ""
	}
	b.ApplyKk(fc.KK)
	if len(b.K) == 0 && len(b.B) == 0 {
		return ""
	}
	withP := func(name string, amount int) string {
		return strings.TrimSpace(fmt.Sprintf("%s %d %s", name, amount, fc.Suffix))
	}
	dotted := func(name string, amount int) string { return fmt.Sprintf("%s %d.", name, amount) }

	lineRaw := func(p *Pick, side string) string {
		if p.KK { // pemain tetap .kk: selalu P
			return withP(CleanName(p.Name), p.Amount)
		}
		raw := p.Name
		if p.UID != "" {
			if cn, ok := fc.Contacts[p.UID]; ok {
				raw = strings.ToUpper(cn)
			}
		}
		shown := CleanName(raw)
		ov := b.Over[side+":"+NormName(p.Name)]
		own := b.Owners[Nick(p.Name)]
		// Anti-clone: nama sama dengan pemilik tapi ID berbeda -> titik, .p/.c tidak berlaku
		if ov != "" && own != nil && p.UID != "" && own.UID != p.UID {
			return dotted(shown, p.Amount)
		}
		if ov == "P" {
			return withP(shown, p.Amount)
		}
		if ov == "C" {
			return fmt.Sprintf("%s %d", shown, p.Amount)
		}
		if p.UID != "" && fc.Svown[p.UID] { // .svown: selalu P
			return withP(shown, p.Amount)
		}
		if own != nil && p.UID != "" && own.UID != p.UID {
			return dotted(shown, p.Amount)
		}
		if own != nil && p.UID == "" { // nama terikat ke satu akun tapi pik ini tanpa ID: tak terbukti
			return dotted(shown, p.Amount)
		}
		if players == nil { // pin kosong / tidak terbaca: tetap masuk list, titik, tanpa P
			return dotted(shown, p.Amount)
		}
		if have, ok := PinHave(players, p.Name, p.UID, fc.Contacts); ok && have >= p.Amount {
			return withP(shown, p.Amount)
		}
		return dotted(shown, p.Amount)
	}
	line := func(p *Pick, side string) string {
		out := lineRaw(p, side)
		if dots != nil && strings.HasSuffix(out, ".") {
			*dots = append(*dots, Dot{Side: side, P: p})
		}
		return out
	}
	fmtSide := func(arr []*Pick, side string) string {
		if len(arr) == 0 {
			return "-"
		}
		lines := make([]string, len(arr))
		for i, p := range arr {
			lines[i] = line(p, side)
		}
		return strings.Join(lines, "\n")
	}
	return "K:\n" + fmtSide(b.K, "K") + "\n\nB:\n" + fmtSide(b.B, "B")
}

// FormatRk menyusun rekap .rk: total K dan B, selisih, dan saldo seharusnya. Kosong -> "".
func (b *Book) FormatRk(def KKCfg) string {
	if b == nil {
		return ""
	}
	b.ApplyKk(def)
	if len(b.K) == 0 && len(b.B) == 0 {
		return ""
	}
	nums := func(a []*Pick) string {
		if len(a) == 0 {
			return "0"
		}
		s := make([]string, len(a))
		for i, p := range a {
			s[i] = fmt.Sprint(p.Amount)
		}
		return strings.Join(s, ", ")
	}
	k, bb := total(b.K), total(b.B)
	lockK, lockB := "", ""
	if k > bb { // 🔒 di sisi yang lebih besar (patokan); seimbang = tanpa gembok
		lockK = "🔒 "
	}
	if bb > k {
		lockB = "🔒 "
	}
	diff := ""
	if bb < k {
		diff = fmt.Sprintf("🐠 B kurang %d untuk samakan K\n\n", k-bb)
	} else if k < bb {
		diff = fmt.Sprintf("🐠 K kurang %d untuk samakan B\n\n", bb-k)
	}
	return fmt.Sprintf("⭐ %sK: %s = %d\n\n⭐ %sB: %s = %d\n\n%s💰 Saldo seharusnya: %d K",
		lockK, nums(b.K), k, lockB, nums(b.B), bb, diff, k+bb)
}
