package cmdmenu

import "cu/internal/tg"

func MainKeyboard() tg.InlineMarkup {
	btn := func(k string) tg.Button { return tg.Button{Text: Cats[k].Label, CallbackData: "cmd:" + k} }
	return tg.InlineMarkup{InlineKeyboard: [][]tg.Button{
		{btn("set"), btn("list")},
		{btn("pin"), btn("help")},
	}}
}

func BackKeyboard() tg.InlineMarkup {
	return tg.InlineMarkup{InlineKeyboard: [][]tg.Button{{{Text: BackLabel, CallbackData: "cmd:menu"}}}}
}

// View: pesan (teks + tombol) untuk data callback "cmd:<kunci>"; ok=false bila kunci tidak dikenal.
func View(key string) (text string, markup tg.InlineMarkup, ok bool) {
	if key == "menu" {
		return MainText, MainKeyboard(), true
	}
	c, found := Cats[key]
	if !found {
		return "", tg.InlineMarkup{}, false
	}
	return c.Text, BackKeyboard(), true
}
