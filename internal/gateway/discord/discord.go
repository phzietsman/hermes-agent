// Package discord is the Discord adapter over discordgo: a websocket gateway connection for inbound messages and
// the REST API for replies and edits.
//
// The bot needs the "Message Content" privileged intent switched on in the Discord developer portal; without it
// Discord sends empty text for messages that do not mention the bot.
package discord

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/gateway"
)

const (
	// TokenEnv names the secret (in <home>/.env or the environment).
	TokenEnv = "DISCORD_BOT_TOKEN"
	// maxLen is Discord's limit (2000 characters) with a margin.
	maxLen = 1990
	// readyTimeout bounds the wait for the gateway to confirm the login.
	readyTimeout = 30 * time.Second
)

// Adapter is the Discord platform.
type Adapter struct {
	token string

	mu      sync.Mutex
	session *discordgo.Session
	botID   string
	handle  func(gateway.Event)
}

// New builds the adapter: the token comes from the secrets.
func New(_ *config.Config, secrets *config.Secrets) (gateway.Adapter, error) {
	tok, _ := secrets.Lookup(TokenEnv)
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return nil, fmt.Errorf("%s is not set: create a bot in the Discord developer portal (with the Message Content intent on) and put its token in <home>/.env", TokenEnv)
	}
	return &Adapter{token: tok}, nil
}

// Name implements gateway.Adapter.
func (a *Adapter) Name() string { return "discord" }

// MaxLen implements gateway.Adapter.
func (a *Adapter) MaxLen() int { return maxLen }

// Start connects to the Discord gateway and returns once the connection is ready.
func (a *Adapter) Start(ctx context.Context, handle func(gateway.Event)) error {
	s, err := discordgo.New("Bot " + a.token)
	if err != nil {
		return scrub(err, a.token)
	}
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent
	s.ShouldReconnectOnError = true
	ready := make(chan struct{})
	var once sync.Once
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		a.mu.Lock()
		a.botID = r.User.ID
		a.mu.Unlock()
		once.Do(func() { close(ready) })
	})
	s.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if ev, ok := a.event(m.Message); ok {
			handle(ev)
		}
	})
	// Open blocks until the handshake finishes and is not context-aware, so it runs on its own goroutine: a gateway
	// that never answers must not hang the process, and a connection that arrives late is closed, not leaked.
	opened := make(chan error, 1)
	go func() { opened <- s.Open() }()
	openReturned := false
	abandon := func(cause error) error {
		go func() {
			if !openReturned {
				<-opened
			}
			_ = s.Close()
		}()
		return cause
	}
	pending := opened
	timeout := time.After(readyTimeout)
	for waiting := true; waiting; {
		select {
		case err := <-pending:
			if err != nil {
				return scrub(err, a.token)
			}
			openReturned, pending = true, nil // keep waiting for READY
		case <-ready:
			waiting = false
		case <-ctx.Done():
			return abandon(ctx.Err())
		case <-timeout:
			return abandon(errors.New("discord: the gateway did not confirm the login in time (check the token and intents)"))
		}
	}
	a.mu.Lock()
	a.session, a.handle = s, handle
	a.mu.Unlock()
	return nil
}

// Stop closes the connection.
func (a *Adapter) Stop(context.Context) error {
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}

// scrub keeps the token out of an error: it can appear in request headers echoed by a transport error.
func scrub(err error, token string) error {
	return errors.New(strings.ReplaceAll(err.Error(), token, "***"))
}

var mentionRe = regexp.MustCompile(`<@!?(\d+)>`)

// aliases lets people type "!new" where Discord would swallow "/new" as an application command.
var aliases = map[string]bool{"new": true, "stop": true, "status": true, "help": true, "yolo": true}

// event turns a message into a gateway event, or reports false for what the bot should not see (its own and other
// bots' messages, empty text).
func (a *Adapter) event(m *discordgo.Message) (gateway.Event, bool) {
	if m == nil || m.Author == nil || m.Author.Bot {
		return gateway.Event{}, false
	}
	a.mu.Lock()
	botID := a.botID
	a.mu.Unlock()
	if botID != "" && m.Author.ID == botID {
		return gateway.Event{}, false
	}
	dm := m.GuildID == ""
	addressed := dm
	for _, u := range m.Mentions {
		if u != nil && u.ID == botID {
			addressed = true
		}
	}
	if m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil && m.ReferencedMessage.Author.ID == botID {
		addressed = true
	}
	text := m.Content
	if botID != "" {
		text = mentionRe.ReplaceAllStringFunc(text, func(s string) string {
			if mentionRe.FindStringSubmatch(s)[1] == botID {
				return ""
			}
			return s
		})
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "!") {
		if cmd, rest, _ := strings.Cut(text[1:], " "); aliases[strings.ToLower(cmd)] {
			text = strings.TrimSpace("/" + strings.ToLower(cmd) + " " + rest)
		}
	}
	if text == "" {
		return gateway.Event{}, false
	}
	name := m.Author.Username
	if m.Member != nil && m.Member.Nick != "" {
		name = m.Member.Nick
	}
	return gateway.Event{Platform: "discord", Target: gateway.Target{ChatID: m.ChannelID}, UserID: m.Author.ID, UserName: name,
		Text: text, MessageID: m.ID, DM: dm, Addressed: addressed}, true
}

// noPings stops model output from pinging anyone: @everyone, @here, roles and users in a reply are text, never a
// notification.
var noPings = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}

func (a *Adapter) sess() (*discordgo.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return nil, errors.New("discord: not connected")
	}
	return a.session, nil
}

// Send implements gateway.Adapter.
func (a *Adapter) Send(_ context.Context, to gateway.Target, text string) (string, error) {
	s, err := a.sess()
	if err != nil {
		return "", err
	}
	m, err := s.ChannelMessageSendComplex(to.ChatID, &discordgo.MessageSend{Content: text, AllowedMentions: noPings})
	if err != nil {
		return "", scrub(err, a.token)
	}
	return m.ID, nil
}

// Edit implements gateway.Adapter.
func (a *Adapter) Edit(_ context.Context, to gateway.Target, msgID, text string) error {
	s, err := a.sess()
	if err != nil {
		return err
	}
	content := text
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: msgID, Channel: to.ChatID, Content: &content, AllowedMentions: noPings})
	return scrubNil(err, a.token)
}

// Typing implements gateway.Adapter.
func (a *Adapter) Typing(_ context.Context, to gateway.Target) error {
	s, err := a.sess()
	if err != nil {
		return err
	}
	return scrubNil(s.ChannelTyping(to.ChatID), a.token)
}

func scrubNil(err error, token string) error {
	if err == nil {
		return nil
	}
	return scrub(err, token)
}

var _ gateway.Adapter = (*Adapter)(nil)
