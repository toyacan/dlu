// checksession: cek apakah sesi lama versi Node (data/<id>/session.txt) bisa dibaca dan dikonversi.
// Tidak mencetak kunci apa pun. Pakai:  go run ./cmd/checksession data/123456789/session.txt
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"cu/internal/config"
	"cu/internal/secure"
	"cu/internal/ubot"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Pakai: go run ./cmd/checksession data/<id>/session.txt")
		os.Exit(2)
	}
	_ = config.LoadDotenv(".env")
	path := os.Args[1]
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("gagal membaca file:", err)
		os.Exit(1)
	}
	dataDir := filepath.Dir(filepath.Dir(path))
	text, rewrite, err := secure.New(dataDir, config.String("SESSION_KEY", "")).Open(string(raw))
	if err != nil {
		fmt.Println("DEKRIPSI GAGAL:", err)
		os.Exit(1)
	}
	fmt.Printf("dekripsi OK (teks polos lama: %v)\n", rewrite)
	dc, addr, port, key, err := ubot.ParseGramJS(text)
	if err != nil {
		fmt.Println("FORMAT SESI TIDAK DIKENAL:", err)
		os.Exit(1)
	}
	fmt.Printf("format GramJS OK: DC %d, alamat %s, port %d, panjang kunci %d byte\n", dc, addr, port, len(key))
}
