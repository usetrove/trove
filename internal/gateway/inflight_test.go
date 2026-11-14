package gateway

import (
	"bytes"
	"sync"
	"testing"
)

func cred(b byte) [32]byte { var c [32]byte; c[0] = b; return c }

func TestInflightFindsADuplicateOnlyWhileTheFirstIsUnanswered(t *testing.T) {
	b := newInflightBook()
	body := []byte(`{"model":"m","messages":[]}`)
	a, dup := b.enter("anthropic|POST|/v1/messages", cred(1), body)
	if dup {
		t.Fatal("the first request cannot be a duplicate")
	}
	_, dup = b.enter("anthropic|POST|/v1/messages", cred(1), append([]byte(nil), body...))
	if !dup {
		t.Fatal("an identical request while the first waits must be a duplicate")
	}
	b.answered(a)
	// The second one is still waiting, so a third identical request still has something to join.
	if _, dup = b.enter("anthropic|POST|/v1/messages", cred(1), body); !dup {
		t.Error("the second request is unanswered, so a third is still a duplicate")
	}
}

func TestInflightRequiresEveryPartToMatch(t *testing.T) {
	b := newInflightBook()
	body := []byte(`{"model":"m"}`)
	b.enter("anthropic|POST|/v1/messages|v1", cred(1), body)
	cases := map[string]struct {
		meta string
		cred [32]byte
		body []byte
	}{
		"another credential":          {"anthropic|POST|/v1/messages|v1", cred(2), body},
		"another path or header":      {"anthropic|POST|/v1/messages|v2", cred(1), body},
		"another body":                {"anthropic|POST|/v1/messages|v1", cred(1), []byte(`{"model":"n"}`)},
		"a body one byte longer":      {"anthropic|POST|/v1/messages|v1", cred(1), []byte(`{"model":"m"} `)},
		"another provider":            {"openai|POST|/v1/messages|v1", cred(1), body},
		"an empty body":               {"anthropic|POST|/v1/messages|v1", cred(1), nil},
		"same body, empty credential": {"anthropic|POST|/v1/messages|v1", cred(0), body},
	}
	for name, c := range cases {
		if _, dup := b.enter(c.meta, c.cred, c.body); dup {
			t.Errorf("%s counted as a duplicate", name)
		}
	}
}

func TestInflightLeaveForgetsTheRequestAndIsSafeTwice(t *testing.T) {
	b := newInflightBook()
	body := []byte("x")
	a, _ := b.enter("m", cred(1), body)
	if b.size() != 1 {
		t.Fatalf("size %d", b.size())
	}
	b.leave(a)
	b.leave(a)
	if b.size() != 0 {
		t.Fatalf("size %d after leave", b.size())
	}
	if _, dup := b.enter("m", cred(1), body); dup {
		t.Error("a request that left still counted as in flight")
	}
}

// Many requests at once must leave the book empty and must not race.
func TestInflightUnderConcurrency(t *testing.T) {
	b := newInflightBook()
	var wg sync.WaitGroup
	var mu sync.Mutex
	dups := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e, dup := b.enter("m", cred(byte(i%3)), []byte{byte(i % 5)})
			mu.Lock()
			if dup {
				dups++
			}
			mu.Unlock()
			if i%2 == 0 {
				b.answered(e)
			}
			b.leave(e)
		}(i)
	}
	wg.Wait()
	if b.size() != 0 {
		t.Fatalf("%d requests left in the book", b.size())
	}
}

// The hash only narrows the search. Two different requests that happen to share a hash must
// still not be taken for one another: that is what the byte comparison is for.
func TestInflightACollisionNeverMakesTwoDifferentRequestsLookAlike(t *testing.T) {
	b := newInflightBook()
	b.hashOf = func(string, []byte) uint64 { return 42 } // everything collides
	if _, dup := b.enter("m", cred(1), []byte(`{"a":1}`)); dup {
		t.Fatal("first request")
	}
	if _, dup := b.enter("m", cred(1), []byte(`{"a":2}`)); dup {
		t.Error("a colliding hash made a different body look like a duplicate")
	}
	if _, dup := b.enter("m", cred(2), []byte(`{"a":1}`)); dup {
		t.Error("a colliding hash made a different credential look like the same one")
	}
	if _, dup := b.enter("n", cred(1), []byte(`{"a":1}`)); dup {
		t.Error("a colliding hash made a different path or header look the same")
	}
	if _, dup := b.enter("m", cred(1), []byte(`{"a":1}`)); !dup {
		t.Error("an identical request must still be found when hashes collide")
	}
}

// The tracker runs on every request while dedup is on or in shadow, so its cost must stay far
// inside the gateway's 5 ms budget. This is a 217 KB body, the size the engine's budget test uses.
func BenchmarkInflightEnterLeave217KB(b *testing.B) {
	book := newInflightBook()
	body := bytes.Repeat([]byte("abcdefghij"), 21700)
	meta := "anthropic|POST|/v1/messages|2023-06-01"
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e, _ := book.enter(meta, cred(1), body)
		book.leave(e)
	}
}
