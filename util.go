// Package util: fungsi kecil bersama (format tanggal WIB, rupiah, konversi ID).
package util

import (
	"strconv"
	"strings"
	"time"
)

// Indonesia (WIB) tidak memakai DST, jadi zona tetap cukup dan tidak butuh tzdata di server.
var wib = time.FixedZone("WIB", 7*3600)

// FmtDate: milidetik epoch -> "12/11/2026, 13.22.10" (WIB), setara toLocaleString("id-ID") di Node.
func FmtDate(ms int64) string {
	return time.UnixMilli(ms).In(wib).Format("02/01/2006, 15.04.05")
}

// Rp: 3000 -> "Rp 3.000".
func Rp(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	out := "Rp " + b.String()
	if neg {
		out = "Rp -" + b.String()
	}
	return out
}

// ID mengubah ID teks ("123456") menjadi int64; 0 bila bukan angka.
func ID(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func Itoa(n int64) string { return strconv.FormatInt(n, 10) }
