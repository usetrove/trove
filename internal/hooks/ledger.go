package hooks

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/usetrove/trove/internal/optimize"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// maxReads bounds the per-session ledger. The oldest entries go first.
	maxReads = 500
	// compactAt is how many journal lines a session file may reach before it is
	// rewritten with only the live entries.
	compactAt = 4 * maxReads
)

// readEntry is the engine's ReadEntry; hooks only stores it.
type readEntry = optimize.ReadEntry

// op is one line of a session's journal.
type op struct {
	T string // pin, prompt, read, del or clear
	K string // read key (read, del)
	N string // optimizer name (pin)
	V string // state (pin)
	H string // short prompt hash (prompt)
	E *readEntry
}

// The journal is plain text, one op per line, fields separated by single spaces
// (no field can contain one: they are hex, digits and fixed names). It is read on
// every tool call, and this is several times faster to parse than JSON:
//
//	P <optimizer> <state>
//	N <prompt hash>
//	R <key> <sum> <mtime> <size> <prompt> <at> <blocked sum or ->
//	D <key>
//	C
func (o op) line() []byte {
	switch o.T {
	case "pin":
		return []byte("P " + o.N + " " + o.V)
	case "prompt":
		h := o.H
		if h == "" {
			h = "-"
		}
		return []byte("N " + h)
	case "read":
		e := o.E
		b := e.BlockedSum
		if b == "" {
			b = "-"
		}
		sum := e.Sum
		if sum == "" {
			sum = "-" // entries that are not file reads (rewritten commands, trimmed outputs) have no content hash
		}
		return []byte(fmt.Sprintf("R %s %s %d %d %d %d %s", o.K, sum, e.MTime, e.Size, e.Prompt, e.At, b))
	case "del":
		return []byte("D " + o.K)
	}
	return []byte("C")
}

func parseOp(line []byte) (op, bool) {
	f := strings.Fields(string(line))
	if len(f) == 0 {
		return op{}, false
	}
	switch f[0] {
	case "P":
		if len(f) == 3 {
			return op{T: "pin", N: f[1], V: f[2]}, true
		}
	case "N":
		if len(f) == 2 {
			h := f[1]
			if h == "-" {
				h = ""
			}
			return op{T: "prompt", H: h}, true
		}
	case "R":
		if len(f) == 8 {
			mt, e1 := strconv.ParseInt(f[3], 10, 64)
			sz, e2 := strconv.ParseInt(f[4], 10, 64)
			pr, e3 := strconv.Atoi(f[5])
			at, e4 := strconv.ParseInt(f[6], 10, 64)
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
				return op{}, false
			}
			b, sum := f[7], f[2]
			if b == "-" {
				b = ""
			}
			if sum == "-" {
				sum = ""
			}
			return op{T: "read", K: f[1], E: &readEntry{Sum: sum, MTime: mt, Size: sz, Prompt: pr, At: at, BlockedSum: b}}, true
		}
	case "D":
		if len(f) == 2 {
			return op{T: "del", K: f[1]}, true
		}
	case "C":
		return op{T: "clear"}, true
	}
	return op{}, false
}

// sessionState is one session's ledger, rebuilt from its journal file under
// <data dir>/hooks/sessions. A hook adds to the journal with one short append, so
// concurrent hooks never rewrite or rename the file (which is slow on some
// systems and loses updates), and a damaged line costs only itself.
type sessionState struct {
	PromptN    int
	LastPrompt string
	Pins       map[string]string
	Reads      map[string]*readEntry

	lines   int
	pending []op
}

func newSessionState() *sessionState {
	return &sessionState{Pins: map[string]string{}, Reads: map[string]*readEntry{}}
}

func sessionDir(dir string) string { return filepath.Join(dir, "hooks", "sessions") }

func sessionPath(dir, key string) string { return filepath.Join(sessionDir(dir), key+".log") }

// apply changes the in-memory state for one op.
func (st *sessionState) apply(o op) {
	switch o.T {
	case "pin":
		st.Pins[o.N] = o.V
	case "prompt":
		st.LastPrompt = o.H
		st.PromptN++
	case "read":
		if o.E != nil && o.K != "" {
			st.Reads[o.K] = o.E
		}
	case "del":
		delete(st.Reads, o.K)
	case "clear":
		st.Reads = map[string]*readEntry{}
	}
}

// do applies an op now and queues it for save.
func (st *sessionState) do(o op) {
	st.apply(o)
	st.pending = append(st.pending, o)
}

// loadSession replays the session's journal. A missing file is a new session;
// lines that do not parse are skipped, so a corrupt file at worst forgets reads
// (which means a repeat is allowed, never a wrong block).
func loadSession(dir, key string) *sessionState {
	st := newSessionState()
	f, err := os.Open(sessionPath(dir, key)) //nolint:gosec // path under Trove's data dir
	if err != nil {
		return st
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		st.lines++
		if o, ok := parseOp(sc.Bytes()); ok {
			st.apply(o)
		}
	}
	if len(st.Reads) > maxReads {
		st.evict()
	}
	return st
}

// evict drops the oldest reads beyond maxReads.
func (st *sessionState) evict() {
	type kv struct {
		k string
		a int64
	}
	all := make([]kv, 0, len(st.Reads))
	for k, e := range st.Reads {
		all = append(all, kv{k, e.At})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].a < all[j].a || (all[i].a == all[j].a && all[i].k < all[j].k) })
	for _, o := range all[:len(all)-maxReads] {
		delete(st.Reads, o.k)
	}
}

// save appends the queued ops with a single write, then compacts the file if the
// journal has grown long.
func (st *sessionState) save(dir, key string) error {
	if len(st.pending) == 0 {
		return nil
	}
	var buf []byte
	for _, o := range st.pending {
		b := o.line()
		buf = append(append(buf, b...), '\n')
	}
	st.lines += len(st.pending)
	st.pending = nil
	p := sessionPath(dir, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // Trove's own data dir
	if err != nil {
		return err
	}
	_, werr := f.Write(buf)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if st.lines > compactAt {
		return st.compact(dir, key)
	}
	return nil
}

// compact rewrites the journal as one line per live fact.
func (st *sessionState) compact(dir, key string) error {
	if len(st.Reads) > maxReads {
		st.evict()
	}
	var buf []byte
	add := func(o op) {
		buf = append(append(buf, o.line()...), '\n')
		st.lines++
	}
	st.lines = 0
	names := make([]string, 0, len(st.Pins))
	for n := range st.Pins {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		add(op{T: "pin", N: n, V: st.Pins[n]})
	}
	// The prompt counter is restored by replaying that many prompt ops; keep the
	// file small by writing a single op per prompt only up to a sane cap.
	n := st.PromptN
	st.PromptN = 0
	for i := 0; i < n && i < 2000; i++ {
		h := ""
		if i == n-1 {
			h = st.LastPrompt
		}
		add(op{T: "prompt", H: h})
	}
	st.PromptN = n
	keys := make([]string, 0, len(st.Reads))
	for k := range st.Reads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(op{T: "read", K: k, E: st.Reads[k]})
	}
	return writeFileAtomic(sessionPath(dir, key), buf)
}

// seePrompt counts a prompt the first time its id is seen, so entries can say
// "prompt #3" without the hook input carrying a turn number.
func (st *sessionState) seePrompt(id string) {
	if id == "" {
		return
	}
	if h := shortHash(id); h != st.LastPrompt {
		st.do(op{T: "prompt", H: h})
	}
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

// writeFileAtomic writes via a temp file in the same folder and renames, with
// 0600 permissions and the folder created at 0700.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
