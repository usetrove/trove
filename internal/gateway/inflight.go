package gateway

import (
	"bytes"
	"hash/maphash"
	"sync"
)

// In-flight dedup, shadow mode.
//
// Parallel agents sometimes send the same request at the same moment: a retry that
// raced the original, or sibling subagents with the same prompt. A gateway could
// make one call to the provider and give every one of them the answer. This file
// only finds those cases. It tells the recorder that a request arrived while an
// identical one was still waiting for the provider, so the report can say what
// sharing the call would have saved. Nothing is shared and no request or response
// changes: every request still goes upstream on its own.
//
// Two requests are the same when the method, path, query, the headers that change
// the answer, the credential and every byte of the body are equal. The credential
// is compared only through an HMAC held in memory for the length of the request
// (see auth.Fingerprinter); a different key never counts as a duplicate. The body
// is compared byte for byte, so a hash collision cannot make two requests look
// alike.
//
// A request counts as a duplicate only while the earlier one has not yet received
// the provider's response headers. That is the window in which an active version
// could attach the second request to the first call; later than that, the answer
// has started and joining would mean replaying a stream already in progress.
//
// Everything here is in memory and lasts as long as a request does. Nothing is
// written down. It never blocks a request: one short lock, no I/O.

// inflightEntry is one request that is in flight.
type inflightEntry struct {
	hash      uint64
	meta      string
	cred      [32]byte
	body      []byte // a reference to the body the request already holds, not a copy
	responded bool
	left      bool
}

// inflightBook tracks the requests in flight.
type inflightBook struct {
	mu   sync.Mutex
	seed maphash.Seed
	live map[uint64][]*inflightEntry
	// hashOf is replaceable so a test can force two different requests to collide.
	hashOf func(meta string, body []byte) uint64
}

func newInflightBook() *inflightBook {
	b := &inflightBook{seed: maphash.MakeSeed(), live: map[uint64][]*inflightEntry{}}
	b.hashOf = func(meta string, body []byte) uint64 {
		var h maphash.Hash
		h.SetSeed(b.seed)
		_, _ = h.WriteString(meta)
		_, _ = h.Write(body)
		return h.Sum64()
	}
	return b
}

// enter registers a request and reports whether an identical one is in flight and
// has not yet been answered. meta is everything about the request other than its
// credential and body that would change the provider's answer.
func (b *inflightBook) enter(meta string, cred [32]byte, body []byte) (e *inflightEntry, duplicate bool) {
	e = &inflightEntry{hash: b.hashOf(meta, body), meta: meta, cred: cred, body: body}

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, o := range b.live[e.hash] {
		if !o.responded && !o.left && o.meta == meta && o.cred == cred && bytes.Equal(o.body, body) {
			duplicate = true
			break
		}
	}
	b.live[e.hash] = append(b.live[e.hash], e)
	return e, duplicate
}

// answered marks that the provider's response headers arrived for e.
func (b *inflightBook) answered(e *inflightEntry) {
	b.mu.Lock()
	e.responded = true
	b.mu.Unlock()
}

// leave removes e. It is safe to call more than once.
func (b *inflightBook) leave(e *inflightEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e.left {
		return
	}
	e.left = true
	list := b.live[e.hash]
	for i, o := range list {
		if o == e {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(b.live, e.hash)
	} else {
		b.live[e.hash] = list
	}
}

// size is the number of requests in flight, for tests.
func (b *inflightBook) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, l := range b.live {
		n += len(l)
	}
	return n
}
