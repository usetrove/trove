package optimize

import (
	"crypto/sha256"
	"sync"
)

// spanMemo remembers what an optimizer produced for a string it has already
// seen. An agent resends its whole history every turn, so almost every tool
// result in a request was in the previous one; the memo lets the optimizer skip
// decoding and cleaning it again.
//
// The memo is a speed-up and nothing more. A hit returns exactly what computing
// again would, because the key is a SHA-256 of the exact bytes and the optimizer
// is a pure function of those bytes. Output never depends on what is in the
// memo, so a gateway restart (empty memo) produces the same bytes.
//
// A nil *spanMemo is valid and remembers nothing.
type spanMemo struct {
	mu    sync.Mutex
	m     map[[sha256.Size]byte][]byte
	bytes int
}

const (
	// memoMinSpan: below this, hashing costs about as much as redoing the work.
	memoMinSpan    = 256
	memoMaxEntries = 50_000
	memoMaxBytes   = 64 << 20
)

func newSpanMemo() *spanMemo { return &spanMemo{m: map[[sha256.Size]byte][]byte{}} }

func memoKey(b []byte) [sha256.Size]byte { return sha256.Sum256(b) }

// get returns the remembered replacement for key. known is false if the string
// has not been seen; a nil replacement with known true means "left unchanged".
func (c *spanMemo) get(key [sha256.Size]byte) (repl []byte, known bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	repl, known = c.m[key]
	return repl, known
}

// put remembers repl for key (nil for "unchanged"). When the memo is full it is
// emptied; it refills from the next request.
func (c *spanMemo) put(key [sha256.Size]byte, repl []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= memoMaxEntries || c.bytes+len(repl) > memoMaxBytes {
		c.m, c.bytes = map[[sha256.Size]byte][]byte{}, 0
	}
	c.m[key] = repl
	c.bytes += len(repl)
}
