package buy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// flex: angka yang bisa datang sebagai number atau string ("3000").
type flex float64

func (f *flex) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flex(v)
	return nil
}

type txn struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Amount     flex   `json:"amount"`
	Fee        flex   `json:"fee"`
	ExternalID string `json:"external_id"`
	ExpiresAt  string `json:"expires_at"`
	PaidAt     string `json:"paid_at"`
	Livemode   *bool  `json:"livemode"`
	Payment    *struct {
		QRString string `json:"qr_string"`
	} `json:"payment"`
}

func (t *txn) qr() string {
	if t.Payment == nil {
		return ""
	}
	return t.Payment.QRString
}

func parseTime(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

func (b *Buy) kasera(ctx context.Context, method, path string, body any, idem string) (*txn, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		j, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.cfg.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, errors.New(strings.ReplaceAll(err.Error(), b.cfg.APIKey, "<key>"))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = string(raw)
			if len(msg) > 120 {
				msg = msg[:120]
			}
		}
		return nil, fmt.Errorf("Kasera %d %s: %s", resp.StatusCode, e.Error.Code, msg)
	}
	var t txn
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("Kasera: respons tidak valid: %v", err)
	}
	return &t, nil
}

func (b *Buy) getTxn(ctx context.Context, id string) (*txn, error) {
	return b.kasera(ctx, http.MethodGet, "/v1/transactions/"+url.PathEscape(id), nil, "")
}

// createTxn: Idempotency-Key = id pesanan, jadi permintaan yang terulang tidak membuat tagihan ganda.
func (b *Buy) createTxn(ctx context.Context, orderID string, amount int64) (*txn, error) {
	t, err := b.kasera(ctx, http.MethodPost, "/v1/transactions", map[string]any{
		"amount":             amount,
		"payment_methods":    []string{"qris"},
		"external_id":        orderID,
		"description":        "Langganan uBot",
		"expires_in_minutes": b.cfg.TTLMin,
	}, orderID)
	if err != nil {
		return nil, err
	}
	// "payment" bisa belum ada sesaat setelah dibuat; ambil ulang beberapa kali
	for i := 0; i < 3 && t.qr() == ""; i++ {
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
		nt, err := b.getTxn(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		t = nt
	}
	return t, nil
}
