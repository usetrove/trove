package plugin

import (
	"regexp"
	"sync"
	"time"
)

// Event is one row the server hands to a Recorder. Metadata only (CLAUDE.md rule
// 7): the session key is already hashed, and every string field has passed the
// checks in sanitize.go, so prompt text, paths and credentials cannot get in.
type Event struct {
	TS             time.Time
	SessionKey     string
	Harness        string
	Login          string
	Provider       string
	Model          string
	Kind           string
	Optimizer      string
	State          string
	Bytes          int64
	Tokens         int64
	OverheadTokens int64
	// Usage fields, set only on kind "usage"; nil when the harness did not report them.
	InTokens, OutTokens, CacheReadTokens, CacheWriteTokens *int64
	CostUSD                                                *float64
	Detail                                                 string
}

// Recorder receives events. Record must not block: the server calls it in the
// request path. A nil Recorder records nothing.
type Recorder interface{ Record(Event) }

// Logins a plugin may report. The plugin classifies the login from non-secret
// fields; Trove never sees a credential (rules 2, 3 and 10).
const (
	LoginAPIKey  = "api_key"
	LoginCopilot = "copilot"
	// LoginUnknown covers every subscription login and anything the plugin could
	// not classify, including a ChatGPT login (held; see docs/opencode.md). It is
	// shadow-only: an optimizer counts what it would do and changes nothing.
	LoginUnknown = "subscription_or_unknown"
)

// Eligible reports whether optimizers may change content for this login.
func Eligible(login string) bool { return login == LoginAPIKey || login == LoginCopilot }

// NormLogin returns login if it is one of the three known values, else LoginUnknown.
func NormLogin(login string) string {
	switch login {
	case LoginAPIKey, LoginCopilot:
		return login
	}
	return LoginUnknown
}

var (
	harnessRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	// nameRE admits provider and model identifiers (anthropic, claude-sonnet-5-5,
	// github-copilot, gpt-5.1:high, accounts/x/models/y) and nothing that could
	// carry free text.
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,95}$`)
	detailOK = map[string]bool{"tool_error": true, "retry": true, "abort": true, "session_end": true, "compaction": true}
)

func cleanName(s string) string {
	if nameRE.MatchString(s) {
		return s
	}
	return ""
}

// Batcher is a Recorder that writes in batches from a background goroutine, so
// recording never sits in the path of a tool call. Sink gets each batch; an
// error from Sink drops that batch (a lost measurement, never a failed call).
type Batcher struct {
	sink  func([]Event) error
	ch    chan Event
	done  chan struct{}
	every time.Duration
	max   int
	once  sync.Once
}

// NewBatcher starts a Batcher. Close flushes what is left.
func NewBatcher(sink func([]Event) error) *Batcher {
	b := &Batcher{sink: sink, ch: make(chan Event, 1024), done: make(chan struct{}), every: 500 * time.Millisecond, max: 128}
	go b.run()
	return b
}

// Record queues e, or drops it if the queue is full.
func (b *Batcher) Record(e Event) {
	select {
	case b.ch <- e:
	default:
	}
}

// Close stops the Batcher after writing every queued event.
func (b *Batcher) Close() {
	b.once.Do(func() { close(b.ch) })
	<-b.done
}

func (b *Batcher) run() {
	defer close(b.done)
	t := time.NewTicker(b.every)
	defer t.Stop()
	var buf []Event
	flush := func() {
		if len(buf) > 0 {
			_ = b.sink(buf)
			buf = nil
		}
	}
	for {
		select {
		case e, ok := <-b.ch:
			if !ok {
				flush()
				return
			}
			buf = append(buf, e)
			if len(buf) >= b.max {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}
