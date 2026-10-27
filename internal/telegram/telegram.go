// Package telegram is a minimal Telegram Bot API client (long polling only).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const DefaultBaseURL = "https://api.telegram.org"

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboard struct {
	Rows [][]Button `json:"inline_keyboard"`
}

// Client talks to the Bot API. The token never appears in returned errors.
type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(token, baseURL string, hc *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{base: baseURL, token: token, http: hc}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (c *Client) do(ctx context.Context, method, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, body)
	if err != nil {
		return errors.New("telegram " + method + ": cannot build request")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error embeds the URL, and therefore the token: keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	var r apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d, bad response", method, resp.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %d %s", method, r.ErrorCode, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.do(ctx, method, "application/json", bytes.NewReader(b), out)
}

// GetUpdates long-polls for up to timeout seconds.
func (c *Client) GetUpdates(ctx context.Context, offset int, timeout time.Duration) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout.Seconds()),
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

type sendParams struct {
	ChatID                int64           `json:"chat_id"`
	MessageID             int             `json:"message_id,omitempty"`
	Text                  string          `json:"text"`
	ParseMode             string          `json:"parse_mode"`
	DisableWebPagePreview bool            `json:"disable_web_page_preview"`
	ReplyMarkup           *InlineKeyboard `json:"reply_markup,omitempty"`
}

// SendMessage sends an HTML-formatted message and returns its message id.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, kb *InlineKeyboard) (int, error) {
	var m Message
	err := c.call(ctx, "sendMessage", sendParams{ChatID: chatID, Text: text, ParseMode: "HTML", DisableWebPagePreview: true, ReplyMarkup: kb}, &m)
	return m.MessageID, err
}

// EditMessage replaces the text (and keyboard; nil removes it) of a message.
func (c *Client) EditMessage(ctx context.Context, chatID int64, msgID int, text string, kb *InlineKeyboard) error {
	if kb == nil {
		kb = &InlineKeyboard{Rows: [][]Button{}}
	}
	return c.call(ctx, "editMessageText", sendParams{ChatID: chatID, MessageID: msgID, Text: text, ParseMode: "HTML", DisableWebPagePreview: true, ReplyMarkup: kb}, nil)
}

func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

func (c *Client) SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	fw, err := w.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	_, _ = fw.Write(data)
	if err := w.Close(); err != nil {
		return err
	}
	return c.do(ctx, "sendDocument", w.FormDataContentType(), &buf, nil)
}

func (c *Client) SetMyCommands(ctx context.Context, cmds []Command) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}
