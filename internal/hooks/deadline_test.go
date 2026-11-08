package hooks

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// blockingReader stands in for a hook whose input never arrives in time. run()
// reads stdin inside the guarded goroutine, so the deadline is what ends the call.
type blockingReader struct{ release chan struct{} }

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

// TestCallDeadlineLetsTheAgentThroughWhenAHookIsSlow is the test that the per-call
// deadline really fires: a hook past the deadline must produce no output and exit 0,
// so the agent is let through (fail open, CLAUDE.md rule 4).
func TestCallDeadlineLetsTheAgentThroughWhenAHookIsSlow(t *testing.T) {
	old := callDeadline
	callDeadline = 20 * time.Millisecond
	t.Cleanup(func() { callDeadline = old })

	dir := t.TempDir() // registered before the cleanup below, so it is removed after it
	release := make(chan struct{})
	// Unblock the abandoned hook goroutine and give it a moment to finish its last
	// write into dir before TempDir is removed.
	t.Cleanup(func() {
		close(release)
		time.Sleep(50 * time.Millisecond)
	})

	var out bytes.Buffer
	start := time.Now()
	code := Main(Env{
		Args:   []string{EventPreRead, "--data-dir", dir},
		Stdin:  &blockingReader{release: release},
		Stdout: &out,
		Getenv: func(string) string { return "" },
		Now:    time.Now,
	})
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("exit %d: a hook must never block the agent", code)
	}
	if out.Len() != 0 {
		t.Fatalf("a hook past its deadline must print nothing, got %q", out.String())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the call was not cut off by the deadline: took %v", elapsed)
	}
}
