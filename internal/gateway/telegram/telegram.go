// Package telegram is the Telegram Bot API adapter: long-polling getUpdates for inbound messages, sendMessage and
// editMessageText for replies. It speaks the HTTP API directly (the surface is five methods), so there is no client
// library to track and every wire detail is testable against a fake Bot API server.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/gateway"
	"github.com/phzietsman/hermes-agent/internal/redact"
)

const (
	// TokenEnv names the secret (in <home>/.env or the environment).
	TokenEnv = "TELEGRAM_BOT_TOKEN"
	// maxLen is below Telegram's 4096 limit: it counts UTF-16 units, and an astral character is two.
	maxLen         = 4000
	defaultBaseURL = "https://api.telegram.org"
)

// Adapter is the Telegram platform.
type Adapter struct {
	token   string
	baseURL string
	http    *http.Client
	// PollTimeout is the long-poll wait; Backoff is the pause after a failed poll (doubling to MaxBackoff).
	PollTimeout time.Duration
	Backoff     time.Duration
	MaxBackoff  time.Duration

	mu       sync.Mutex
	botID    int64
	botName  string
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	offset   int64
	handle   func(gateway.Event)
	stopping bool
}

// New builds the adapter from the profile: the token from the secrets, gateway.platforms.telegram.base_url and
// poll_timeout (seconds) from config.
func New(cfg *config.Config, secrets *config.Secrets) (gateway.Adapter, error) {
	tok, _ := secrets.Lookup(TokenEnv)
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return nil, fmt.Errorf("%s is not set: create a bot with @BotFather and put its token in <home>/.env", TokenEnv)
	}
	a := &Adapter{
		token: tok, baseURL: strings.TrimRight(cfg.String("gateway.platforms.telegram.base_url", defaultBaseURL), "/"),
		PollTimeout: time.Duration(cfg.Int("gateway.platforms.telegram.poll_timeout", 30)) * time.Second,
		Backoff:     time.Second, MaxBackoff: 30 * time.Second,
	}
	return a, nil
}

func (a *Adapter) client() *http.Client {
	if a.http != nil {
		return a.http
	}
	// Long polls outlive any fixed overall timeout; each call carries its own context deadline instead.
	return &http.Client{}
}

// Name implements gateway.Adapter.
func (a *Adapter) Name() string { return "telegram" }

// MaxLen implements gateway.Adapter.
func (a *Adapter) MaxLen() int { return maxLen }

// apiError is a failed Bot API call.
type apiError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram %s: %s (code %d)", e.Method, e.Description, e.Code)
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call posts a method with a JSON body and decodes result into out. A 429 is retried once after the wait Telegram asks for.
func (a *Adapter) call(ctx context.Context, method string, params map[string]any, out any) error {
	for attempt := 0; ; attempt++ {
		body, _ := json.Marshal(params)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/bot"+a.token+"/"+method, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.client().Do(req)
		if err != nil {
			// The token is part of the URL: never let it into an error that reaches a log or the chat.
			return fmt.Errorf("telegram %s: %s", method, strings.ReplaceAll(redact.Text(err.Error()), a.token, "***"))
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("telegram %s: unreadable response (HTTP %d)", method, resp.StatusCode)
		}
		if env.OK {
			if out != nil {
				return json.Unmarshal(env.Result, out)
			}
			return nil
		}
		ae := &apiError{Method: method, Code: env.ErrorCode, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
		if ae.Code == http.StatusTooManyRequests && attempt == 0 && ae.RetryAfter > 0 && ae.RetryAfter <= 60 {
			select {
			case <-time.After(time.Duration(ae.RetryAfter) * time.Second):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return ae
	}
}

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type tgMessage struct {
	MessageID      int64   `json:"message_id"`
	ThreadID       int64   `json:"message_thread_id"`
	IsTopicMessage bool    `json:"is_topic_message"`
	From           *tgUser `json:"from"`
	Chat           struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text     string `json:"text"`
	Caption  string `json:"caption"`
	Entities []struct {
		Type   string  `json:"type"`
		Offset int     `json:"offset"`
		Length int     `json:"length"`
		User   *tgUser `json:"user"`
	} `json:"entities"`
	ReplyTo *struct {
		From *tgUser `json:"from"`
	} `json:"reply_to_message"`
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

// Start verifies the token with getMe and begins long polling.
func (a *Adapter) Start(ctx context.Context, handle func(gateway.Event)) error {
	var me tgUser
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.call(cctx, "getMe", map[string]any{}, &me); err != nil {
		return err
	}
	pctx, pcancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.botID, a.botName, a.handle, a.cancel, a.stopping = me.ID, strings.ToLower(me.Username), handle, pcancel, false
	a.mu.Unlock()
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.poll(pctx) }()
	return nil
}

// Stop ends the poll loop.
func (a *Adapter) Stop(context.Context) error {
	a.mu.Lock()
	a.stopping = true
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.wg.Wait()
	return nil
}

func (a *Adapter) poll(ctx context.Context) {
	backoff := a.Backoff
	for ctx.Err() == nil {
		a.mu.Lock()
		offset := a.offset
		a.mu.Unlock()
		secs := int(a.PollTimeout / time.Second)
		pctx, cancel := context.WithTimeout(ctx, a.PollTimeout+15*time.Second)
		var updates []tgUpdate
		err := a.call(pctx, "getUpdates", map[string]any{"offset": offset, "timeout": secs, "allowed_updates": []string{"message"}}, &updates)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("telegram poll failed", "err", err, "retry_in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, a.MaxBackoff)
			continue
		}
		backoff = a.Backoff
		for _, u := range updates {
			a.mu.Lock()
			if u.UpdateID >= a.offset {
				a.offset = u.UpdateID + 1 // acknowledged: Telegram will not send it again
			}
			a.mu.Unlock()
			if ev, ok := a.event(u); ok {
				a.mu.Lock()
				h := a.handle
				a.mu.Unlock()
				h(ev)
			}
		}
	}
}

// event turns an update into a gateway event, or reports false for what the bot should not see: its own and other
// bots' messages, edits, and messages with no text.
func (a *Adapter) event(u tgUpdate) (gateway.Event, bool) {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot {
		return gateway.Event{}, false
	}
	text := m.Text
	entities := m.Entities
	if text == "" {
		text = m.Caption
	}
	if strings.TrimSpace(text) == "" {
		return gateway.Event{}, false
	}
	a.mu.Lock()
	botName, botID := a.botName, a.botID
	a.mu.Unlock()
	dm := m.Chat.Type == "private"
	addressed := dm
	// A mention of the bot, by @username (an entity, or a bare word in the text) or by a text_mention of its id.
	cleaned := text
	if !dm && botName != "" {
		lower := strings.ToLower(text)
		if strings.Contains(lower, "@"+botName) {
			addressed = true
			cleaned = stripMention(text, botName)
		}
		for _, e := range entities {
			if e.Type == "text_mention" && e.User != nil && e.User.ID == botID {
				addressed = true
			}
		}
		if m.ReplyTo != nil && m.ReplyTo.From != nil && m.ReplyTo.From.ID == botID {
			addressed = true
		}
	}
	name := m.From.Username
	if name == "" {
		name = m.From.FirstName
	}
	ev := gateway.Event{
		Platform: "telegram", Target: gateway.Target{ChatID: strconv.FormatInt(m.Chat.ID, 10)},
		UserID: strconv.FormatInt(m.From.ID, 10), UserName: name, Text: strings.TrimSpace(cleaned),
		MessageID: strconv.FormatInt(m.MessageID, 10), DM: dm, Addressed: addressed,
	}
	if m.IsTopicMessage && m.ThreadID != 0 {
		ev.ThreadID = strconv.FormatInt(m.ThreadID, 10)
	}
	// "/stop@mybot" in a group is the command "/stop".
	if strings.HasPrefix(ev.Text, "/") && botName != "" {
		ev.Text = strings.Replace(ev.Text, "@"+botName, "", 1)
	}
	return ev, true
}

// stripMention removes "@botname" (any case) from text.
func stripMention(text, botName string) string {
	lower, tag := strings.ToLower(text), "@"+botName
	var out strings.Builder
	for {
		i := strings.Index(lower, tag)
		if i < 0 {
			out.WriteString(text)
			return out.String()
		}
		out.WriteString(text[:i])
		text, lower = text[i+len(tag):], lower[i+len(tag):]
	}
}

func threadParam(to gateway.Target, params map[string]any) {
	if to.ThreadID != "" {
		if n, err := strconv.ParseInt(to.ThreadID, 10, 64); err == nil {
			params["message_thread_id"] = n
		}
	}
}

func chatParam(to gateway.Target) any {
	if n, err := strconv.ParseInt(to.ChatID, 10, 64); err == nil {
		return n
	}
	return to.ChatID // "@channelname"
}

// Send implements gateway.Adapter. Text goes out plain: Markdown dialects reject unbalanced characters, and a message
// that fails to parse is a message the person never sees.
func (a *Adapter) Send(ctx context.Context, to gateway.Target, text string) (string, error) {
	params := map[string]any{"chat_id": chatParam(to), "text": text}
	threadParam(to, params)
	var res struct {
		MessageID int64 `json:"message_id"`
	}
	if err := a.call(ctx, "sendMessage", params, &res); err != nil {
		return "", err
	}
	return strconv.FormatInt(res.MessageID, 10), nil
}

// Edit implements gateway.Adapter. Editing to the text already shown is not an error to Telegram's callers here.
func (a *Adapter) Edit(ctx context.Context, to gateway.Target, msgID, text string) error {
	id, err := strconv.ParseInt(msgID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad message id %q", msgID)
	}
	err = a.call(ctx, "editMessageText", map[string]any{"chat_id": chatParam(to), "message_id": id, "text": text}, nil)
	var ae *apiError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

// Typing implements gateway.Adapter.
func (a *Adapter) Typing(ctx context.Context, to gateway.Target) error {
	params := map[string]any{"chat_id": chatParam(to), "action": "typing"}
	threadParam(to, params)
	return a.call(ctx, "sendChatAction", params, nil)
}

var _ gateway.Adapter = (*Adapter)(nil)
