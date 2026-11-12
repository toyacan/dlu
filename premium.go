package tg

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Premium mengubah emoji biasa di pesan & tombol menjadi emoji premium (custom emoji), setara premium.js.
type Premium struct {
	pairs   [][2]string // emoji, id; terpanjang dulu
	byEmoji map[string]string
	re      *regexp.Regexp
}

// NewPremium mengembalikan nil bila tidak ada pasangan (fitur mati).
func NewPremium(pairs [][2]string) *Premium {
	by := map[string]string{}
	for _, kv := range pairs {
		by[kv[0]] = kv[1]
	}
	if len(by) == 0 {
		return nil
	}
	p := &Premium{byEmoji: by}
	for e, id := range by {
		p.pairs = append(p.pairs, [2]string{e, id})
	}
	sort.Slice(p.pairs, func(i, j int) bool {
		a, b := p.pairs[i][0], p.pairs[j][0]
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return a < b
	})
	parts := make([]string, len(p.pairs))
	for i, kv := range p.pairs {
		parts[i] = regexp.QuoteMeta(kv[0])
	}
	p.re = regexp.MustCompile(strings.Join(parts, "|"))
	return p
}

// Count: jumlah emoji yang terdaftar.
func (p *Premium) Count() int {
	if p == nil {
		return 0
	}
	return len(p.pairs)
}

var htmlEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Convert: teks polos di-escape dulu (supaya "<id>" tidak dianggap tag), lalu emoji diganti <tg-emoji>.
func (p *Premium) Convert(text string, isHTML bool) string {
	base := text
	if !isHTML {
		base = htmlEsc.Replace(text)
	}
	return p.re.ReplaceAllStringFunc(base, func(e string) string {
		return fmt.Sprintf(`<tg-emoji emoji-id="%s">%s</tg-emoji>`, p.byEmoji[e], e)
	})
}

func (p *Premium) fixButton(b Button) Button {
	if b.IconCustomEmojiID != "" {
		return b
	}
	for _, kv := range p.pairs {
		if strings.HasPrefix(b.Text, kv[0]) {
			rest := strings.TrimLeftFunc(b.Text[len(kv[0]):], unicode.IsSpace)
			if rest != "" {
				b.Text = rest
				b.IconCustomEmojiID = kv[1]
				return b
			}
		}
	}
	return b
}

func (p *Premium) fixRows(rows [][]Button) [][]Button {
	out := make([][]Button, len(rows))
	for i, r := range rows {
		nr := make([]Button, len(r))
		for j, b := range r {
			nr[j] = p.fixButton(b)
		}
		out[i] = nr
	}
	return out
}

// FixMarkup: emoji di awal teks tombol dipindah ke icon_custom_emoji_id.
func (p *Premium) FixMarkup(m any) any {
	switch v := m.(type) {
	case InlineMarkup:
		return InlineMarkup{InlineKeyboard: p.fixRows(v.InlineKeyboard)}
	case ReplyMarkup:
		v.Keyboard = p.fixRows(v.Keyboard)
		return v
	}
	return m
}

// Prep menyiapkan teks+opsi versi emoji premium. ok=false bila dilewati (mati / Markdown).
func (p *Premium) Prep(text string, o SendOpts) (string, SendOpts, bool) {
	if p == nil {
		return "", o, false
	}
	pm := strings.ToUpper(o.ParseMode)
	if pm != "" && pm != "HTML" {
		return "", o, false
	}
	no := o
	no.ParseMode = "HTML"
	no.Markup = p.FixMarkup(o.Markup)
	return p.Convert(text, o.ParseMode != ""), no, true
}
