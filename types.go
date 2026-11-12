// Package tg: klien Telegram Bot API tipis (hanya stdlib), setara node-telegram-bot-api yang dipakai bot.js.
package tg

import "unicode/utf16"

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Entity struct {
	Type          string `json:"type"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
	CustomEmojiID string `json:"custom_emoji_id"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	UserID      int64  `json:"user_id"`
}

type Message struct {
	MessageID      int      `json:"message_id"`
	From           *User    `json:"from"`
	Chat           Chat     `json:"chat"`
	Text           string   `json:"text"`
	Caption        string   `json:"caption"`
	Entities       []Entity `json:"entities"`
	Contact        *Contact `json:"contact"`
	ReplyToMessage *Message `json:"reply_to_message"`
}

type CallbackQuery struct {
	ID              string   `json:"id"`
	From            User     `json:"from"`
	Message         *Message `json:"message"`
	InlineMessageID string   `json:"inline_message_id"`
	Data            string   `json:"data"`
}

type InlineQuery struct {
	ID    string `json:"id"`
	From  User   `json:"from"`
	Query string `json:"query"`
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
	InlineQuery   *InlineQuery   `json:"inline_query"`
}

// Button dipakai untuk tombol inline maupun tombol keyboard biasa.
type Button struct {
	Text              string `json:"text"`
	CallbackData      string `json:"callback_data,omitempty"`
	RequestContact    bool   `json:"request_contact,omitempty"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
}

// InlineMarkup: inline_keyboard SELALU dikirim (kosong = hapus tombol saat edit).
type InlineMarkup struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

type ReplyMarkup struct {
	Keyboard        [][]Button `json:"keyboard"`
	ResizeKeyboard  bool       `json:"resize_keyboard,omitempty"`
	OneTimeKeyboard bool       `json:"one_time_keyboard,omitempty"`
}

type RemoveMarkup struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
}

// Remove adalah penanda "hapus keyboard balasan".
var Remove = RemoveMarkup{RemoveKeyboard: true}

// SendOpts: opsi kirim/edit pesan.
type SendOpts struct {
	ParseMode string
	ReplyTo   int
	Markup    any // InlineMarkup | ReplyMarkup | RemoveMarkup | nil
}

// Target menunjuk pesan yang diedit: pesan biasa (ChatID+MessageID) atau pesan inline (InlineID).
type Target struct {
	ChatID    int64
	MessageID int
	InlineID  string
}

// UTF16Slice memotong teks memakai offset/length UTF-16 (satuan yang dipakai Telegram).
func UTF16Slice(s string, off, length int) string {
	u := utf16.Encode([]rune(s))
	if off < 0 || off > len(u) {
		return ""
	}
	end := off + length
	if end > len(u) {
		end = len(u)
	}
	return string(utf16.Decode(u[off:end]))
}

// FullName: nama depan + belakang, "-" bila kosong.
func (u *User) FullName() string {
	n := u.FirstName
	if u.LastName != "" {
		if n != "" {
			n += " "
		}
		n += u.LastName
	}
	if n == "" {
		return "-"
	}
	return n
}

// FullName untuk hasil getChat.
func (c *Chat) FullName() string {
	u := User{FirstName: c.FirstName, LastName: c.LastName}
	return u.FullName()
}
