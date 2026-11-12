// Package buy: port buy.js. /buy OTOMATIS lewat Kasera Pay (QRIS). Akses aktif sendiri setelah
// pembayaran terkonfirmasi lewat API (bukan dari isi webhook). Format data/orders.json sama dengan Node.
package buy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"cu/internal/config"
	"cu/internal/manager"
	"cu/internal/tg"
	"cu/internal/util"
)

type Config struct {
	APIKey        string
	BaseURL       string
	WebhookSecret string
	WebhookPort   int
	WebhookPath   string
	PricePerDay   int
	MaxDays       int
	TTLMin        int
	PollEvery     time.Duration
	Grace         time.Duration
	DataDir       string
	OwnerID       int64 // 0 = tidak ada
}

func ConfigFromEnv(dataDir string, ownerID int64) Config {
	ttl := config.Int("PAY_TTL_MIN", 20)
	if ttl < 1 {
		ttl = 20
	}
	poll := config.Int("PAY_POLL_SEC", 5)
	if poll <= 0 {
		poll = 5
	}
	if poll < 3 { // Kasera membatasi 1 cek/detik per transaksi
		poll = 3
	}
	grace := config.Int("PAY_GRACE_MIN", 30)
	if grace <= 0 {
		grace = 30
	}
	return Config{
		APIKey:        config.String("KASERA_API_KEY", ""),
		BaseURL:       strings.TrimRight(config.String("KASERA_BASE_URL", "https://pay.kasera.id"), "/"),
		WebhookSecret: config.String("KASERA_WEBHOOK_SECRET", ""),
		WebhookPort:   config.Int("WEBHOOK_PORT", 0),
		WebhookPath:   config.String("WEBHOOK_PATH", "/kasera/webhook"),
		PricePerDay:   config.Int("PRICE_PER_DAY", 3000),
		MaxDays:       config.Int("MAX_DAYS", 30),
		TTLMin:        ttl,
		PollEvery:     time.Duration(poll) * time.Second,
		Grace:         time.Duration(grace) * time.Minute,
		DataDir:       dataDir,
		OwnerID:       ownerID,
	}
}

type Order struct {
	ID          string  `json:"id"`
	TxnID       string  `json:"txnId"`
	UserID      string  `json:"userId"`
	Username    string  `json:"username"`
	Name        string  `json:"name"`
	Days        int     `json:"days"`
	Base        int64   `json:"base"`
	Fee         float64 `json:"fee"`
	Total       int64   `json:"total"`
	Status      string  `json:"status"` // pending | paid | expired | cancelled
	Credited    bool    `json:"credited"`
	CreditedAt  int64   `json:"creditedAt,omitempty"`
	Livemode    bool    `json:"livemode"`
	Created     int64   `json:"created"`
	GwExpires   int64   `json:"gwExpires,omitempty"`
	Expires     int64   `json:"expires"`
	ChatID      int64   `json:"chatId,omitempty"`
	MsgID       int     `json:"msgId,omitempty"`
	HasPhoto    bool    `json:"hasPhoto,omitempty"`
	PaidAt      int64   `json:"paidAt,omitempty"`
	ClosedAt    int64   `json:"closedAt,omitempty"`
	CompletedAt any     `json:"completedAt,omitempty"`
	Mismatch    bool    `json:"mismatchReported,omitempty"`
}

type Buy struct {
	cfg  Config
	bot  *tg.Client
	mgr  *manager.Manager
	http *http.Client

	mu        sync.Mutex // melindungi orders, raw, procs, lastCheck, polling
	orders    map[string]*Order
	raw       map[string]json.RawMessage // entri lama yang tak bisa dibaca dipertahankan apa adanya
	procs     map[string]*sync.Mutex
	lastCheck map[string]time.Time
	polling   bool
}

func New(cfg Config, bot *tg.Client, mgr *manager.Manager) (*Buy, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	b := &Buy{
		cfg: cfg, bot: bot, mgr: mgr,
		http:      &http.Client{},
		orders:    map[string]*Order{},
		raw:       map[string]json.RawMessage{},
		procs:     map[string]*sync.Mutex{},
		lastCheck: map[string]time.Time{},
	}
	b.load()
	return b, nil
}

func (b *Buy) Enabled() bool { return b.cfg.APIKey != "" }

func (b *Buy) file() string { return filepath.Join(b.cfg.DataDir, "orders.json") }

func (b *Buy) load() {
	data, err := os.ReadFile(b.file())
	if err != nil {
		return
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		// jangan menimpa file yang tak terbaca: simpan salinan
		_ = os.WriteFile(b.file()+".rusak", data, 0o600)
		log.Printf("peringatan: orders.json tidak terbaca (%v), salinan disimpan di orders.json.rusak", err)
		return
	}
	for id, r := range all {
		var o Order
		if err := json.Unmarshal(r, &o); err != nil {
			b.raw[id] = r
			continue
		}
		b.orders[id] = &o
	}
}

// saveLocked: dipanggil saat b.mu dipegang. Tulis atomik.
func (b *Buy) saveLocked() {
	out := make(map[string]any, len(b.orders)+len(b.raw))
	for id, r := range b.raw {
		out[id] = r
	}
	for id, o := range b.orders {
		out[id] = o
	}
	j, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		log.Printf("buy: gagal menyimpan orders: %v", err)
		return
	}
	tmp := b.file() + ".tmp"
	if err := os.WriteFile(tmp, j, 0o600); err != nil {
		log.Printf("buy: gagal menyimpan orders: %v", err)
		return
	}
	if err := os.Rename(tmp, b.file()); err != nil {
		log.Printf("buy: gagal menyimpan orders: %v", err)
	}
}

func (b *Buy) snap(id string) (Order, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.orders[id]
	if !ok {
		return Order{}, false
	}
	return *o, true
}

func (b *Buy) all() []Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Order, 0, len(b.orders))
	for _, o := range b.orders {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out
}

// mut mengubah satu pesanan lalu menyimpan.
func (b *Buy) mut(id string, fn func(o *Order)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o, ok := b.orders[id]; ok {
		fn(o)
		b.saveLocked()
	}
}

// lockProc: kunci per pesanan agar webhook & polling tidak memproses pesanan yang sama bersamaan.
func (b *Buy) lockProc(id string) func() {
	b.mu.Lock()
	m := b.procs[id]
	if m == nil {
		m = &sync.Mutex{}
		b.procs[id] = m
	}
	b.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func nowMs() int64 { return time.Now().UnixMilli() }

func (b *Buy) clampDays(n int) int {
	if n < 1 {
		n = 1
	}
	if n > b.cfg.MaxDays {
		n = b.cfg.MaxDays
	}
	return n
}

func (b *Buy) newOrderID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		r := make([]byte, 3)
		_, _ = rand.Read(r)
		id := "UB" + strings.ToUpper(hex.EncodeToString(r))
		if _, ok := b.orders[id]; !ok {
			if _, ok2 := b.raw[id]; !ok2 {
				return id
			}
		}
	}
}

func isLate(o Order) bool { return nowMs() > o.Expires }

// watchable: masih perlu dipantau (menunggu bayar, atau sudah ditutup tetapi QR-nya mungkin masih berlaku).
func (b *Buy) watchable(o Order) bool {
	if o.TxnID == "" {
		return false
	}
	if o.Status == "pending" {
		return true
	}
	if o.Status == "expired" || o.Status == "cancelled" {
		gw := o.GwExpires
		if gw == 0 {
			gw = o.Expires
		}
		return nowMs() < gw+b.cfg.Grace.Milliseconds()
	}
	return false
}

func (b *Buy) notifyOwner(ctx context.Context, text string) {
	if b.cfg.OwnerID != 0 {
		_, _ = b.bot.SendMessage(ctx, b.cfg.OwnerID, text, tg.SendOpts{})
	}
}

func (b *Buy) sendUser(ctx context.Context, userID, text string) {
	if id := util.ID(userID); id != 0 {
		_, _ = b.bot.SendMessage(ctx, id, text, tg.SendOpts{})
	}
}

// ---------- Tagihan ----------

func (b *Buy) invoiceText(o Order) string {
	return "💲 <b>T A G I H A N</b> 💲\n\n" +
		fmt.Sprintf("<blockquote>🎟 Langganan %d hari\n", o.Days) +
		fmt.Sprintf("🪙 Harga paket: %s\n", util.Rp(o.Base)) +
		fmt.Sprintf("💎 <b>Total bayar: %s</b></blockquote>\n", util.Rp(o.Total)) +
		"<blockquote>📲 Scan QRIS di atas dengan e-wallet / m-banking apa saja.\n" +
		fmt.Sprintf("⏳ Bayar sebelum %s WIB\n", util.FmtDate(o.Expires)) +
		"✅ Akses aktif otomatis begitu pembayaran terdeteksi (biasanya beberapa detik).</blockquote>"
}

func invoiceButtons(o Order) tg.InlineMarkup {
	return tg.InlineMarkup{InlineKeyboard: [][]tg.Button{
		{{Text: "🔄 Cek pembayaran", CallbackData: "buy:chk:" + o.ID}},
		{{Text: "❌ Batal", CallbackData: "buy:cancel:" + o.ID}},
	}}
}

func notModified(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

func (b *Buy) editInvoice(ctx context.Context, id, text string, keepButtons bool) {
	o, ok := b.snap(id)
	if !ok || o.ChatID == 0 || o.MsgID == 0 {
		return
	}
	markup := tg.InlineMarkup{InlineKeyboard: [][]tg.Button{}}
	if keepButtons {
		markup = invoiceButtons(o)
	}
	err := b.bot.EditMessageCaption(ctx, tg.Target{ChatID: o.ChatID, MessageID: o.MsgID}, text, "HTML", markup)
	if err != nil && !notModified(err) {
		log.Printf("buy: edit tagihan #%s: %v", id, err)
	}
}

// ---------- Penyelesaian pesanan ----------

// credit memberi masa aktif. Aman dipanggil berulang: hanya berhasil sekali (Credited).
func (b *Buy) credit(ctx context.Context, id string) {
	o, ok := b.snap(id)
	if !ok || o.Credited {
		return
	}
	u := b.mgr.ExtendUser(ctx, o.UserID, o.Days, o.Name)
	b.mut(id, func(o *Order) {
		o.Credited = true
		o.CreditedAt = nowMs()
	})
	until := "permanen"
	if u.Expires != nil {
		until = "sampai " + util.FmtDate(*u.Expires)
	}
	b.editInvoice(ctx, id, fmt.Sprintf("✅ Pembayaran #%s diterima (%s). Terima kasih!", o.ID, util.Rp(o.Total)), false)
	b.sendUser(ctx, o.UserID, fmt.Sprintf("✅ Pembayaran #%s diterima.\nAkses aktif %s.\nKetik /connect untuk menghubungkan akun, atau /status untuk cek masa aktif.", o.ID, until))
	b.notifyOwner(ctx, fmt.Sprintf("💰 Pembayaran otomatis #%s\n%s (ID %s)\n%d hari • %s (biaya gateway %s)",
		o.ID, o.Name, o.UserID, o.Days, util.Rp(o.Total), util.Rp(int64(o.Fee))))
}

// check: satu-satunya pintu untuk menandai lunas, SELALU lewat status API Kasera (bukan isi webhook).
// Pesanan yang sudah ditutup tetap dicek: QR Kasera tidak bisa dibatalkan lewat API, jadi bila ternyata
// dibayar, pembayarannya tetap diaktifkan.
func (b *Buy) check(ctx context.Context, id string) (string, error) {
	unlock := b.lockProc(id)
	defer unlock()
	o, ok := b.snap(id)
	if !ok {
		return "", fmt.Errorf("pesanan #%s tidak ada", id)
	}
	if o.Status == "paid" {
		if !o.Credited {
			b.credit(ctx, id)
		}
		return "paid", nil
	}
	if o.Status != "pending" && o.Status != "expired" && o.Status != "cancelled" {
		return o.Status, nil
	}
	t, err := b.getTxn(ctx, o.TxnID)
	if err != nil {
		return "", err
	}
	if ts, ok := parseTime(t.ExpiresAt); ok {
		b.mut(id, func(o *Order) { o.GwExpires = ts })
	}

	if t.Status == "succeeded" {
		if int64(math.Round(float64(t.Amount))) != o.Base || (t.ExternalID != "" && t.ExternalID != o.ID) {
			if !o.Mismatch {
				b.mut(id, func(o *Order) { o.Mismatch = true })
				log.Printf("buy: #%s status succeeded tapi data tidak cocok (amount %v, external_id %s). Diabaikan, cek manual.", o.ID, float64(t.Amount), t.ExternalID)
				b.notifyOwner(ctx, fmt.Sprintf("⚠️ Pesanan #%s: Kasera melapor lunas tetapi data tidak cocok (amount %v vs %d). Cek dashboard Kasera.", o.ID, float64(t.Amount), o.Base))
			}
			return "mismatch", nil
		}
		wasClosed := o.Status != "pending"
		b.mut(id, func(o *Order) {
			o.Status = "paid"
			o.PaidAt = nowMs()
			if t.PaidAt != "" {
				o.CompletedAt = t.PaidAt
			}
			if f := float64(t.Fee); f != 0 {
				o.Fee = f
			}
		})
		if wasClosed {
			b.sendUser(ctx, o.UserID, fmt.Sprintf("ℹ️ Pembayaran untuk pesanan #%s yang sempat ditutup ternyata masuk. Akses sedang diaktifkan.", o.ID))
		}
		b.credit(ctx, id)
		return "paid", nil
	}

	if o.Status != "pending" {
		return o.Status, nil // sudah ditutup, hanya dipantau menunggu pembayaran terlambat
	}

	// Gateway menutup (kedaluwarsa/gagal/batal) atau lewat batas waktu lokal -> tutup di sisi kita
	if t.Status == "expired" || t.Status == "failed" || t.Status == "canceled" || isLate(o) {
		b.mut(id, func(o *Order) {
			o.Status = "expired"
			o.ClosedAt = nowMs()
		})
		b.editInvoice(ctx, id, fmt.Sprintf("⌛ Pesanan #%s kedaluwarsa karena tidak dibayar. Ketik /buy untuk memesan lagi.", o.ID), false)
		return "expired", nil
	}
	return "pending", nil
}

// ---------- Pemantau berkala + webhook ----------

func (b *Buy) pollOnce(ctx context.Context) {
	b.mu.Lock()
	if b.polling {
		b.mu.Unlock()
		return
	}
	b.polling = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.polling = false
		b.mu.Unlock()
	}()
	for _, o := range b.all() {
		if o.TxnID == "" { // pesanan lama dari alur manual, tidak disentuh
			continue
		}
		if (o.Status == "paid" && !o.Credited) || b.watchable(o) {
			if _, err := b.check(ctx, o.ID); err != nil {
				log.Printf("buy poll #%s: %v", o.ID, err)
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// Start menjalankan pemantau (dan webhook bila WEBHOOK_PORT > 0).
func (b *Buy) Start(ctx context.Context) {
	if !b.Enabled() {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second): // setelah restart, langsung cocokkan pesanan yang menunggu
		}
		b.pollOnce(ctx)
		t := time.NewTicker(b.cfg.PollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.pollOnce(ctx)
			}
		}
	}()
	if b.cfg.WebhookPort > 0 {
		b.startWebhook(ctx)
	}
	log.Printf("Fitur /buy OTOMATIS (Kasera Pay QRIS) aktif: %s/hari, maks %d hari, cek tiap %v.",
		util.Rp(int64(b.cfg.PricePerDay)), b.cfg.MaxDays, b.cfg.PollEvery)
}

// Tanda tangan: header Kasera-Signature-V1 = "t=<unix>,v1=<hex>[,v1=<hex>]", HMAC-SHA256 atas "<t>.<body mentah>".
func (b *Buy) validSignature(header string, raw []byte) bool {
	if b.cfg.WebhookSecret == "" {
		return true // status tetap dikonfirmasi ulang lewat API
	}
	var t string
	var v1 []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			t = kv[1]
		case "v1":
			v1 = append(v1, kv[1])
		}
	}
	ts, err := strconv.ParseFloat(t, 64)
	if t == "" || err != nil || math.Abs(float64(time.Now().Unix())-ts) > 300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(b.cfg.WebhookSecret))
	mac.Write([]byte(t + "." + string(raw)))
	want := hex.EncodeToString(mac.Sum(nil))
	for _, v := range v1 {
		if len(v) == len(want) && subtle.ConstantTimeCompare([]byte(v), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

func (b *Buy) startWebhook(ctx context.Context) {
	if b.cfg.WebhookSecret == "" {
		log.Println("Peringatan: KASERA_WEBHOOK_SECRET kosong, tanda tangan webhook tidak diperiksa (status tetap dikonfirmasi lewat API).")
	}
	mux := http.NewServeMux()
	mux.HandleFunc(b.cfg.WebhookPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
		if err != nil || len(raw) > 64*1024 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !b.validSignature(r.Header.Get("Kasera-Signature-V1"), raw) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var p struct {
			Type string `json:"type"`
			Data struct {
				PaymentRequestID string `json:"payment_request_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("ok"))
		if p.Type != "payment.paid" {
			return
		}
		for _, o := range b.all() {
			if o.TxnID == p.Data.PaymentRequestID {
				id := o.ID
				go func() {
					if _, err := b.check(context.Background(), id); err != nil {
						log.Printf("buy webhook #%s: %v", id, err)
					}
				}()
				break
			}
		}
	})
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(b.cfg.WebhookPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		log.Printf("Webhook Kasera aktif di port %d%s", b.cfg.WebhookPort, b.cfg.WebhookPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Webhook server error: %v", err)
		}
	}()
}

// ---------- Layar pilih durasi ----------

func (b *Buy) pickScreen(days int) (string, tg.SendOpts) {
	days = b.clampDays(days)
	price := int64(b.cfg.PricePerDay)
	text := "💳 <b>Beli langganan uBot List</b>\n\n" +
		fmt.Sprintf("%s per hari, maksimal %d hari per transaksi.\n\n", util.Rp(price), b.cfg.MaxDays) +
		fmt.Sprintf("<blockquote>Durasi   %d hari\nHarga    %s</blockquote>\n", days, util.Rp(int64(days)*price)) +
		"Atur pakai tombol ➖ ➕, lalu tekan Lanjut. Pembayaran lewat QRIS dan akses aktif otomatis."
	minus, plus := "buy:noop", "buy:noop"
	if days > 1 {
		minus = fmt.Sprintf("buy:d:%d", days-1)
	}
	if days < b.cfg.MaxDays {
		plus = fmt.Sprintf("buy:d:%d", days+1)
	}
	var quick []tg.Button
	for _, d := range []int{1, 3, 7, 14, 30} {
		if d <= b.cfg.MaxDays {
			quick = append(quick, tg.Button{Text: fmt.Sprintf("%dH", d), CallbackData: fmt.Sprintf("buy:d:%d", d)})
		}
	}
	rows := [][]tg.Button{
		{
			{Text: "➖", CallbackData: minus},
			{Text: fmt.Sprintf("%d hari", days), CallbackData: "buy:noop"},
			{Text: "➕", CallbackData: plus},
		},
	}
	if len(quick) > 0 {
		rows = append(rows, quick)
	}
	rows = append(rows, []tg.Button{{Text: "✅ Lanjut", CallbackData: fmt.Sprintf("buy:go:%d", days)}})
	return text, tg.SendOpts{ParseMode: "HTML", Markup: tg.InlineMarkup{InlineKeyboard: rows}}
}

// HandleBuyCommand: /buy
func (b *Buy) HandleBuyCommand(ctx context.Context, msg *tg.Message) {
	if msg.Chat.Type != "private" {
		_, _ = b.bot.SendMessage(ctx, msg.Chat.ID, "Buka chat pribadi dengan bot ini lalu ketik /buy.", tg.SendOpts{})
		return
	}
	if !b.Enabled() {
		_, _ = b.bot.SendMessage(ctx, msg.Chat.ID, "ℹ️ Pembelian otomatis belum diaktifkan. Hubungi admin untuk berlangganan.", tg.SendOpts{})
		return
	}
	if msg.From != nil && b.mgr.IsOwner(util.Itoa(msg.From.ID)) {
		_, _ = b.bot.SendMessage(ctx, msg.Chat.ID, "Anda admin, akses tidak perlu dibeli.", tg.SendOpts{})
		return
	}
	text, opts := b.pickScreen(1)
	_, _ = b.bot.SendMessage(ctx, msg.Chat.ID, text, opts)
}

// HandleOrders: /orders (admin dicek oleh pemanggil)
func (b *Buy) HandleOrders(ctx context.Context, chatID int64) {
	all := b.all()
	var pending, paid []Order
	var stuck int
	since := nowMs() - 24*60*60*1000
	var sum int64
	for _, o := range all {
		if o.TxnID == "" {
			continue
		}
		if o.Status == "pending" && !isLate(o) {
			pending = append(pending, o)
		}
		if o.Status == "paid" && o.PaidAt >= since {
			paid = append(paid, o)
			sum += o.Base
		}
		if o.Status == "paid" && !o.Credited {
			stuck++
		}
	}
	t := fmt.Sprintf("🧾 24 jam terakhir: %d lunas, omzet paket %s\n", len(paid), util.Rp(sum))
	if stuck > 0 {
		t += fmt.Sprintf("⚠️ %d pesanan lunas belum berhasil mengaktifkan akses (dicoba otomatis tiap %v)\n", stuck, b.cfg.PollEvery)
	}
	if len(pending) > 0 {
		t += fmt.Sprintf("\nMenunggu bayar (%d):\n", len(pending))
		lines := make([]string, len(pending))
		for i, o := range pending {
			lines[i] = fmt.Sprintf("#%s • %s\n   ID %s • %d hari • %s", o.ID, o.Name, o.UserID, o.Days, util.Rp(o.Total))
		}
		t += strings.Join(lines, "\n")
	} else {
		t += "\nTidak ada pesanan yang menunggu."
	}
	_, _ = b.bot.SendMessage(ctx, chatID, t, tg.SendOpts{})
}

// HandleCallback memproses tombol "buy:*". Mengembalikan false bila bukan milik modul ini.
func (b *Buy) HandleCallback(ctx context.Context, q *tg.CallbackQuery) bool {
	if !strings.HasPrefix(q.Data, "buy:") {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("buy callback panic: %v", r)
			_ = b.bot.AnswerCallbackQuery(ctx, q.ID, "Terjadi kesalahan.", true)
		}
	}()
	parts := strings.SplitN(q.Data, ":", 3)
	act, arg := "", ""
	if len(parts) > 1 {
		act = parts[1]
	}
	if len(parts) > 2 {
		arg = parts[2]
	}
	answer := func(text string, alert bool) { _ = b.bot.AnswerCallbackQuery(ctx, q.ID, text, alert) }
	m := q.Message
	if m == nil || act == "noop" {
		answer("", false)
		return true
	}
	uid := util.Itoa(q.From.ID)

	switch act {
	case "start":
		text, opts := b.pickScreen(1)
		answer("", false)
		_, _ = b.bot.SendMessage(ctx, m.Chat.ID, text, opts)
		return true

	case "d":
		n, _ := strconv.Atoi(arg)
		text, opts := b.pickScreen(n)
		answer("", false)
		if err := b.bot.EditMessageText(ctx, tg.Target{ChatID: m.Chat.ID, MessageID: m.MessageID}, text, opts); err != nil && !notModified(err) {
			log.Printf("buy: %v", err)
		}
		return true

	case "go":
		b.goOrder(ctx, q, arg, uid, answer)
		return true
	}

	// chk / cancel: pastikan pesanan milik penekan tombol
	o, ok := b.snap(arg)
	if !ok || o.UserID != uid {
		answer("", false)
		return true
	}
	switch act {
	case "chk":
		if o.Status == "paid" {
			answer("Pembayaran sudah diterima ✅", true)
			return true
		}
		if o.Status != "pending" {
			answer("Pesanan ini sudah ditutup. Ketik /buy lagi.", true)
			return true
		}
		b.mu.Lock()
		last := b.lastCheck[o.ID]
		tooSoon := time.Since(last) < 4*time.Second
		if !tooSoon {
			b.lastCheck[o.ID] = time.Now()
		}
		b.mu.Unlock()
		if tooSoon {
			answer("Tunggu beberapa detik lalu coba lagi.", false)
			return true
		}
		st, err := b.check(ctx, o.ID)
		switch {
		case err != nil:
			answer("Gagal memeriksa, coba lagi sebentar.", true)
		case st == "paid":
			answer("Pembayaran diterima ✅", true)
		case st == "pending":
			answer("Belum terdeteksi. Selesaikan pembayaran lalu cek lagi.", true)
		default:
			answer("Pesanan sudah ditutup. Ketik /buy lagi.", true)
		}
		return true

	case "cancel":
		if o.Status != "pending" {
			answer("Pesanan ini sudah diproses.", true)
			return true
		}
		// Cek dulu: kalau sudah dibayar, jangan dibatalkan
		if st, err := b.check(ctx, o.ID); err == nil && st == "paid" {
			answer("Pembayaran ternyata sudah diterima ✅", true)
			return true
		}
		unlock := b.lockProc(o.ID)
		b.mut(o.ID, func(x *Order) {
			if x.Status == "pending" {
				x.Status = "cancelled"
				x.ClosedAt = nowMs()
			}
		})
		unlock()
		answer("Pesanan dibatalkan.", false)
		b.editInvoice(ctx, o.ID, fmt.Sprintf("❎ Pesanan #%s dibatalkan. Ketik /buy untuk membeli lagi.", o.ID), false)
		return true
	}
	answer("", false)
	return true
}

func (b *Buy) goOrder(ctx context.Context, q *tg.CallbackQuery, arg, uid string, answer func(string, bool)) {
	m := q.Message
	n, _ := strconv.Atoi(arg)
	days := b.clampDays(n)
	base := int64(days) * int64(b.cfg.PricePerDay)

	// Pesanan menunggu milik pengguna ini diganti. QR lama tetap dipantau: bila ternyata dibayar, tetap diaktifkan.
	for _, old := range b.all() {
		if old.UserID != uid || old.Status != "pending" || old.TxnID == "" {
			continue
		}
		st, err := b.check(ctx, old.ID)
		if err != nil {
			st = "pending"
		}
		if st == "paid" {
			answer(fmt.Sprintf("Pesanan #%s ternyata sudah lunas. Akses diperbarui.", old.ID), true)
			return
		}
		unlock := b.lockProc(old.ID)
		b.mut(old.ID, func(x *Order) {
			if x.Status == "pending" {
				x.Status = "cancelled"
				x.ClosedAt = nowMs()
			}
		})
		unlock()
		b.editInvoice(ctx, old.ID, fmt.Sprintf("❎ Pesanan #%s diganti pesanan baru.", old.ID), false)
		break
	}

	answer("Membuat tagihan...", false)
	id := b.newOrderID()
	t, err := b.createTxn(ctx, id, base)
	if err != nil {
		log.Printf("buy: gagal membuat transaksi: %v", err)
		_, _ = b.bot.SendMessage(ctx, m.Chat.ID, "❌ Gagal membuat tagihan. Coba lagi sebentar lagi, atau hubungi admin.", tg.SendOpts{})
		return
	}
	qr := t.qr()
	if t.ID == "" || qr == "" {
		log.Printf("buy: respons Kasera tidak memuat id/payment.qr_string (id=%q)", t.ID)
		_, _ = b.bot.SendMessage(ctx, m.Chat.ID, "❌ Gagal membuat QRIS. Coba lagi sebentar lagi, atau hubungi admin.", tg.SendOpts{})
		return
	}
	localExp := nowMs() + int64(b.cfg.TTLMin)*60*1000
	gw, gwOK := parseTime(t.ExpiresAt)
	exp, gwExp := localExp, localExp
	if gwOK {
		gwExp = gw
		if gw < localExp {
			exp = gw
		}
	}
	name := q.From.FullName()
	if q.From.Username != "" {
		name += " (@" + q.From.Username + ")"
	}
	live := true
	if t.Livemode != nil {
		live = *t.Livemode
	}
	o := &Order{
		ID: id, TxnID: t.ID, UserID: uid, Username: q.From.Username, Name: name,
		Days: days, Base: base, Fee: float64(t.Fee), Total: base,
		Status: "pending", Livemode: live, Created: nowMs(), GwExpires: gwExp, Expires: exp,
	}
	b.mu.Lock()
	b.orders[id] = o
	b.saveLocked() // simpan SEBELUM kirim ke pengguna: pembayaran tidak boleh hilang walau pengiriman gagal
	b.mu.Unlock()

	_ = b.bot.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
	png, err := qrcode.Encode(qr, qrcode.Medium, 600)
	var sent *tg.Message
	if err == nil {
		snapshot, _ := b.snap(id)
		sent, err = b.bot.SendPhoto(ctx, m.Chat.ID, png, b.invoiceText(snapshot), "HTML", invoiceButtons(snapshot))
	}
	if err != nil {
		log.Printf("buy: gagal mengirim QR: %v", err)
		b.mut(id, func(x *Order) { // tetap dipantau sampai QR kedaluwarsa, kalau-kalau sempat dibayar
			x.Status = "cancelled"
			x.ClosedAt = nowMs()
		})
		_, _ = b.bot.SendMessage(ctx, m.Chat.ID, "❌ Gagal mengirim QR. Ketik /buy untuk mencoba lagi.", tg.SendOpts{})
		return
	}
	b.mut(id, func(x *Order) {
		x.ChatID = m.Chat.ID
		x.MsgID = sent.MessageID
		x.HasPhoto = true
	})
}
