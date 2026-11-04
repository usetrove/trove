package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestExternalToolsRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	s := db.ExternalTools()
	now := time.UnixMilli(1_700_000_000_000).UTC()
	in := ExternalTool{Name: "rtk", Version: "1.0.0", License: "MIT", State: "off", InstalledAt: now, LicenseAckAt: now, ChangeIDs: []string{"c1", "c2"}}
	if err := s.Put(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, "rtk")
	if err != nil || !ok || !reflect.DeepEqual(got, in) {
		t.Fatalf("Get = %+v ok=%v err=%v", got, ok, err)
	}
	if ok, _ := s.SetState(ctx, "rtk", "on"); !ok {
		t.Fatal("SetState on known tool reported false")
	}
	if ok, _ := s.SetState(ctx, "nope", "on"); ok {
		t.Fatal("SetState on unknown tool reported true")
	}
	all, _ := s.List(ctx)
	if len(all) != 1 || all[0].State != "on" {
		t.Fatalf("List = %+v", all)
	}
	if err := s.Delete(ctx, "rtk"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "rtk"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "rtk"); ok {
		t.Fatal("still present after delete")
	}
}
