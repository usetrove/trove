package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func open(t *testing.T, o Options) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "recovery")
	s, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := open(t, Options{})
	data := []byte("line1\nline2\nline3\n")
	id, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if id != ID(data) || !ValidID(id) {
		t.Fatalf("bad id %q", id)
	}
	got, total, err := s.Get(id, 0, 0)
	if err != nil || got != string(data) || total != 3 {
		t.Fatalf("got %q %d %v", got, total, err)
	}
	id2, _ := s.Put(data)
	if id2 != id {
		t.Fatal("same bytes must give the same ID")
	}
}

func TestGetRange(t *testing.T) {
	s, _ := open(t, Options{})
	id, _ := s.Put([]byte("a\nb\nc\nd\n"))
	tests := []struct {
		name       string
		start, end int
		want       string
		wantErr    bool
	}{
		{"middle", 2, 3, "b\nc\n", false},
		{"to end", 3, 0, "c\nd\n", false},
		{"from start", 0, 2, "a\nb\n", false},
		{"end clamps", 4, 99, "d\n", false},
		{"past end", 9, 10, "", true},
		{"inverted", 3, 2, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := s.Get(id, tc.start, tc.end)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q err %v", got, err)
			}
		})
	}
}

func TestInvalidIDs(t *testing.T) {
	s, _ := open(t, Options{})
	for _, id := range []string{"", "../x", "..\\x", "abc", "0123456789abcdeg", "0123456789ABCDEF", "0123456789abcdef.out", "/etc/passwd"} {
		if _, _, err := s.Get(id, 0, 0); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%q: want invalid-ID error, got %v", id, err)
		}
	}
	if _, _, err := s.Get("0123456789abcdef", 0, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown valid ID: want ErrNotFound, got %v", err)
	}
}

func TestTTLExpiry(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	s, dir := open(t, Options{TTL: time.Hour, Now: c.now})
	id, _ := s.Put([]byte("x\n"))
	c.t = c.t.Add(30 * time.Minute)
	if _, _, err := s.Get(id, 0, 0); err != nil {
		t.Fatalf("should still be there: %v", err)
	}
	c.t = c.t.Add(2 * time.Hour)
	if _, _, err := s.Get(id, 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want expired, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+fileExt)); !os.IsNotExist(err) {
		t.Fatal("expired file should be deleted")
	}
}

func TestSweepOnOpenAndPut(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	dir := filepath.Join(t.TempDir(), "r")
	s, _ := Open(dir, Options{TTL: time.Hour, Now: c.now})
	_, _ = s.Put([]byte("old\n"))
	c.t = c.t.Add(2 * time.Hour)
	s2, err := Open(dir, Options{TTL: time.Hour, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := s2.Stats(); st.Entries != 0 {
		t.Fatalf("open should sweep expired entries, have %d", st.Entries)
	}
}

func TestSizeCapEvictsOldest(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	s, _ := open(t, Options{MaxBytes: 100, Now: c.now})
	var ids []string
	for i := 0; i < 4; i++ {
		c.t = c.t.Add(time.Minute)
		id, err := s.Put([]byte(strings.Repeat(string(rune('a'+i)), 40)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	st, _ := s.Stats()
	if st.Bytes > 100 {
		t.Fatalf("over cap: %d", st.Bytes)
	}
	if _, _, err := s.Get(ids[0], 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("oldest should be evicted")
	}
	if _, _, err := s.Get(ids[3], 0, 0); err != nil {
		t.Fatalf("newest must survive: %v", err)
	}
}

func TestOversizedOutputRefused(t *testing.T) {
	s, _ := open(t, Options{MaxBytes: 10})
	if _, err := s.Put([]byte(strings.Repeat("x", 11))); err == nil {
		t.Fatal("want refusal for output larger than the cap")
	}
	if st, _ := s.Stats(); st.Entries != 0 {
		t.Fatal("nothing should be stored")
	}
}

func TestPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not enforced on Windows")
	}
	s, dir := open(t, Options{})
	id, _ := s.Put([]byte("secret\n"))
	fi, _ := os.Stat(filepath.Join(dir, id+fileExt))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
}

func TestConcurrentPut(t *testing.T) {
	s, _ := open(t, Options{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte(strings.Repeat("z", i%5+1) + "\n")
			id, err := s.Put(data)
			if err != nil {
				t.Error(err)
				return
			}
			if got, _, err := s.Get(id, 0, 0); err != nil || got != string(data) {
				t.Errorf("round trip failed: %q %v", got, err)
			}
		}(i)
	}
	wg.Wait()
}
