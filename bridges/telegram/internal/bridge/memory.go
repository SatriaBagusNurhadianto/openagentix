package bridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/openagentix/openagentix/bridges/telegram/internal/memory"
)

// Memories is what the bridge needs from kagent's memory service, reached through the A2A gate (ADR 0006, round 3):
// what is stored for one user id, and deleting all of it. The bridge passes the chat's own user id (prefix + chat id);
// the gate checks it against the bridge's rule.
type Memories interface {
	List(ctx context.Context, userID string) ([]memory.Item, error)
	Delete(ctx context.Context, userID string) error
}

const (
	memoryCallTimeout = 20 * time.Second
	maxListedItems    = 10
	maxItemRunes      = 200
	maxReplyRunes     = 3500 // below the 4096 a Telegram message holds
	memoryFailure     = "Sorry, I could not do that just now. Please try again."
)

// command splits "/name@bot arg arg" and reports whether it is addressed to this bot (or to none).
func (b *Bridge) command(text string) (name string, args []string, ours bool) {
	f := strings.Fields(text)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return "", nil, false
	}
	name, target, addressed := strings.Cut(f[0], "@")
	if addressed && !strings.EqualFold(target, b.username) {
		return "", nil, false
	}
	return name, f[1:], true
}

// handleMemoryCommand answers /memories and /forget. It returns false when the text is not one of them (or the feature is
// off), and the message then goes on to the agent as before. The user id is the chat's, never something typed in the text.
func (b *Bridge) handleMemoryCommand(ctx context.Context, chat int64, ch, trace, text string, started time.Time) bool {
	if b.Memory == nil {
		return false
	}
	name, args, ours := b.command(text)
	if !ours || (name != "/memories" && name != "/forget") {
		return false
	}
	reply, outcome, count := "", "", 0
	mctx, cancel := context.WithTimeout(ctx, memoryCallTimeout)
	defer cancel()
	userID := b.UserIDPrefix + strconv.FormatInt(chat, 10)
	switch {
	case b.UserIDPrefix == "":
		// no per-chat user id: the call would act for kagent's default user. Refuse.
		reply, outcome = memoryFailure, "memory_error"
	case name == "/memories":
		items, err := b.Memory.List(mctx, userID)
		if err != nil {
			b.logMemoryError(ch, trace, "list", err)
			reply, outcome = memoryFailure, "memory_error"
			break
		}
		reply, outcome, count = formatMemories(items), "memories", len(items)
	default: // /forget
		if len(args) != 1 || args[0] != "confirm" {
			reply, outcome = "This deletes everything I remember about you in this chat. It cannot be undone. To go on, send: /forget confirm", "forget_ask"
			break
		}
		if err := b.Memory.Delete(mctx, userID); err != nil {
			b.logMemoryError(ch, trace, "delete", err)
			reply, outcome = memoryFailure, "memory_error"
			break
		}
		reply, outcome = "Done. Everything I remembered about you is deleted. This chat's conversation history is separate: /new starts a fresh one.", "forget"
	}
	if err := b.Telegram.SendMessage(ctx, chat, reply); err != nil {
		b.Log.Error("telegram sendMessage failed", "chat_hash", ch, "trace_id", trace, "err", err)
		if outcome != "memory_error" {
			outcome = "send_error"
		}
	}
	// ids, outcome and a count; never the memories, the reply or the chat id
	b.Log.Info("turn", "trace_id", trace, "chat_hash", ch, "outcome", outcome, "memories", count, "total_ms", time.Since(started).Milliseconds())
	return true
}

// logMemoryError logs the kind of failure with numbers only. The error text is not logged: for a call that failed in an
// unexpected way it could hold anything.
func (b *Bridge) logMemoryError(ch, trace, op string, err error) {
	var me *memory.Error
	if errors.As(err, &me) {
		b.Log.Error("memory call failed", "chat_hash", ch, "trace_id", trace, "op", op, "http", me.HTTP, "grpc", me.GRPC)
		return
	}
	b.Log.Error("memory call failed", "chat_hash", ch, "trace_id", trace, "op", op)
}

// formatMemories lists at most maxListedItems, each cut to maxItemRunes characters, in at most maxReplyRunes in all.
func formatMemories(items []memory.Item) string {
	if len(items) == 0 {
		return "I have nothing stored about you in this chat."
	}
	var sb strings.Builder
	noun := "things"
	if len(items) == 1 {
		noun = "thing"
	}
	fmt.Fprintf(&sb, "I remember %d %s about you:\n", len(items), noun)
	shown := 0
	for _, it := range items {
		if shown == maxListedItems {
			break
		}
		line := fmt.Sprintf("%d. %s", shown+1, clean(it.Content, maxItemRunes))
		if !it.ExpiresAt.IsZero() {
			line += " (until " + it.ExpiresAt.UTC().Format("2006-01-02") + ")"
		}
		if len([]rune(sb.String()))+len([]rune(line))+1 > maxReplyRunes-200 { // keep room for the last lines
			break
		}
		sb.WriteString(line + "\n")
		shown++
	}
	if rest := len(items) - shown; rest > 0 {
		fmt.Fprintf(&sb, "...and %d more.\n", rest)
	}
	sb.WriteString("Send /forget to delete all of this.")
	return sb.String()
}

// clean puts text on one line without control characters and cuts it to n characters (never inside one).
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-3]) + "..."
	}
	return s
}
