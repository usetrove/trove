package optimize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// This file holds the two tool-level optimizers that agent hooks use. They used
// to live in the hooks package; the hooks adapter now only parses events, keeps
// the ledger's storage, and renders the engine's decision as the agent expects.

// MaxHashBytes is the largest file repeat_read will hash. Bigger files are never
// judged: hashing them would slow the agent's Read.
const MaxHashBytes = 2 << 20

// bytesPerLine is a deliberately low guess used only to size a partial read
// (one with a line limit) for the token estimate.
const bytesPerLine = 60

// LenientInt reads a JSON number, or a string holding one, and is zero for
// anything else. A model can pass "10" where 10 was meant, and a hook must not
// fail on it.
type LenientInt int

// UnmarshalJSON implements json.Unmarshaler.
func (l *LenientInt) UnmarshalJSON(b []byte) error {
	*l = 0
	s := string(bytes.Trim(bytes.TrimSpace(b), `"`))
	if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 && f < 1<<31 {
		*l = LenientInt(f)
	}
	return nil
}

// ReadEntry is what is remembered about one successful read: hashes, sizes and
// times. Never the path, the contents or the raw session id.
type ReadEntry struct {
	Sum   string // first 16 hex chars of the content's sha256
	MTime int64  // modification time, ns since the epoch
	Size  int64
	// Prompt is how many distinct prompts the session had seen when the file was read.
	Prompt int
	At     int64 // unix seconds
	// BlockedSum is the content hash of a version that was denied. The next read
	// of that same version goes through, so a block can never loop.
	BlockedSum string
}

// ReadLedger is the per-session memory repeat_read needs. The adapter supplies
// the storage; the engine owns every decision about what goes in it.
type ReadLedger interface {
	Get(key string) (ReadEntry, bool)
	Put(key string, e ReadEntry)
	Delete(key string)
	// Prompts is how many distinct prompts the session has seen.
	Prompts() int
	// Save persists pending changes. If a denial cannot be saved it is not made.
	Save() error
}

// ReadKey identifies one read by a hash of the path and the requested range.
func ReadKey(path string, offset, limit int) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%s:%d:%d", hex.EncodeToString(h[:])[:16], offset, limit)
}

// FileStat describes the file a Read call is about to open.
type FileStat struct {
	Size    int64
	MTime   int64
	Sum     string
	Partial int64 // bytes the read will return when it is limited to some lines; else Size
}

// StatFile returns what a Read of the file would see, or ok=false for anything
// that should not be judged: not an absolute path, not a regular file, empty, or
// too large to hash quickly. limit is the Read's line limit (0 for none).
func StatFile(path string, limit int, hash bool) (FileStat, bool) {
	if !filepath.IsAbs(path) {
		return FileStat{}, false
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > MaxHashBytes {
		return FileStat{}, false
	}
	fs := FileStat{Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Partial: fi.Size()}
	if l := int64(limit); l > 0 && l*bytesPerLine < fs.Partial {
		fs.Partial = l * bytesPerLine
	}
	if hash {
		sum, ok := fileSum(path, fi.Size(), MaxHashBytes)
		if !ok {
			return FileStat{}, false
		}
		fs.Sum = sum
	}
	return fs, true
}

// fileSum hashes a file's contents with a cap on how much it will read: false
// means "do not use the content hash" (too large, or unreadable).
func fileSum(path string, size, maxBytes int64) (string, bool) {
	if size > maxBytes {
		return "", false
	}
	f, err := os.Open(path) //nolint:gosec // the file the agent is reading
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBytes+1))
	if err != nil || n > maxBytes {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil))[:16], true
}

// EstTokens is ceil(bytes / 3.5), Trove's usual bytes-per-token prior. It is an
// estimate and is always labelled so.
func EstTokens(b int64) int64 { return (b*2 + 6) / 7 }

type readArgs struct {
	FilePath string     `json:"file_path"`
	Offset   LenientInt `json:"offset"`
	Limit    LenientInt `json:"limit"`
	Pages    string     `json:"pages"`
}

func parseReadArgs(raw json.RawMessage) (readArgs, bool) {
	var a readArgs
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil || a.Pages != "" {
		return a, false // a paged (PDF) read is never judged
	}
	return a, true
}

// RepeatRead is the hook_repeat_read optimizer. Before a Read it checks whether
// the session already read this exact file range successfully and the file has
// the same modification time, size and content hash now. In "on" it denies the
// call once so the agent reuses what it has; the agent's retry of that same
// version always goes through. A changed or unreadable file is never blocked.
// After a successful Read it records what was read.
type RepeatRead struct{}

// Name implements Base.
func (RepeatRead) Name() string { return config.OptimizerHookRepeatRead }

// Tier implements Base: the agent can always repeat the Read to get it anyway.
func (RepeatRead) Tier() Tier { return TierRecoverable }

// Points implements Capable.
func (RepeatRead) Points() []Point { return []Point{PointToolInput, PointToolOutput} }

// OverheadTokens implements Capable: the denial text is only sent when it acts.
func (RepeatRead) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (RepeatRead) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "needs the file as it is on disk before the Read runs; the gateway only sees finished history"}, true
	}
	return Support{}, false
}

// TransformInput implements ToolInputOptimizer.
func (RepeatRead) TransformInput(_ context.Context, call ToolCall, s Session) (ToolCall, Report) {
	if call.Name != "Read" || s.Ledger == nil {
		return call, Report{}
	}
	a, ok := parseReadArgs(call.Input)
	if !ok {
		return call, Report{}
	}
	name := config.OptimizerHookRepeatRead
	rk := ReadKey(a.FilePath, int(a.Offset), int(a.Limit))
	ent, found := s.Ledger.Get(rk)
	if !found {
		return call, Report{}
	}
	fs, ok := StatFile(a.FilePath, int(a.Limit), true)
	if !ok || fs.MTime != ent.MTime || fs.Size != ent.Size || fs.Sum != ent.Sum {
		s.Ledger.Delete(rk) // changed (or gone): the next successful read re-records it
		if err := s.Ledger.Save(); err != nil {
			return call, Report{Err: "ledger"}
		}
		return call, Report{}
	}
	tokens := EstTokens(fs.Partial)
	rep := Report{Name: name, BytesRemoved: int(fs.Partial), TokensRemoved: tokens}
	if s.Mode == config.StateShadow {
		rep.Decision = &Decision{Kind: KindWouldBlock, Bytes: fs.Partial, Tokens: tokens}
		return call, rep
	}
	if ent.BlockedSum == fs.Sum {
		// This version was denied and the agent asked again: let it through, and
		// count it, because it means the block did not help.
		cleared := ent
		cleared.BlockedSum = ""
		s.Ledger.Put(rk, cleared)
		if err := s.Ledger.Save(); err != nil {
			return call, Report{Err: "ledger"}
		}
		return call, Report{Name: name, Decision: &Decision{Kind: KindRereadAnyway, Bytes: fs.Partial, Tokens: tokens}}
	}
	blocked := ent
	blocked.BlockedSum = fs.Sum
	s.Ledger.Put(rk, blocked)
	if err := s.Ledger.Save(); err != nil {
		return call, Report{Err: "ledger"} // if the denial cannot be remembered, do not deny: it could loop
	}
	rep.Decision = &Decision{Kind: KindBlocked, Bytes: fs.Partial, Tokens: tokens, Message: repeatReadMessage(ent.Prompt, ago(s.Now, ent.At))}
	return call, rep
}

// TransformOutput implements ToolOutputOptimizer. It never changes the output;
// it records a successful Read so a later repeat can be recognised. A failed
// read is never taken for one that happened.
func (RepeatRead) TransformOutput(_ context.Context, call ToolCall, out ToolOutput, s Session) (ToolOutput, Report) {
	if call.Name != "Read" || s.Ledger == nil || out.IsError {
		return out, Report{}
	}
	a, ok := parseReadArgs(call.Input)
	if !ok {
		return out, Report{}
	}
	fs, ok := StatFile(a.FilePath, int(a.Limit), true)
	if !ok {
		return out, Report{}
	}
	s.Ledger.Put(ReadKey(a.FilePath, int(a.Offset), int(a.Limit)),
		ReadEntry{Sum: fs.Sum, MTime: fs.MTime, Size: fs.Size, Prompt: s.Ledger.Prompts(), At: s.Now.Unix()})
	if err := s.Ledger.Save(); err != nil {
		return out, Report{Err: "ledger"}
	}
	return out, Report{Name: config.OptimizerHookRepeatRead}
}

func ago(now time.Time, at int64) string {
	d := now.Sub(time.Unix(at, 0))
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("about %d min", int(d.Minutes()))
	}
	return fmt.Sprintf("about %.1f h", d.Hours())
}

// LargeOutputAdvice is the hook_large_output optimizer: for a known-noisy shell
// command it adds a hint to use trove_run. It never denies anything.
type LargeOutputAdvice struct{}

// Name implements Base.
func (LargeOutputAdvice) Name() string { return config.OptimizerHookLargeOutput }

// Tier implements Base: advice only, nothing is removed.
func (LargeOutputAdvice) Tier() Tier { return TierLossless }

// Points implements Capable.
func (LargeOutputAdvice) Points() []Point { return []Point{PointToolInput} }

// OverheadTokens implements Capable: the hint is injected only for noisy commands.
func (LargeOutputAdvice) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (LargeOutputAdvice) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "advice has to reach the agent before the command runs; the gateway only sees finished history"}, true
	}
	return Support{}, false
}

// TransformInput implements ToolInputOptimizer.
func (LargeOutputAdvice) TransformInput(_ context.Context, call ToolCall, s Session) (ToolCall, Report) {
	if call.Name != "Bash" {
		return call, Report{}
	}
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(call.Input, &a) != nil || !IsNoisy(a.Command) {
		return call, Report{}
	}
	name := config.OptimizerHookLargeOutput
	switch {
	case s.Mode == config.StateShadow:
		return call, Report{Name: name, Decision: &Decision{Kind: KindWouldAdvise}}
	case !s.TroveRun:
		return call, Report{} // trove_run is not registered with the agent, so there is nothing to suggest
	}
	return call, Report{Name: name, Decision: &Decision{Kind: KindAdvised,
		Message: largeOutputHint}}
}

// largeOutputHint is the advice hook_large_output adds; its size is the per-use overhead declared for B8.
const largeOutputHint = "Trove: this command usually prints a lot. If the trove_run tool is available, run it through trove_run: the full output stays retrievable and only the part that matters enters your context."

// repeatReadMessage is the denial the agent sees; its size is the per-use overhead declared for B6.
func repeatReadMessage(prompt int, ago string) string {
	return fmt.Sprintf("Trove: this file is unchanged since you read it earlier in this session (prompt #%d, %s ago), so its contents are already in your context. Use that. If you really need it read again, repeat the same Read and it will go through.", prompt, ago)
}
