// Package config: baca .env (format sama dengan dotenv di Node) dan ambil nilainya.
package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// LoadDotenv memuat file .env. Variabel yang sudah ada di lingkungan tidak ditimpa.
func LoadDotenv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		} else if j := strings.Index(val, " #"); j >= 0 {
			val = strings.TrimSpace(val[:j])
		}
		if _, ok := os.LookupEnv(key); !ok {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}

func String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func Int(key string, def int) int {
	if n, err := strconv.Atoi(String(key, "")); err == nil {
		return n
	}
	return def
}

func Float(key string, def float64) float64 {
	if n, err := strconv.ParseFloat(String(key, ""), 64); err == nil {
		return n
	}
	return def
}
