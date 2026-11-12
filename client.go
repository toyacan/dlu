package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// APIError: Telegram menjawab ok=false.
type APIError struct {
	Code       int
	Desc       string
	RetryAfter int
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram %d: %s", e.Code, e.Desc) }

type Client struct {
	token   string
	http    *http.Client
	premium *Premium
}

// New membuat klien. premium boleh nil (emoji premium mati).
func New(token string, p *Premium) *Client {
	return &Client{token: token, http: &http.Client{}, premium: p}
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// sanitize: URL Bot API memuat token; jangan sampai ikut tercetak di log.
func (c *Client) sanitize(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), c.token, "<token>"))
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return c.sanitize(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.sanitize(err)
	}
	var r apiResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("respons Telegram tidak valid (HTTP %d)", resp.StatusCode)
	}
	if !r.OK {
		return &APIError{Code: r.ErrorCode, Desc: r.Description, RetryAfter: r.Parameters.RetryAfter}
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any, out any, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.token+"/"+method, body)
	if err != nil {
		return c.sanitize(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

type P = map[string]any

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.call(ctx, "getMe", nil, &u, 0); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) GetChat(ctx context.Context, id int64) (*Chat, error) {
	var ch Chat
	if err := c.call(ctx, "getChat", P{"chat_id": id}, &ch, 0); err != nil {
		return nil, err
	}
	return &ch, nil
}

// GetUpdates: long polling.
func (c *Client) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", P{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query", "inline_query"},
	}, &ups, time.Duration(timeoutSec+15)*time.Second)
	return ups, err
}

var retryableRe = regexp.MustCompile(`(?i)emoji|entit|button|parse`)

func retryable(err error) bool { return err != nil && retryableRe.MatchString(err.Error()) }

func fill(p P, o SendOpts) {
	if o.ParseMode != "" {
		p["parse_mode"] = o.ParseMode
	}
	if o.Markup != nil {
		p["reply_markup"] = o.Markup
	}
}

// SendMessage: emoji premium dipakai otomatis; bila ditolak Telegram, dikirim ulang dengan emoji biasa.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, o SendOpts) (*Message, error) {
	send := func(text string, o SendOpts) (*Message, error) {
		p := P{"chat_id": chatID, "text": text}
		fill(p, o)
		if o.ReplyTo != 0 {
			p["reply_parameters"] = P{"message_id": o.ReplyTo, "allow_sending_without_reply": true}
		}
		var m Message
		if err := c.call(ctx, "sendMessage", p, &m, 0); err != nil {
			return nil, err
		}
		return &m, nil
	}
	if pt, po, ok := c.premium.Prep(text, o); ok {
		m, err := send(pt, po)
		if retryable(err) {
			log.Printf("emoji premium ditolak, kirim ulang tanpa emoji premium: %v", err)
			return send(text, o)
		}
		return m, err
	}
	return send(text, o)
}

func (t Target) fill(p P) {
	if t.InlineID != "" {
		p["inline_message_id"] = t.InlineID
		return
	}
	p["chat_id"] = t.ChatID
	p["message_id"] = t.MessageID
}

func (c *Client) EditMessageText(ctx context.Context, t Target, text string, o SendOpts) error {
	edit := func(text string, o SendOpts) error {
		p := P{"text": text}
		t.fill(p)
		fill(p, o)
		return c.call(ctx, "editMessageText", p, nil, 0)
	}
	if pt, po, ok := c.premium.Prep(text, o); ok {
		err := edit(pt, po)
		if retryable(err) {
			log.Printf("emoji premium ditolak (edit), ulang tanpa emoji premium: %v", err)
			return edit(text, o)
		}
		return err
	}
	return edit(text, o)
}

func (c *Client) EditMessageCaption(ctx context.Context, t Target, caption, parseMode string, markup any) error {
	p := P{"caption": caption}
	t.fill(p)
	fill(p, SendOpts{ParseMode: parseMode, Markup: markup})
	return c.call(ctx, "editMessageCaption", p, nil, 0)
}

func (c *Client) DeleteMessage(ctx context.Context, chatID int64, msgID int) error {
	return c.call(ctx, "deleteMessage", P{"chat_id": chatID, "message_id": msgID}, nil, 0)
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	p := P{"callback_query_id": id}
	if text != "" {
		p["text"] = text
	}
	if alert {
		p["show_alert"] = true
	}
	return c.call(ctx, "answerCallbackQuery", p, nil, 0)
}

func (c *Client) AnswerInlineQuery(ctx context.Context, id string, results []any, cacheTime int, personal bool) error {
	if results == nil {
		results = []any{}
	}
	return c.call(ctx, "answerInlineQuery", P{
		"inline_query_id": id,
		"results":         results,
		"cache_time":      cacheTime,
		"is_personal":     personal,
	}, nil, 0)
}

// SendPhoto mengirim gambar PNG (multipart).
func (c *Client) SendPhoto(ctx context.Context, chatID int64, png []byte, caption, parseMode string, markup any) (*Message, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	if parseMode != "" {
		_ = w.WriteField("parse_mode", parseMode)
	}
	if markup != nil {
		b, err := json.Marshal(markup)
		if err != nil {
			return nil, err
		}
		_ = w.WriteField("reply_markup", string(b))
	}
	fw, err := w.CreateFormFile("photo", "qris.png")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(png); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.token+"/sendPhoto", &buf)
	if err != nil {
		return nil, c.sanitize(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	var m Message
	if err := c.do(req, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
