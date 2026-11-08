package gateway

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// stream shows a reply as it is written: the first text creates a message, later text edits it in place (throttled,
// since platforms rate-limit edits), a reply longer than the platform allows continues in a new message, and a tool
// call closes the current message so the text after it starts a fresh one.
type stream struct {
	ad       Adapter
	to       Target
	interval time.Duration
	now      func() time.Time
	ctx      context.Context

	mu       sync.Mutex
	done     string // text of this segment already completed in earlier messages (a reply that outgrew one message)
	buf      string // text of the message being written
	shown    string // what the platform currently shows for it
	msgID    string
	lastEdit time.Time
	sent     bool // anything was delivered this turn
}

func newStream(ctx context.Context, ad Adapter, to Target, interval time.Duration, now func() time.Time) *stream {
	return &stream{ctx: ctx, ad: ad, to: to, interval: interval, now: now}
}

// Append adds streamed text.
func (s *stream) Append(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf += t
	s.flushLocked(false)
}

// Break ends the current message: whatever comes next is a new one.
func (s *stream) Break() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked(true)
	s.buf, s.shown, s.msgID, s.done = "", "", "", ""
}

// Sent reports whether anything reached the chat this turn.
func (s *stream) Sent() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.sent }

// Finish makes the last message say exactly final (the model's answer): an edit when text was streamed, a plain
// send when nothing was. An empty final just flushes what is pending.
func (s *stream) Finish(final string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(final) != "" {
		// The earlier messages of a long reply already show the start of final; only the tail is still to write.
		if rest, ok := strings.CutPrefix(final, s.done); ok {
			s.buf = rest
		} else {
			s.buf, s.done = final, ""
		}
	}
	s.flushLocked(true)
}

func (s *stream) flushLocked(force bool) {
	if strings.TrimSpace(s.buf) == "" || s.buf == s.shown {
		return
	}
	if !force && s.msgID != "" && s.now().Sub(s.lastEdit) < s.interval {
		return
	}
	if !force && s.msgID == "" && utf8.RuneCountInString(s.buf) < 1 {
		return
	}
	max := s.ad.MaxLen()
	for {
		pieces := splitText(s.buf, max)
		head := pieces[0]
		if s.msgID == "" {
			id, err := s.ad.Send(s.ctx, s.to, head)
			if err != nil {
				slog.Warn("gateway send failed", "platform", s.ad.Name(), "err", err)
				return
			}
			s.msgID, s.sent = id, true
		} else if head != s.shown {
			if err := s.ad.Edit(s.ctx, s.to, s.msgID, head); err != nil {
				// An edit can fail for a message that vanished or did not change: say it again as a new message.
				id, err2 := s.ad.Send(s.ctx, s.to, head)
				if err2 != nil {
					slog.Warn("gateway edit failed", "platform", s.ad.Name(), "err", err, "resend", err2)
					return
				}
				s.msgID = id
			}
		}
		s.shown, s.lastEdit = head, s.now()
		if len(pieces) == 1 {
			return
		}
		// The reply outgrew one message: this one is complete, the rest continues in the next.
		s.buf = strings.TrimPrefix(s.buf, head)
		s.done += head
		s.shown, s.msgID = "", ""
		if strings.TrimSpace(s.buf) == "" {
			return
		}
	}
}
