package bot

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cu/internal/cmdmenu"
	"cu/internal/rolls"
	"cu/internal/tg"
	"cu/internal/util"
)

// Pengaman login: proses kedaluwarsa, batas salah kode/password, dan batas permintaan kode OTP
// (supaya bot tidak bisa dipakai membanjiri nomor orang lain dengan kode login).
const (
	loginTTL         = 10 * time.Minute
	maxTries         = 5
	otpPerHourUser   = 5
	otpPerHourPhone  = 3
)

func (b *Bot) getLogin(id string) *loginState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.logins[id]
}

func (b *Bot) resetLogin(id string) {
	b.mu.Lock()
	l := b.logins[id]
	delete(b.logins, id)
	b.mu.Unlock()
	if l != nil && l.flow != nil {
		l.flow.Cancel()
	}
}

func (b *Bot) otpAllowed(userKey, phoneKey string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	hit := func(k string, max int) bool {
		var keep []time.Time
		for _, t := range b.otpLog[k] {
			if now.Sub(t) < time.Hour {
				keep = append(keep, t)
			}
		}
		b.otpLog[k] = keep
		return len(keep) >= max
	}
	return !hit(userKey, otpPerHourUser) && !hit(phoneKey, otpPerHourPhone)
}

func (b *Bot) otpRecord(userKey, phoneKey string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.otpLog[userKey] = append(b.otpLog[userKey], now)
	b.otpLog[phoneKey] = append(b.otpLog[phoneKey], now)
}

// StartHousekeeping: bersihkan proses login yang ditinggalkan dan catatan OTP lama.
func (b *Bot) StartHousekeeping(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				var stale []string
				b.mu.Lock()
				for id, l := range b.logins {
					if time.Since(l.startedAt) > loginTTL {
						stale = append(stale, id)
					}
				}
				for k, arr := range b.otpLog {
					var keep []time.Time
					for _, x := range arr {
						if time.Since(x) < time.Hour {
							keep = append(keep, x)
						}
					}
					if len(keep) > 0 {
						b.otpLog[k] = keep
					} else {
						delete(b.otpLog, k)
					}
				}
				b.mu.Unlock()
				for _, id := range stale {
					b.resetLogin(id)
				}
			}
		}
	}()
}

func (b *Bot) startConnect(ctx context.Context, m *tg.Message) {
	chat := m.Chat.ID
	id := util.Itoa(m.From.ID)
	if b.ownerNum == 0 {
		_, _ = b.send(ctx, chat, "OWNER_ID belum diisi di .env.\nAdmin: ketik /id, isi OWNER_ID dengan angka itu, lalu restart.")
		return
	}
	if !b.mgr.IsAllowed(id) {
		_, _ = b.send(ctx, chat, "⛔ Anda belum punya akses atau masa aktif habis.\nCoba gratis dengan /trial atau beli langganan dengan /buy, lalu /connect.")
		return
	}
	b.resetLogin(id)
	b.mu.Lock()
	b.logins[id] = &loginState{state: "phone", startedAt: time.Now()}
	b.mu.Unlock()
	if b.mgr.Info(id).HasSession {
		_, _ = b.send(ctx, chat, "🔄 Hubungkan Ulang uBot\n\nKamu akan login ulang dengan OTP baru.")
	} else {
		_, _ = b.send(ctx, chat, "🔌 Hubungkan uBot\n\nKamu akan login dengan nomor HP dan OTP.")
	}
	_, _ = b.send(ctx, chat, "📱 Tekan tombol Kirim nomor HP di bawah, atau ketik manual: +628xxx\n\nBatalkan dengan /cancel.",
		tg.SendOpts{Markup: tg.ReplyMarkup{
			Keyboard:        [][]tg.Button{{{Text: "📱 Kirim nomor HP", RequestContact: true}}},
			ResizeKeyboard:  true,
			OneTimeKeyboard: true,
		}})
}

func (b *Bot) logout(ctx context.Context, m *tg.Message) {
	id := util.Itoa(m.From.ID)
	b.resetLogin(id)
	ub := b.mgr.Bot(id)
	// akhiri sesi di server Telegram dulu (selagi userbot masih tersambung), baru dimatikan & dihapus
	ended := ub.LogoutRemote(ctx) == nil && ub.HasSession()
	b.mgr.StopUser(id)
	ub.ClearSession()
	if ended {
		_, _ = b.send(ctx, m.Chat.ID, "🔌 Userbot dimatikan, sesi diakhiri di Telegram, dan data sesi dihapus dari server.")
	} else {
		_, _ = b.send(ctx, m.Chat.ID, "🔌 Userbot dimatikan dan data sesi dihapus dari server.\n⚠️ Sesi di Telegram belum bisa diakhiri otomatis. Akhiri manual: Pengaturan → Perangkat → Akhiri sesi lain.")
	}
}

func (b *Bot) finishLogin(ctx context.Context, chatID int64, id, session string) {
	ub := b.mgr.Bot(id)
	if err := ub.SaveSession(session); err != nil {
		log.Printf("[%s] gagal menyimpan sesi: %v", id, err)
	}
	b.resetLogin(id)
	_, _ = b.send(ctx, chatID, "✅ Login berhasil. Menjalankan userbot...")
	name, err := ub.StartSession(ctx, session)
	if err != nil {
		_, _ = b.send(ctx, chatID, fmt.Sprintf("❌ Gagal menjalankan userbot: %v", err))
		return
	}
	if name == "" {
		name = "akun Anda"
	}
	_, _ = b.send(ctx, chatID, fmt.Sprintf("🚀 Userbot aktif sebagai %s.\nBuka Telegram akun itu lalu ketik .help di chat mana saja.", name))
}

var (
	nonPhoneRe = regexp.MustCompile(`[^\d+]`)
	nonDigitRe = regexp.MustCompile(`\D`)
	rpcCodeRe  = regexp.MustCompile(`[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+`)
)

// rpcCode mengambil kode galat Telegram (mis. PHONE_CODE_INVALID) dari teks galat.
func rpcCode(err error) string {
	s := err.Error()
	if c := rpcCodeRe.FindString(s); c != "" {
		return c
	}
	return s
}

func (b *Bot) handleLogin(ctx context.Context, m *tg.Message, override string) {
	chatID := m.Chat.ID
	id := util.Itoa(m.From.ID)
	l := b.getLogin(id)
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// proses mungkin sudah direset selagi menunggu kunci
	if b.getLogin(id) != l {
		return
	}
	text := strings.TrimSpace(override)
	if override == "" {
		text = strings.TrimSpace(m.Text)
	}
	if time.Since(l.startedAt) > loginTTL {
		b.resetLogin(id)
		_, _ = b.send(ctx, chatID, "⌛ Proses login kedaluwarsa (lebih dari 10 menit). Ketik /connect untuk mengulang.", tg.SendOpts{Markup: tg.Remove})
		return
	}

	var err error
	switch l.state {
	case "phone":
		phone := nonPhoneRe.ReplaceAllString(text, "")
		if len(phone) < 8 {
			_, _ = b.send(ctx, chatID, "Nomor tidak valid. Contoh: +628123456789")
			return
		}
		uKey := "u:" + id
		pKey := "p:" + nonDigitRe.ReplaceAllString(phone, "")
		if !b.otpAllowed(uKey, pKey) {
			b.resetLogin(id)
			_, _ = b.send(ctx, chatID, "⛔ Terlalu banyak permintaan kode untuk akun/nomor ini. Coba lagi dalam 1 jam.", tg.SendOpts{Markup: tg.Remove})
			return
		}
		b.otpRecord(uKey, pKey)
		_, _ = b.send(ctx, chatID, "⏳ Meminta kode OTP...")
		l.flow = b.newLogin()
		if !strings.HasPrefix(phone, "+") {
			phone = "+" + phone
		}
		if err = l.flow.SendCode(ctx, phone); err == nil {
			l.state = "code"
			_, _ = b.send(ctx, chatID,
				"📩 Kode OTP dikirim ke aplikasi Telegram akun itu (chat dari \"Telegram\").\n\n"+
					"⚠️ Kirim kode dengan tanda strip di antara angka, contoh: 1-2-3-4-5\n"+
					"Jika dikirim polos, Telegram akan membatalkan kodenya.",
				tg.SendOpts{Markup: tg.Remove})
		}

	case "code":
		_ = b.api.DeleteMessage(ctx, chatID, m.MessageID)
		code := nonDigitRe.ReplaceAllString(text, "")
		var needPw bool
		var session string
		needPw, session, err = l.flow.SubmitCode(ctx, code)
		if err == nil {
			if needPw {
				l.state = "password"
				_, _ = b.send(ctx, chatID, "🔐 Akun memakai verifikasi 2 langkah. Kirim password 2FA Anda.")
			} else {
				b.finishLogin(ctx, chatID, id, session)
			}
		}

	case "password":
		_ = b.api.DeleteMessage(ctx, chatID, m.MessageID)
		var session string
		session, err = l.flow.SubmitPassword(ctx, text)
		if err == nil {
			b.finishLogin(ctx, chatID, id, session)
		}
	}
	if err == nil {
		return
	}

	code := rpcCode(err)
	log.Printf("[%s] Login error: %s", id, code)
	if code == "PHONE_CODE_INVALID" || code == "PASSWORD_HASH_INVALID" {
		l.tries++
		if l.tries >= maxTries {
			b.resetLogin(id)
			_, _ = b.send(ctx, chatID, fmt.Sprintf("⛔ Salah %d kali. Proses login dibatalkan. Ketik /connect untuk mengulang nanti.", maxTries))
			return
		}
		sisa := maxTries - l.tries
		if code == "PHONE_CODE_INVALID" {
			_, _ = b.send(ctx, chatID, fmt.Sprintf("❌ Kode salah (sisa %d percobaan). Kirim ulang kodenya (format 1-2-3-4-5).", sisa))
		} else {
			_, _ = b.send(ctx, chatID, fmt.Sprintf("❌ Password 2FA salah (sisa %d percobaan). Kirim ulang passwordnya.", sisa))
		}
		return
	}
	b.resetLogin(id)
	switch {
	case code == "PHONE_CODE_EXPIRED":
		_, _ = b.send(ctx, chatID, "⌛ Kode kedaluwarsa. Ketik /connect untuk mengulang.")
	case code == "PHONE_NUMBER_INVALID":
		_, _ = b.send(ctx, chatID, "❌ Nomor HP tidak valid. Ketik /connect untuk mengulang.")
	case strings.HasPrefix(code, "FLOOD"):
		_, _ = b.send(ctx, chatID, "⏳ Terlalu banyak percobaan. Tunggu beberapa menit lalu /connect lagi.")
	default:
		_, _ = b.send(ctx, chatID, fmt.Sprintf("❌ Gagal: %s\nKetik /connect untuk mengulang.", code))
	}
}

// ---------- Tombol ----------

func targetOf(q *tg.CallbackQuery) (tg.Target, bool) {
	if q.InlineMessageID != "" {
		return tg.Target{InlineID: q.InlineMessageID}, true
	}
	if q.Message != nil {
		return tg.Target{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID}, true
	}
	return tg.Target{}, false
}

func notModified(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not modified")
}

func (b *Bot) onCallback(ctx context.Context, q *tg.CallbackQuery) {
	if q.Message != nil {
		fake := q.From
		b.pm.track(q.Message.Chat, &fake)
	}
	data := q.Data
	answer := func(text string, alert bool) { _ = b.api.AnswerCallbackQuery(ctx, q.ID, text, alert) }

	switch {
	case data == "start:trial" || data == "start:status":
		answer("", false)
		if q.Message == nil || q.Message.Chat.Type != "private" {
			return
		}
		if data == "start:trial" {
			b.doTrial(ctx, q.Message.Chat.ID, &q.From)
		} else {
			b.sendStatus(ctx, q.Message.Chat.ID, &q.From)
		}

	case strings.HasPrefix(data, "cmd:"):
		text, markup, ok := cmdmenu.View(strings.TrimPrefix(data, "cmd:"))
		answer("", false)
		if !ok {
			return
		}
		if t, has := targetOf(q); has {
			if err := b.api.EditMessageText(ctx, t, text, tg.SendOpts{Markup: markup}); err != nil && !notModified(err) {
				log.Printf("edit cmd: %v", err)
			}
		}

	case strings.HasPrefix(data, "roll:"):
		b.onRollCallback(ctx, q, answer)

	case strings.HasPrefix(data, "buy:"):
		b.buy.HandleCallback(ctx, q)
	}
}

func (b *Bot) onRollCallback(ctx context.Context, q *tg.CallbackQuery, answer func(string, bool)) {
	parts := strings.Split(q.Data, ":")
	if len(parts) < 3 {
		answer("", false)
		return
	}
	action, token := parts[1], parts[2]
	log.Printf("roll %s diklik oleh %d (token %s)", action, q.From.ID, token)
	r := rolls.Get(token)
	if r == nil {
		answer("Permintaan sudah kedaluwarsa.", true)
		return
	}
	res := r.Resolve(action, util.Itoa(q.From.ID))
	if res.Alert != "" {
		answer(res.Alert, res.Text == "")
	} else {
		answer("", false)
	}
	if res.Text != "" {
		if t, ok := targetOf(q); ok {
			if err := b.api.EditMessageText(ctx, t, res.Text, tg.SendOpts{}); err != nil {
				log.Printf("edit roll: %v", err)
			}
		}
	}
}

// ---------- Mode inline: menu .cmd dan Roll Request dari userbot ----------
// Wajib aktifkan di BotFather: /setinline (isi placeholder apa saja, jangan /empty).

var rollQueryRe = regexp.MustCompile(`^roll:([a-f0-9]+)$`)

func (b *Bot) onInline(ctx context.Context, q *tg.InlineQuery) {
	log.Printf("inline_query masuk dari %d: %q", q.From.ID, q.Query)
	query := strings.TrimSpace(q.Query)

	if strings.EqualFold(query, "cmd") {
		results := []any{map[string]any{
			"type":                  "article",
			"id":                    "cmd",
			"title":                 "Daftar Perintah",
			"description":           "Menu perintah userbot",
			"input_message_content": map[string]any{"message_text": cmdmenu.MainText},
			"reply_markup":          cmdmenu.MainKeyboard(),
		}}
		if err := b.api.AnswerInlineQuery(ctx, q.ID, results, 0, true); err != nil {
			log.Printf("inline_query error: %v", err)
		}
		return
	}

	var token string
	var r *rolls.Entry
	if m := rollQueryRe.FindStringSubmatch(query); m != nil {
		token = m[1]
		r = rolls.Get(token)
	}
	var owners []string
	if r != nil {
		owners = r.OwnerIDs
		if len(owners) == 0 {
			owners = []string{r.UserID}
		}
	}
	asker := util.Itoa(q.From.ID)
	isOwner := false
	for _, o := range owners {
		if o == asker {
			isOwner = true
		}
	}
	// hanya akun pemilik userbot yang boleh meminta pesan ini
	if r == nil || r.IsDone() || !isOwner {
		why := fmt.Sprintf("token %s, selesai=%v, pemilik=%s, penanya=%s",
			map[bool]string{true: "ada", false: "tidak ada"}[r != nil],
			r != nil && r.IsDone(), strings.Join(owners, ","), asker)
		rolls.SetLastReject(why)
		log.Printf("inline_query ditolak: %s", why)
		_ = b.api.AnswerInlineQuery(ctx, q.ID, nil, 0, true)
		return
	}

	// Nama pemain di-mention (biru, bisa diklik) bila userbot mengirim data mention
	var entities []map[string]any
	if mt := r.Mention; mt != nil {
		if uid, err := strconv.ParseInt(mt.UserID, 10, 64); err == nil {
			entities = []map[string]any{{"type": "text_mention", "offset": mt.Offset, "length": mt.Length, "user": map[string]any{"id": uid}}}
		}
	}
	sendResults := func(withMention bool) error {
		content := map[string]any{"message_text": r.Text}
		if withMention {
			content["entities"] = entities
		}
		return b.api.AnswerInlineQuery(ctx, q.ID, []any{map[string]any{
			"type":                  "article",
			"id":                    token,
			"title":                 "Roll Request",
			"description":           "Kirim permintaan roll",
			"input_message_content": content,
			"reply_markup": tg.InlineMarkup{InlineKeyboard: [][]tg.Button{{
				{Text: "✅ Konfirm", CallbackData: "roll:ok:" + token},
				{Text: "❌ Tolak", CallbackData: "roll:no:" + token},
			}}},
		}}, 0, true)
	}
	err := sendResults(len(entities) > 0)
	if err != nil && len(entities) > 0 {
		log.Printf("inline mention gagal, kirim tanpa mention: %v", err)
		err = sendResults(false) // cadangan: tanpa mention
	}
	if err != nil {
		log.Printf("inline_query error: %v", err)
	}
}
