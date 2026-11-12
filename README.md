# Port Node.js -> Go

## Status
- Tahap 1 (selesai): `.env`, enkripsi sesi (kompatibel `secure.js`), manager pengguna.
- Tahap 2 (selesai ditulis, BELUM dikompilasi): bot utama, /buy QRIS Kasera, trial, admin, /bcpm,
  emoji premium, menu `.cmd` + Roll Request via inline.
- Tahap 3a (selesai ditulis, BELUM dikompilasi): kerangka userbot gotd: sambung/putus + sambung ulang,
  sesi terenkripsi, login OTP/2FA lewat chat, logout, perintah `.ping` dan `.id`.
  Sesi lama versi Node (`data/<id>/session.txt`) dibaca & dikonversi otomatis (file lama TIDAK diubah;
  sesi Go disimpan di `session.gotd`). Perintah lain (`.list`, `.rk`, `.sk`, ...) = tahap 3b dst.

Sengaja tidak diport: `/broadcast` (kirim ke semua grup lewat userbot semua pelanggan) dan alur manual `buy-manual.js`.

## Cek sesi lama dulu (penting)
    go run ./cmd/checksession data/<id>/session.txt
Harus menampilkan "format GramJS OK". Kalau gagal, kirim pesannya (tanpa isi sesi) supaya parsernya kusesuaikan.
JANGAN jalankan userbot Node dan Go bersamaan untuk akun yang sama (Telegram akan memutus sesi: AUTH_KEY_DUPLICATED).

## Cara coba
    go mod tidy            # mengunduh gotd/td dan skip2/go-qrcode (butuh Go 1.23+ / toolchain terbaru)
    go vet ./... && go test ./...
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cu .

Format `data/users.json`, `trials.json`, `orders.json`, `bot_pm.json` sama dengan versi Node,
jadi data lama dipakai langsung. Jangan jalankan versi Node dan Go bersamaan dengan BOT_TOKEN yang sama.

## Tanpa Go di komputer: kompilasi lewat GitHub
1. Buat repositori PRIVAT di GitHub, unggah isi folder ini (JANGAN unggah `.env` atau folder `data/`).
2. Tab Actions -> "build" -> Run workflow. Satu kali jalan menampilkan semua error `go vet`, `go test`, dan build.
3. Bila hijau, unduh artifact `cu-linux-amd64` (file `cu`) lalu unggah ke panel.
Kirim log merah (salin teksnya) ke Claude untuk diperbaiki.

## Paket `internal/slot` (tahap 3b-1)
Inti pencatatan pik K/B tanpa dependensi Telegram: parsing (`k50`, `.perak`, `kall`), `.list`, `.rk`,
aturan sisi tertutup / setelah `.rk` / anti-clone, `.kk`, `.rs`, dan `picks.json` (format sama dengan Node).
Nilai harapan di tes parsing diambil dari menjalankan fungsi asli di ubot.js. Belum disambung ke perintah `.on/.list/.rk`.
