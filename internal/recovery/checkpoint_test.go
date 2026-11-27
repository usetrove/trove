package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func openCk(t *testing.T, now *time.Time) (*CheckpointStore, string) {
	t.Helper()
	dir := CheckpointDir(filepath.Join(t.TempDir(), "recovery"))
	s, err := OpenCheckpoints(dir, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func sampleNote() Checkpoint {
	return Checkpoint{Session: "c-abc", Reason: "precompact", Task: []string{"fix the test"}, Branch: "main",
		Files: []string{"a.go"}, FailingTests: []string{"go test ./..."}}
}

func TestCheckpointFieldCaps(t *testing.T) {
	long := strings.Repeat("é", 1000) // two bytes each: a cut must not split one
	c := Checkpoint{Branch: long, LastReply: long}
	for i := 0; i < 50; i++ {
		c.Task = append(c.Task, fmt.Sprintf("task %d %s", i, long))
		c.Decisions = append(c.Decisions, long)
		c.Files = append(c.Files, long)
		c.FailingTests = append(c.FailingTests, long)
	}
	c = c.Cap()
	if len(c.Task) != MaxTaskItems || len(c.Decisions) != MaxDecisionItems || len(c.Files) != MaxFileItems || len(c.FailingTests) != MaxFailingItems {
		t.Fatalf("items: %d %d %d %d", len(c.Task), len(c.Decisions), len(c.Files), len(c.FailingTests))
	}
	if !strings.HasPrefix(c.Task[0], "task 47 ") {
		t.Errorf("the newest items must be kept: %q", c.Task[0][:10])
	}
	for _, s := range append(append([]string{c.Branch, c.LastReply}, c.Task...), c.Files...) {
		if len(s) > MaxTaskBytes && len(s) > MaxReplyBytes {
			t.Errorf("a text of %d bytes passed its cap", len(s))
		}
		if !strings.HasSuffix(s, "…") || strings.ContainsRune(s, '�') {
			t.Errorf("bad cut: %q", s[len(s)-8:])
		}
	}
	if len(c.Branch) > MaxBranchBytes || len(c.LastReply) > MaxReplyBytes || len(c.Files[0]) > MaxFileBytes {
		t.Errorf("caps: branch %d, reply %d, file %d", len(c.Branch), len(c.LastReply), len(c.Files[0]))
	}
	if got := cutText("a \n\t b", 100); got != "a b" {
		t.Errorf("whitespace: %q", got)
	}
}

func TestCheckpointSaveRefusesOversizeAndEmpty(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, dir := openCk(t, &now)
	slot := Slot("/proj")
	if _, err := s.Save(slot, Checkpoint{Reason: "precompact"}); err == nil {
		t.Error("an empty note was stored")
	}
	// '<' is written as < in JSON, six bytes each, so a note can pass the file cap even with every field capped.
	big := strings.Repeat("<", 1000)
	c := Checkpoint{LastReply: big, Branch: big}
	for i := 0; i < 10; i++ {
		c.Task, c.Decisions, c.Files, c.FailingTests = append(c.Task, big), append(c.Decisions, big), append(c.Files, big), append(c.FailingTests, big)
	}
	if _, err := s.Save(slot, c); err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("an oversize note: %v", err)
	}
	if _, err := s.Save("../../etc", sampleNote()); err == nil {
		t.Error("a slot that is not a hash was accepted")
	}
	des, _ := os.ReadDir(dir)
	if len(des) != 0 {
		t.Errorf("refused notes left files: %v", des)
	}
	n, err := s.Save(slot, sampleNote())
	if err != nil || n <= 0 || n > CheckpointMaxFileBytes {
		t.Fatalf("save: %d %v", n, err)
	}
}

func TestCheckpointExpiry(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, dir := openCk(t, &now)
	slot := Slot("/proj")
	if _, err := s.Save(slot, sampleNote()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if c, err := s.Latest(slot, 2*time.Hour); err != nil || c.Branch != "main" || c.V != checkpointVersion {
		t.Fatalf("a fresh note: %+v %v", c, err)
	}
	now = now.Add(2 * time.Hour) // 3 hours old: too old to restore, still kept
	if _, err := s.Latest(slot, 2*time.Hour); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("a 3-hour-old note was restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, slot+checkpointExt)); err != nil {
		t.Fatal("a note younger than the TTL was removed")
	}
	now = now.Add(CheckpointTTL) // past the TTL: removed on read
	if _, err := s.Latest(slot, 0); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("an expired note was returned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, slot+checkpointExt)); err == nil {
		t.Fatal("an expired note was kept")
	}
	// Sweep, which runs on open, removes expired notes nobody reads.
	if _, err := s.Save(Slot("/other"), sampleNote()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(CheckpointTTL + time.Minute)
	if _, err := OpenCheckpoints(dir, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if des, _ := os.ReadDir(dir); len(des) != 0 {
		t.Errorf("sweep left %d files", len(des))
	}
}

func TestCheckpointNewerReplacesOlder(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, _ := openCk(t, &now)
	slot := Slot("/proj")
	a, b := sampleNote(), sampleNote()
	b.Branch = "feature"
	_, _ = s.Save(slot, a)
	now = now.Add(time.Minute)
	_, _ = s.Save(slot, b)
	if c, err := s.Latest(slot, 0); err != nil || c.Branch != "feature" {
		t.Fatalf("%+v %v", c, err)
	}
	if Slot("/proj") == Slot("/proj2") || Slot("/proj/") != Slot("/proj") {
		t.Error("slots must be per folder")
	}
}

func TestCheckpointDamagedNoteIsRemoved(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for name, content := range map[string]string{
		"not json":      "{nope",
		"wrong version": `{"v":99,"saved_at":1,"branch":"x"}`,
		"no time":       `{"v":1,"branch":"x"}`,
		"too large":     `{"v":1,"saved_at":1,"branch":"` + strings.Repeat("x", CheckpointMaxFileBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, dir := openCk(t, &now)
			p := filepath.Join(dir, Slot("/p")+checkpointExt)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Latest(Slot("/p"), 0); !errors.Is(err, ErrNoCheckpoint) {
				t.Fatalf("got %v", err)
			}
			if _, err := os.Stat(p); err == nil {
				t.Error("the damaged note was kept")
			}
		})
	}
}

// A hand-edited note is held to the same caps as a stored one.
func TestCheckpointLatestAppliesCaps(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, dir := openCk(t, &now)
	body := fmt.Sprintf(`{"v":1,"saved_at":%d,"branch":"%s"}`, now.UnixMilli(), strings.Repeat("b", 500))
	if err := os.WriteFile(filepath.Join(dir, Slot("/p")+checkpointExt), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := s.Latest(Slot("/p"), 0)
	if err != nil || len(c.Branch) > MaxBranchBytes {
		t.Fatalf("%d %v", len(c.Branch), err)
	}
}

func TestCheckpointFolderCap(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, dir := openCk(t, &now)
	long := strings.Repeat("x", 290)
	note := Checkpoint{LastReply: long, Branch: long[:90]}
	for i := 0; i < 10; i++ {
		note.Task, note.Files = append(note.Task, long), append(note.Files, long[:190])
	}
	var last string
	for i := 0; i < 400; i++ {
		now = now.Add(time.Second)
		last = Slot(fmt.Sprintf("/p%d", i))
		if _, err := s.Save(last, note); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	des, _ := os.ReadDir(dir)
	for _, d := range des {
		fi, _ := d.Info()
		total += fi.Size()
	}
	if total > CheckpointMaxTotalBytes {
		t.Errorf("folder holds %d bytes, over %d", total, CheckpointMaxTotalBytes)
	}
	if len(des) >= 400 {
		t.Error("nothing was evicted")
	}
	if _, err := s.Latest(last, 0); err != nil {
		t.Error("the newest note was evicted")
	}
	if _, err := s.Latest(Slot("/p0"), 0); err == nil {
		t.Error("the oldest note survived")
	}
}

func TestCheckpointPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	now := time.Now()
	s, dir := openCk(t, &now)
	if _, err := s.Save(Slot("/p"), sampleNote()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, Slot("/p")+checkpointExt))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("note mode %v %v", fi.Mode(), err)
	}
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v %v", di.Mode(), err)
	}
}
