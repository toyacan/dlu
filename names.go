// Package slot: inti pencatatan pik K/B (port dari ubot.js): parsing pesan, daftar .list, rekap .rk,
// aturan sisi tertutup / setelah .rk / anti-clone, dan penyimpanan picks.json. Tanpa dependensi Telegram,
// jadi bisa dites murni. Format picks.json sama dengan versi Node.
package slot

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

var (
	pickRe       = regexp.MustCompile(`(?i)^\W*Pik:\s*([KB])\s*(\d+)\s+(.+?)\s*$`)
	directRe     = regexp.MustCompile(`(?i)^(?:\s|\p{Zs})*([KB])(?:\s|\p{Zs})*(\d+)`)
	directPerak  = regexp.MustCompile(`(?i)^(?:\s|\p{Zs})*([KB])(?:\s|\p{Zs})*(\d+(?:[.,]\d+)*)(k|rb|ribu)?`)
	directAllRe  = regexp.MustCompile(`(?i)^\s*([KB])\s*ALL\s*$`)
	tagStripRe   = regexp.MustCompile(`[\s\-_.:|,]*\d{1,3}\s*[/\\#\x{2044}\x{2215}\x{FF0F}]\s*\d{1,3}`)
	multiSpaceRe = regexp.MustCompile(`\s{2,}`)
	trailPunctRe = regexp.MustCompile(`[\-_.:|,]+$`)
	thousandsRe  = regexp.MustCompile(`^\d{1,3}(\.\d{3})+$`)
	nonAlnumRe   = regexp.MustCompile(`[^A-Z0-9]`)
	pemainRe     = regexp.MustCompile(`(?i)PEMAIN`)
	pinLineRe    = regexp.MustCompile(`^\s*([^\d\n]+?)\s*[-:=]?\s*(\d+)\s*$`)
)

// PerakLiteralMin: bilangan bulat >= 100 dipakai apa adanya pada mode .perak.
const PerakLiteralMin = 100

// NormID menyamakan format ID chat (hilangkan awalan -100 / -).
func NormID(id string) string {
	id = strings.TrimPrefix(id, "-100")
	return strings.TrimPrefix(id, "-")
}

// CleanName: buang penanda "00/00" lalu ambil nama depan ("CAPO 19/20" -> "CAPO").
func CleanName(n string) string {
	base := strings.TrimSpace(multiSpaceRe.ReplaceAllString(tagStripRe.ReplaceAllString(n, ""), " "))
	first := n
	if f := strings.Fields(base); len(f) > 0 {
		first = f[0]
	}
	if out := trailPunctRe.ReplaceAllString(first, ""); out != "" {
		return out
	}
	return n
}

// Nick: kunci pencocokan nama (tanpa 00/00, nama belakang, spasi, simbol).
func Nick(n string) string { return nonAlnumRe.ReplaceAllString(strings.ToUpper(CleanName(n)), "") }

// NormName: seperti Nick tetapi tanpa CleanName.
func NormName(n string) string { return nonAlnumRe.ReplaceAllString(strings.ToUpper(n), "") }

func nextIsLetter(s string) bool {
	for _, r := range s {
		return unicode.IsLetter(r)
	}
	return false
}

// ParsePick: baris pertama pesan konfirmasi "✅ Pik: K 30 kazel".
func ParsePick(text string) (side string, amount int, name string, ok bool) {
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	m := pickRe.FindStringSubmatch(first)
	if m == nil {
		return "", 0, "", false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, "", false
	}
	return strings.ToUpper(m[1]), n, strings.ToUpper(strings.TrimSpace(m[3])), true
}

// ParseDirect: pik langsung dari pemain, "k50" / "K 50 cek" (huruf/angka tepat setelah jumlah = bukan pik).
func ParseDirect(text string) (side string, amount int, ok bool) {
	loc := directRe.FindStringSubmatchIndex(text)
	if loc == nil || nextIsLetter(text[loc[1]:]) {
		return "", 0, false
	}
	n, err := strconv.Atoi(text[loc[4]:loc[5]])
	if err != nil {
		return "", 0, false
	}
	return strings.ToUpper(text[loc[2]:loc[3]]), n, true
}

// ParsePerakText: pik langsung pada mode .perak ("k10,9", "k1k", "k300").
func ParsePerakText(text string) (side string, amount int, ok bool) {
	loc := directPerak.FindStringSubmatchIndex(text)
	if loc == nil || nextIsLetter(text[loc[1]:]) {
		return "", 0, false
	}
	suffix := ""
	if loc[6] >= 0 {
		suffix = text[loc[6]:loc[7]]
	}
	v := ParsePerak(text[loc[4]:loc[5]], suffix)
	if v <= 0 {
		return "", 0, false
	}
	return strings.ToUpper(text[loc[2]:loc[3]]), v, true
}

// ParseAll: "kall" / "ball" / "k all".
func ParseAll(text string) (side string, ok bool) {
	m := directAllRe.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return strings.ToUpper(m[1]), true
}

// ParsePerak: jumlah dalam ribuan. k10=10000, k10,9=10900, k3,6=3600, k1k=1000, k1000=1000, k300=300.
func ParsePerak(numStr, suffix string) int {
	var v float64
	decimal := false
	if thousandsRe.MatchString(numStr) {
		n, _ := strconv.Atoi(strings.ReplaceAll(numStr, ".", ""))
		v = float64(n)
	} else {
		decimal = strings.ContainsAny(numStr, ".,")
		f, err := strconv.ParseFloat(strings.Replace(numStr, ",", ".", 1), 64)
		if err != nil {
			return 0
		}
		v = f
	}
	if !(v > 0) {
		return 0
	}
	if suffix != "" || decimal {
		return int(roundHalfUp(v * 1000))
	}
	if v < PerakLiteralMin {
		return int(v * 1000)
	}
	return int(v)
}

func roundHalfUp(f float64) float64 {
	if f < 0 {
		return -roundHalfUp(-f)
	}
	return float64(int64(f + 0.5))
}

// ParsePinPlayers membaca daftar pemain dari teks pin: { NICK -> jumlah }. nil bila kosong.
// Baris header ("GAME 1 : K 2-0 57") diabaikan; bila ada baris "PEMAIN : (n)", pembacaan mulai setelahnya.
func ParsePinPlayers(text string) map[string]int {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if pemainRe.MatchString(l) {
			lines = lines[i+1:]
			break
		}
	}
	players := map[string]int{}
	for _, l0 := range lines {
		l := tagStripRe.ReplaceAllString(l0, "")
		m := pinLineRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		key := Nick(m[1])
		if key == "" {
			continue
		}
		amt, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		if amt > players[key] {
			players[key] = amt
		}
	}
	if len(players) == 0 {
		return nil
	}
	return players
}

// PinHave: saldo pin satu pemain, dicari lewat nama pik DAN nama kontak (uid). Dipakai yang terbesar.
func PinHave(players map[string]int, name, uid string, contacts map[string]string) (int, bool) {
	if players == nil {
		return 0, false
	}
	keys := map[string]bool{}
	if k := Nick(name); k != "" {
		keys[k] = true
	}
	if uid != "" {
		if cn, ok := contacts[uid]; ok {
			if k := Nick(cn); k != "" {
				keys[k] = true
			}
		}
	}
	best, found := 0, false
	for k := range keys {
		if v, ok := players[k]; ok && (!found || v > best) {
			best, found = v, true
		}
	}
	return best, found
}

func u16len(s string) int { return len(utf16.Encode([]rune(s))) }

// ChunkLines memecah teks per baris agar tiap potongan <= max satuan UTF-16 (batas pesan Telegram).
func ChunkLines(text string, max int) []string {
	if max <= 0 {
		max = 3800
	}
	var out []string
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		if u16len(cur+"\n"+line) > max && cur != "" {
			out = append(out, cur)
			cur = line
		} else if cur != "" {
			cur += "\n" + line
		} else {
			cur = line
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
