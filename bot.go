// Package bot: port bot.js (bot Telegram utama: akun, trial, admin, login, /buy, inline roll).
// Catatan: perintah admin /broadcast (kirim ke SEMUA grup lewat userbot semua pelanggan) sengaja TIDAK diport.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cu/internal/buy"
	"cu/internal/manager"
	"cu/internal/rolls"
	"cu/internal/tg"
	"cu/internal/util"
)

type Config struct {
	OwnerID   string // OWNER_ID (teks angka), boleh kosong
	TrialDays int
	DataDir   string
	PurgeDays float64 // untuk teks pemberitahuan sesi dihapus
}

type Bot struct {
	api *tg.Client
	mgr *manager.Manager
	buy *buy.Buy
	cfg Config

	newLogin func() LoginFlow
	username string
	ownerNum int64
	pm       *pmStore

	mu       sync.Mutex
	logins   map[string]*loginState
	otpLog   map[string][]time.Time
	bcpmBusy bool
}

func New(cfg Config, api *tg.Client, mgr *manager.Manager, b *buy.Buy, newLogin func() LoginFlow) *Bot {
	if cfg.TrialDays <= 0 {
		cfg.TrialDays = 1
	}
	return &Bot{
		api: api, mgr: mgr, buy: b, cfg: cfg, newLogin: newLogin,
		ownerNum: util.ID(cfg.OwnerID),
		pm:       newPmStore(cfg.DataDir),
		logins:   map[string]*loginState{},
		otpLog:   map[string][]time.Time{},
	}
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, o ...tg.SendOpts) (*tg.Message, error) {
	var opts tg.SendOpts
	if len(o) > 0 {
		opts = o[0]
	}
	return b.api.SendMessage(ctx, chatID, text, opts)
}

func (b *Bot) toOwner(ctx context.Context, text string) {
	if b.ownerNum != 0 {
		_, _ = b.send(ctx, b.ownerNum, text)
	}
}

func nameOfUser(u *tg.User) string {
	n := u.FullName()
	if u.Username != "" {
		n += " (@" + u.Username + ")"
	}
	return n
}

func expiryText(exp *int64) string {
	if exp != nil {
		return "sampai " + util.FmtDate(*exp)
	}
	return "permanen"
}

// Run: long polling sampai ctx selesai.
func (b *Bot) Run(ctx context.Context) {
	me, err := b.api.GetMe(ctx)
	if err != nil {
		log.Printf("❌ BOT_TOKEN ditolak Telegram (%v). Isi file .env dengan token terbaru dari BotFather, lalu Restart.", err)
	} else {
		b.username = me.Username
		rolls.SetBotUsername(me.Username)
		log.Printf("Token bot valid: @%s", me.Username)
	}
	rolls.StartCleanup(ctx)
	log.Println("Bot berjalan...")
	if b.ownerNum == 0 {
		log.Println("Info: isi OWNER_ID di .env (ID dari perintah /id) agar fitur admin & /connect bisa dipakai.")
	}

	offset, fatal := 0, 0
	for ctx.Err() == nil {
		ups, err := b.api.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ae *tg.APIError
			if errors.As(err, &ae) && (ae.Code == 401 || ae.Code == 404) {
				fatal++
				if fatal >= 3 {
					log.Printf("Polling error: %v", err)
					log.Println("❌ Token bot tidak valid. Polling dihentikan. Perbaiki BOT_TOKEN di .env lalu Restart.")
					return
				}
			} else {
				fatal = 0
			}
			log.Printf("Polling error: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		fatal = 0
		for i := range ups {
			u := ups[i]
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			go b.handle(ctx, &u)
		}
	}
}

func (b *Bot) handle(ctx context.Context, u *tg.Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic saat memproses update %d: %v", u.UpdateID, r)
		}
	}()
	switch {
	case u.Message != nil:
		b.onMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u.CallbackQuery)
	case u.InlineQuery != nil:
		b.onInline(ctx, u.InlineQuery)
	}
}

// splitCmd: "/Adduser@bot 1 2" -> ("adduser", "1 2", true). ok=false bila bukan perintah / ditujukan ke bot lain.
func (b *Bot) splitCmd(text string) (cmd, args string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	first := text
	rest := ""
	if i := strings.IndexAny(text, " \t\r\n"); i >= 0 {
		first, rest = text[:i], strings.TrimSpace(text[i+1:])
	}
	first = strings.TrimPrefix(first, "/")
	if at := strings.Index(first, "@"); at >= 0 {
		target := first[at+1:]
		first = first[:at]
		if b.username != "" && !strings.EqualFold(target, b.username) {
			return "", "", false
		}
	}
	return strings.ToLower(first), rest, first != ""
}

func (b *Bot) onMessage(ctx context.Context, m *tg.Message) {
	if m.From == nil {
		return
	}
	b.pm.track(m.Chat, m.From)
	b.revealEmojiIDs(ctx, m)

	// nomor HP dikirim lewat tombol "Kirim nomor HP"
	if m.Contact != nil {
		l := b.getLogin(util.Itoa(m.From.ID))
		if m.Chat.Type == "private" && l != nil && m.Contact.UserID == m.From.ID {
			l.mu.Lock()
			isPhone := l.state == "phone"
			l.mu.Unlock()
			if isPhone {
				b.handleLogin(ctx, m, m.Contact.PhoneNumber)
			}
		}
		return
	}
	if m.Text == "" {
		return
	}
	if cmd, args, ok := b.splitCmd(m.Text); ok {
		b.onCommand(ctx, m, cmd, args)
		return
	}
	if strings.HasPrefix(m.Text, "/") {
		return
	}
	if m.Chat.Type == "private" {
		if b.getLogin(util.Itoa(m.From.ID)) != nil {
			b.handleLogin(ctx, m, "")
			return
		}
		_, _ = b.send(ctx, m.Chat.ID, "Ketik /help untuk melihat perintah.")
	}
}

// Admin: kirim emoji premium ke bot -> bot membalas ID-nya (untuk diisi di daftar emoji).
func (b *Bot) revealEmojiIDs(ctx context.Context, m *tg.Message) {
	if m.Text == "" || !b.mgr.IsOwner(util.Itoa(m.From.ID)) {
		return
	}
	var lines []string
	for _, e := range m.Entities {
		if e.Type == "custom_emoji" {
			lines = append(lines, fmt.Sprintf("%s = %s", tg.UTF16Slice(m.Text, e.Offset, e.Length), e.CustomEmojiID))
		}
	}
	if len(lines) > 0 {
		_, _ = b.send(ctx, m.Chat.ID, strings.Join(lines, "\n"))
	}
}

var digitsRe = regexp.MustCompile(`^\d+$`)

func (b *Bot) adminOnly(ctx context.Context, m *tg.Message) bool {
	if b.mgr.IsOwner(util.Itoa(m.From.ID)) {
		return true
	}
	_, _ = b.send(ctx, m.Chat.ID, "⛔ Perintah ini hanya untuk admin.")
	return false
}

func (b *Bot) onCommand(ctx context.Context, m *tg.Message, cmd, args string) {
	chat := m.Chat.ID
	id := util.Itoa(m.From.ID)
	private := m.Chat.Type == "private"
	switch cmd {
	case "start":
		jam := b.cfg.TrialDays * 24
		_, _ = b.send(ctx, chat,
			"Welcome! Bot ini mengatur pendaftaran akun uBot.\n\nKetik /trial untuk coba uBot gratis "+strconv.Itoa(jam)+" jam, atau /connect kalau sudah disetujui.",
			tg.SendOpts{ReplyTo: m.MessageID, Markup: tg.InlineMarkup{InlineKeyboard: [][]tg.Button{{
				{Text: fmt.Sprintf("🎁 Trial %d Jam", jam), CallbackData: "start:trial"},
				{Text: "📊 Status", CallbackData: "start:status"},
			}}}})

	case "help":
		t := "Perintah:\n" +
			"/start - Mulai\n" +
			"/help - Bantuan\n" +
			"/id - Lihat ID Telegram Anda\n" +
			"/connect - Hubungkan akun & aktifkan userbot\n" +
			"/buy - Beli / perpanjang langganan\n" +
			"/trial - Coba gratis (1 akun Telegram 1x)\n" +
			"/status - Lihat status userbot Anda\n" +
			"/cancel - Batalkan proses login\n" +
			"/logout - Matikan userbot & hapus sesi Anda"
		if b.mgr.IsOwner(id) {
			t += "\n\nKhusus admin:\n" +
				"/adduser <id> [hari] - Beri akses (tanpa hari = permanen)\n" +
				"/deluser <id> - Cabut akses\n" +
				"/users - Daftar pengguna\n" +
				"/trials - Daftar pengguna yang sudah klaim trial\n" +
				"/resettrial <id|all> - Reset hak trial (satu ID atau semua)\n" +
				"/orders - Pesanan /buy yang menunggu\n" +
				"/bcpm <pesan> - Kirim pesan ke PM semua orang yang pernah chat ke bot ini"
		}
		_, _ = b.send(ctx, chat, t)

	case "id":
		_, _ = b.send(ctx, chat, fmt.Sprintf("ID Anda: %d", m.From.ID))

	case "status":
		b.sendStatus(ctx, chat, m.From)

	case "connect", "login":
		if private {
			b.startConnect(ctx, m)
		}

	case "cancel":
		b.resetLogin(id)
		_, _ = b.send(ctx, chat, "❎ Proses login dibatalkan.", tg.SendOpts{Markup: tg.Remove})

	case "logout":
		b.logout(ctx, m)

	case "buy":
		b.buy.HandleBuyCommand(ctx, m)

	case "trial":
		if private {
			b.doTrial(ctx, chat, m.From)
		}

	case "adduser":
		b.cmdAddUser(ctx, m, args)
	case "deluser":
		b.cmdDelUser(ctx, m, args)
	case "users":
		b.cmdUsers(ctx, m)
	case "resettrial":
		b.cmdResetTrial(ctx, m, args)
	case "trials":
		b.cmdTrials(ctx, m)
	case "orders":
		if b.adminOnly(ctx, m) {
			b.buy.HandleOrders(ctx, chat)
		}
	case "bcpm":
		b.cmdBcpm(ctx, m, args)
	}
}

func (b *Bot) sendStatus(ctx context.Context, chatID int64, from *tg.User) {
	i := b.mgr.Info(util.Itoa(from.ID))
	if !i.Allowed {
		_, _ = b.send(ctx, chatID, "⛔ Anda belum punya akses. Ketik /trial untuk coba gratis, atau /buy untuk membeli langganan.")
		return
	}
	akses := expiryText(i.Expires)
	if i.Owner {
		akses = "Admin"
	}
	conn, run := "belum", "🔴 mati"
	if i.HasSession {
		conn = "ya"
	}
	if i.Running {
		run = "🟢 aktif"
	}
	_, _ = b.send(ctx, chatID, fmt.Sprintf("📊 Status\nAkses: %s\nAkun terhubung: %s\nUserbot: %s", akses, conn, run))
}

func (b *Bot) doTrial(ctx context.Context, chatID int64, from *tg.User) {
	r := b.mgr.ClaimTrial(ctx, util.Itoa(from.ID), nameOfUser(from))
	if r.OK {
		_, _ = b.send(ctx, chatID, fmt.Sprintf("🎁 Trial %d hari aktif sampai %s.\nKetik /connect untuk menghubungkan akun Anda.\nSetelah habis, ketik /buy untuk lanjut berlangganan.", r.Days, util.FmtDate(*r.Expires)))
		b.toOwner(ctx, fmt.Sprintf("🎁 Trial diklaim: %s\nID: %d", nameOfUser(from), from.ID))
		return
	}
	switch r.Reason {
	case "owner":
		_, _ = b.send(ctx, chatID, "ℹ️ Anda admin, tidak perlu trial.")
	case "active":
		_, _ = b.send(ctx, chatID, "ℹ️ Anda masih punya akses aktif. Cek /status.")
	default:
		_, _ = b.send(ctx, chatID, fmt.Sprintf("⛔ Trial gratis sudah pernah dipakai (%s). Satu akun hanya bisa sekali.\nKetik /buy untuk berlangganan.", util.FmtDate(r.At)))
	}
}

// ---------- Admin ----------

func (b *Bot) cmdAddUser(ctx context.Context, m *tg.Message, args string) {
	if !b.adminOnly(ctx, m) {
		return
	}
	f := strings.Fields(args)
	if len(f) == 0 || !digitsRe.MatchString(f[0]) || (len(f) > 1 && !digitsRe.MatchString(f[1])) || len(f) > 2 {
		_, _ = b.send(ctx, m.Chat.ID, "Gunakan: /adduser <id> [hari]\nContoh: /adduser 123456789 30\nTanpa hari = permanen.\nTeman Anda bisa melihat ID-nya dengan mengirim /id ke bot ini.")
		return
	}
	target := f[0]
	days := 0
	if len(f) > 1 {
		days, _ = strconv.Atoi(f[1])
	}
	name := ""
	if ch, err := b.api.GetChat(ctx, util.ID(target)); err == nil {
		name = ch.FullName()
		if ch.Username != "" {
			name += " (@" + ch.Username + ")"
		}
	}
	u := b.mgr.AddUser(ctx, target, days, name)
	label := name
	if label == "" {
		label = target
	}
	_, _ = b.send(ctx, m.Chat.ID, fmt.Sprintf("✅ Akses diberikan ke %s\nID: %s\nMasa aktif: %s", label, target, expiryText(u.Expires)))
	if _, err := b.send(ctx, util.ID(target), fmt.Sprintf("✅ Akses Anda sudah aktif (%s).\nKetik /connect untuk menghubungkan akun Anda.", expiryText(u.Expires))); err != nil {
		_, _ = b.send(ctx, m.Chat.ID, "ℹ️ Pengguna belum pernah membuka bot ini. Minta ia kirim /start ke bot dulu.")
	}
}

func (b *Bot) cmdDelUser(ctx context.Context, m *tg.Message, args string) {
	if !b.adminOnly(ctx, m) {
		return
	}
	target := strings.TrimSpace(args)
	if !digitsRe.MatchString(target) {
		_, _ = b.send(ctx, m.Chat.ID, "Gunakan: /deluser <id>")
		return
	}
	existed := b.mgr.DelUser(ctx, target)
	if !existed {
		_, _ = b.send(ctx, m.Chat.ID, "ID itu tidak ada di daftar.")
		return
	}
	_, _ = b.send(ctx, m.Chat.ID, fmt.Sprintf("🗑 Akses %s dicabut dan userbot dimatikan.", target))
	_, _ = b.send(ctx, util.ID(target), "⛔ Akses Anda telah dicabut oleh admin.")
}

func (b *Bot) cmdUsers(ctx context.Context, m *tg.Message) {
	if !b.adminOnly(ctx, m) {
		return
	}
	list := b.mgr.ListUsers()
	if len(list) == 0 {
		_, _ = b.send(ctx, m.Chat.ID, "Belum ada pengguna. Tambah dengan /adduser <id> [hari].")
		return
	}
	lines := make([]string, len(list))
	for i, u := range list {
		dot := "🔴"
		if u.Running {
			dot = "🟢"
		}
		name := u.Name
		if name == "" {
			name = "-"
		}
		exp := "permanen"
		if u.Expires != nil {
			pre := "s/d "
			if u.Expired {
				pre = "HABIS "
			}
			exp = pre + util.FmtDate(*u.Expires)
		}
		extra := ""
		if !u.HasSession {
			extra = " • belum connect"
		}
		lines[i] = fmt.Sprintf("%s %s\n   ID: %s\n   %s%s", dot, name, u.ID, exp, extra)
	}
	_, _ = b.send(ctx, m.Chat.ID, fmt.Sprintf("👥 Pengguna (%d):\n\n%s", len(list), strings.Join(lines, "\n")))
}

func (b *Bot) cmdResetTrial(ctx context.Context, m *tg.Message, args string) {
	if !b.adminOnly(ctx, m) {
		return
	}
	a := strings.TrimSpace(args)
	if !digitsRe.MatchString(a) && !strings.EqualFold(a, "all") {
		_, _ = b.send(ctx, m.Chat.ID, "Gunakan: /resettrial <id>  atau  /resettrial all")
		return
	}
	n := b.mgr.ResetTrial(a)
	if n > 0 {
		_, _ = b.send(ctx, m.Chat.ID, fmt.Sprintf("✅ Hak trial direset (%d akun). Mereka bisa /trial lagi.", n))
	} else {
		_, _ = b.send(ctx, m.Chat.ID, "ℹ️ ID itu belum pernah klaim trial.")
	}
}

func (b *Bot) cmdTrials(ctx context.Context, m *tg.Message) {
	if !b.adminOnly(ctx, m) {
		return
	}
	list := b.mgr.ListTrials()
	if len(list) == 0 {
		_, _ = b.send(ctx, m.Chat.ID, "Belum ada yang klaim trial.")
		return
	}
	lines := make([]string, len(list))
	for i, x := range list {
		name := x.Name
		if name == "" {
			name = "-"
		}
		lines[i] = fmt.Sprintf("• %s\n   ID: %s\n   klaim: %s", name, x.ID, util.FmtDate(x.At))
	}
	_, _ = b.send(ctx, m.Chat.ID, fmt.Sprintf("🎁 Trial diklaim (%d):\n\n%s", len(list), strings.Join(lines, "\n")))
}

// /bcpm <pesan> atau reply sebuah pesan lalu /bcpm. Dikirim oleh bot ini ke orang yang pernah chat / klik tombol di PM-nya.
func (b *Bot) cmdBcpm(ctx context.Context, m *tg.Message, args string) {
	if !b.adminOnly(ctx, m) {
		return
	}
	chat := m.Chat.ID
	text := strings.TrimSpace(args)
	if text == "" && m.ReplyToMessage != nil {
		text = strings.TrimSpace(m.ReplyToMessage.Text)
		if text == "" {
			text = strings.TrimSpace(m.ReplyToMessage.Caption)
		}
	}
	if text == "" {
		_, _ = b.send(ctx, chat, "Gunakan: /bcpm <pesan>\natau reply sebuah pesan lalu ketik /bcpm")
		return
	}
	b.mu.Lock()
	if b.bcpmBusy {
		b.mu.Unlock()
		_, _ = b.send(ctx, chat, "⏳ Broadcast PM sebelumnya masih berjalan, tunggu sampai selesai.")
		return
	}
	b.bcpmBusy = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.bcpmBusy = false
		b.mu.Unlock()
	}()

	// Pengguna lama di /users (sudah pernah buka bot) ikut dimasukkan ke daftar PM
	for _, u := range b.mgr.ListUsers() {
		if digitsRe.MatchString(u.ID) {
			n := u.Name
			if n == "" {
				n = "-"
			}
			b.pm.add(u.ID, n)
		}
	}
	ids := b.pm.ids()
	var sent, blocked, failed int
	_, _ = b.send(ctx, chat, fmt.Sprintf("📢 Broadcast PM dimulai ke %d orang, mohon tunggu… (hasil dikirim setelah selesai)", len(ids)))
	for _, sid := range ids {
		if ctx.Err() != nil {
			return
		}
		to := util.ID(sid)
		_, err := b.send(ctx, to, text)
		var ae *tg.APIError
		if err != nil && errors.As(err, &ae) && ae.RetryAfter > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(ae.RetryAfter+1) * time.Second):
			}
			_, err = b.send(ctx, to, text)
		}
		switch {
		case err == nil:
			sent++
		case errors.As(err, &ae) && (ae.Code == 403 || ae.Code == 400):
			b.pm.remove(sid) // diblokir / akun dihapus / chat tidak ada
			blocked++
		default:
			failed++
		}
		time.Sleep(60 * time.Millisecond) // di bawah batas ~30 pesan/detik
	}
	_, _ = b.send(ctx, chat, fmt.Sprintf("✅ Broadcast PM selesai\n📨 Terkirim: %d/%d\n🚫 Diblokir/tidak aktif (dihapus dari daftar): %d\n❌ Gagal: %d", sent, len(ids), blocked, failed))
}

// Watcher: pemberitahuan masa aktif (dipasang ke manager.StartWatcher).
func (b *Bot) Watcher() manager.Watcher {
	bg := context.Background
	return manager.Watcher{
		OnExpire: func(id string) {
			_, _ = b.send(bg(), util.ID(id), "⌛ Masa aktif Anda habis, userbot dimatikan. Ketik /buy untuk perpanjang.")
			b.toOwner(bg(), fmt.Sprintf("⌛ Masa aktif %s habis, userbot dimatikan.", id))
		},
		OnWarn: func(id string, left time.Duration) {
			h := int(left.Hours() + 0.5)
			if h < 1 {
				h = 1
			}
			_, _ = b.send(bg(), util.ID(id),
				fmt.Sprintf("⏳ <b>Saldo jam tinggal %d jam</b>\n\nKalau habis, uBot berhenti otomatis dan grup kamu nggak tercatat. Perpanjang sekarang supaya nggak kepotong.", h),
				tg.SendOpts{ParseMode: "HTML", Markup: tg.InlineMarkup{InlineKeyboard: [][]tg.Button{{{Text: "💳 Perpanjang sekarang", CallbackData: "buy:start"}}}}})
		},
		OnPurge: func(id string) {
			_, _ = b.send(bg(), util.ID(id), "🔒 Masa aktif Anda sudah lama habis, jadi sesi akun Anda diakhiri dan dihapus dari server demi keamanan. Kalau mau pakai lagi, ketik /buy lalu /connect.")
			b.toOwner(bg(), fmt.Sprintf("🧹 Sesi %s dihapus (masa aktif habis lebih dari %v hari).", id, b.cfg.PurgeDays))
		},
	}
}
