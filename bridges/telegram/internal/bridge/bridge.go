package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openagentix/openagentix/bridges/telegram/internal/telegram"
)

// Agent is the conversational backend. contextID is empty on a chat's first
// turn; the returned newContextID is stored and passed on later turns.
type Agent interface {
	Send(ctx context.Context, contextID, text string) (reply, newContextID string, err error)
}

// Messenger is the subset of the Telegram client the bridge needs.
type Messenger interface {
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]telegram.Update, error)
	SendMessage(ctx context.Context, chatID int64, text string) error
	SendTyping(ctx context.Context, chatID int64) error
	GetMe(ctx context.Context) (username string, err error)
}

type Bridge struct {
	Telegram           Messenger
	Agent              Agent
	Store              ContextStore
	Log                *slog.Logger
	AgentTimeout       time.Duration
	PollTimeout        int              // seconds for Telegram long polling
	Backoff            time.Duration    // first retry delay after a failed poll
	MaxBackoff         time.Duration    // cap for exponential growth; <=Backoff means constant
	Allowed            map[int64]bool   // chats allowed to talk to the agent
	MaxConcurrentTurns int              // max agent turns in flight across chats; <=0 means 8
	ShutdownGrace      time.Duration    // how long Run waits for in-flight turns after cancel; 0 cancels them at once
	Now                func() time.Time // clock for health checks; nil means time.Now
	username           string           // the bot's own username, lowercased; set once by Run
	lastPoll           atomic.Int64     // unix nanos of the last successful getUpdates
	AllowAll           bool             // explicit opt-in: accept every chat; otherwise empty Allowed rejects all
}

const defaultMaxTurns = 8

// dispatcher runs turns concurrently across chats, strictly in order within a
// chat, with at most cap(sem) turns in flight.
type dispatcher struct {
	b       *Bridge
	ctx     context.Context // run context: stops queued work from starting
	turnCtx context.Context // survives run cancellation so in-flight turns can finish
	sem     chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	queues  map[int64][]telegram.Update // chats with a worker; value is the pending backlog
}

func (d *dispatcher) dispatch(u telegram.Update) {
	chat := u.Message.Chat.ID
	d.mu.Lock()
	defer d.mu.Unlock()
	if q, busy := d.queues[chat]; busy {
		d.queues[chat] = append(q, u)
		return
	}
	d.queues[chat] = nil
	d.wg.Add(1)
	go d.worker(chat, u)
}

func (d *dispatcher) worker(chat int64, u telegram.Update) {
	defer d.wg.Done()
	for {
		select {
		case d.sem <- struct{}{}:
			if d.ctx.Err() == nil {
				d.b.handle(d.turnCtx, u)
			}
			<-d.sem
		case <-d.ctx.Done():
		}
		d.mu.Lock()
		q := d.queues[chat]
		if len(q) == 0 || d.ctx.Err() != nil {
			delete(d.queues, chat)
			d.mu.Unlock()
			return
		}
		u, d.queues[chat] = q[0], q[1:]
		d.mu.Unlock()
	}
}

func (b *Bridge) Run(ctx context.Context) error {
	me, err := b.Telegram.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("resolving bot username via getMe: %w", err)
	}
	b.username = strings.ToLower(me)
	n := b.MaxConcurrentTurns
	if n <= 0 {
		n = defaultMaxTurns
	}
	turnCtx, cancelTurns := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelTurns()
	d := &dispatcher{b: b, ctx: ctx, turnCtx: turnCtx, sem: make(chan struct{}, n), queues: map[int64][]telegram.Update{}}
	defer b.drain(d, cancelTurns)

	var offset int64
	failures := 0 // consecutive getUpdates failures
	b.markPoll()  // startup counts as fresh until the first poll returns
	for ctx.Err() == nil {
		ups, err := b.Telegram.GetUpdates(ctx, offset, b.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var apiErr *telegram.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
				return fmt.Errorf("telegram rejected the bot token (401): %w", err)
			}
			wait := nextBackoff(b.Backoff, b.MaxBackoff, failures)
			failures++
			b.Log.Error("telegram getUpdates failed; backing off", "err", err, "backoff", wait)
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			continue
		}
		failures = 0
		b.markPoll()
		for _, u := range ups {
			if ctx.Err() != nil {
				break // shutting down: leave the rest of the batch unprocessed
			}
			// The offset lives only in memory and advances when an update is
			// dispatched, not when its turn finishes. After a restart Telegram
			// redelivers whatever it had not seen confirmed (the last batch), so
			// delivery is at-least-once; updates still queued at shutdown may be
			// dropped. A persisted offset would be needed to change either.
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			d.dispatch(u)
		}
	}
	return ctx.Err()
}

// drain waits for in-flight turns up to ShutdownGrace, then cancels them.
func (b *Bridge) drain(d *dispatcher, cancelTurns context.CancelFunc) {
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		return
	case <-time.After(b.ShutdownGrace):
	}
	cancelTurns()
	<-done
}

func (b *Bridge) handle(ctx context.Context, u telegram.Update) {
	if u.Message == nil || u.Message.Text == "" {
		return
	}
	chat := u.Message.Chat.ID
	if !b.AllowAll && !b.Allowed[chat] {
		b.Log.Debug("ignoring message from chat not in allowlist", "chat_id", chat)
		return
	}
	text := u.Message.Text
	if b.isNewCommand(text) {
		b.Store.Delete(chat)
		if err := b.Telegram.SendMessage(ctx, chat, "Started a new conversation."); err != nil {
			b.Log.Error("telegram sendMessage failed", "chat_id", chat, "err", err)
		}
		return
	}
	// Best effort: turns take seconds, so show "typing..." first. A failure
	// (or slow call) is bounded and never blocks the turn.
	tctx, tcancel := context.WithTimeout(ctx, typingTimeout)
	if err := b.Telegram.SendTyping(tctx, chat); err != nil {
		b.Log.Warn("telegram sendChatAction failed", "chat_id", chat, "err", err)
	}
	tcancel()
	actx, cancel := context.WithTimeout(ctx, b.AgentTimeout)
	defer cancel()
	reply, newCtx, err := b.Agent.Send(actx, b.Store.Get(chat), text)
	if err != nil {
		b.Log.Error("agent call failed", "chat_id", chat, "err", err)
		reply = genericFailure
	} else if newCtx != "" {
		b.Store.Set(chat, newCtx)
	}
	if err := b.Telegram.SendMessage(ctx, chat, reply); err != nil {
		b.Log.Error("telegram sendMessage failed", "chat_id", chat, "err", err)
	}
}

const typingTimeout = 3 * time.Second

const genericFailure = "Sorry, something went wrong. Please try again."

// isNewCommand matches "/new" and "/new@<this bot's username>" (any case).
// A command addressed to another bot in a group is not ours to act on.
func (b *Bridge) isNewCommand(text string) bool {
	cmd, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	name, target, addressed := strings.Cut(cmd, "@")
	return name == "/new" && (!addressed || strings.EqualFold(target, b.username))
}

// nextBackoff is base doubled once per earlier consecutive failure, capped at limit.
func nextBackoff(base, limit time.Duration, failures int) time.Duration {
	d := base
	for range failures {
		if d >= limit {
			break
		}
		d *= 2
	}
	return max(base, min(d, limit))
}
