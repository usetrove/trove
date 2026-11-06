package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestFeaturesRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	f := db.Features()
	at := time.UnixMilli(1_700_000_000_000).UTC()

	if _, ok, err := f.Repo(ctx, "r1"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	repo := RepoFeaturesRow{RepoKey: "r1", ComputedAt: at, SizeBucket: "M", LangMix: "go:70,python:30", TestFramework: "go_test"}
	if err := f.PutRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	repo.SizeBucket = "L"
	if err := f.PutRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := f.Repo(ctx, "r1"); err != nil || !ok || !reflect.DeepEqual(got, repo) {
		t.Errorf("repo = %+v ok=%v err=%v, want %+v", got, ok, err, repo)
	}
	if err := f.PutRepo(ctx, RepoFeaturesRow{RepoKey: "r2", ComputedAt: at, SizeBucket: "huge"}); err == nil {
		t.Error("a size bucket outside the fixed set should be rejected by the schema")
	}

	s1 := SessionFeaturesRow{SessionKey: "s1", RepoKey: "r1", RecordedAt: at, FilesRead: 4, Retries: 1, AvgDupRatio: 0.25}
	s2 := SessionFeaturesRow{SessionKey: "s2", RepoKey: "r1", RecordedAt: at.Add(time.Hour), ToolResults: 9}
	for _, s := range []SessionFeaturesRow{s1, s2} {
		if err := f.PutSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	dup := s1
	dup.FilesRead = 99
	if err := f.PutSession(ctx, dup); err != nil {
		t.Fatal(err)
	}
	got, err := f.Sessions(ctx, "r1", 10)
	if err != nil || !reflect.DeepEqual(got, []SessionFeaturesRow{s2, s1}) {
		t.Errorf("sessions = %+v err=%v; want newest first and the first write kept", got, err)
	}
	if got, _ := f.Sessions(ctx, "r1", 1); len(got) != 1 {
		t.Errorf("limit ignored: %d rows", len(got))
	}
}

func TestRepoSaltIsStableAndRandom(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	a, err := db.Features().Salt(ctx)
	if err != nil || len(a) != 32 {
		t.Fatalf("salt = %q err=%v", a, err)
	}
	if b, _ := db.Features().Salt(ctx); b != a {
		t.Errorf("salt changed between calls: %q then %q", a, b)
	}
	other := openTemp(t)
	if c, _ := other.Features().Salt(ctx); c == a {
		t.Error("two installs got the same salt")
	}
}

func TestPurgeRemovesOldSessionFeatures(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	now := time.Now()
	old := SessionFeaturesRow{SessionKey: "old", RepoKey: "r", RecordedAt: now.AddDate(0, 0, -100)}
	cur := SessionFeaturesRow{SessionKey: "new", RepoKey: "r", RecordedAt: now}
	for _, s := range []SessionFeaturesRow{old, cur} {
		if err := db.Features().PutSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Purge(ctx, 90, now); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Features().Sessions(ctx, "r", 10)
	if len(got) != 1 || got[0].SessionKey != "new" {
		t.Errorf("after purge: %+v", got)
	}
}
