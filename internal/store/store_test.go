package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newAd(id, title string) *Ad {
	return &Ad{
		ID: id, Title: title, Kind: KindImage, MediaFile: id + ".png",
		DurationMs: 5000, Fit: "contain", Background: "#000000",
		Status: StatusPending, Enabled: true,
		SubmitterEmail: "member@example.edu", SubmitterName: "A Member",
	}
}

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	want := newAd("a1", "Demo Night")
	if err := s.CreateAd(ctx, want); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetAd(ctx, "a1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != want.Title || got.DurationMs != want.DurationMs || got.Status != StatusPending {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if _, err := s.GetAd(ctx, "missing"); err != ErrNotFound {
		t.Errorf("missing ad: got %v, want ErrNotFound", err)
	}
}

// Playlist is the endpoint the TV hits; every filter it applies is a way for an
// ad to silently not appear, so each one is pinned here.
func TestPlaylistFiltering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	past := now.Add(-48 * time.Hour)
	future := now.Add(48 * time.Hour)

	approved := newAd("live", "Showing")
	approved.Status = StatusApproved

	pending := newAd("pending", "Not reviewed")

	rejected := newAd("rejected", "Turned down")
	rejected.Status = StatusRejected

	paused := newAd("paused", "Paused")
	paused.Status, paused.Enabled = StatusApproved, false

	expired := newAd("expired", "Last week's event")
	expired.Status, expired.EndsAt = StatusApproved, &past

	notYet := newAd("notyet", "Next week's event")
	notYet.Status, notYet.StartsAt = StatusApproved, &future

	inWindow := newAd("window", "Running now")
	inWindow.Status, inWindow.StartsAt, inWindow.EndsAt = StatusApproved, &past, &future

	for _, ad := range []*Ad{approved, pending, rejected, paused, expired, notYet, inWindow} {
		if err := s.CreateAd(ctx, ad); err != nil {
			t.Fatalf("create %s: %v", ad.ID, err)
		}
	}

	got, err := s.Playlist(ctx, now)
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	ids := map[string]bool{}
	for _, a := range got {
		ids[a.ID] = true
	}
	for _, want := range []string{"live", "window"} {
		if !ids[want] {
			t.Errorf("playlist is missing %q", want)
		}
	}
	for _, unwanted := range []string{"pending", "rejected", "paused", "expired", "notyet"} {
		if ids[unwanted] {
			t.Errorf("playlist wrongly includes %q", unwanted)
		}
	}
}

func TestPlaylistOrderFollowsReorder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"first", "second", "third"} {
		ad := newAd(id, id)
		ad.Status = StatusApproved
		if err := s.CreateAd(ctx, ad); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := s.Reorder(ctx, []string{"third", "first", "second"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	got, err := s.Playlist(ctx, time.Now())
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	want := []string{"third", "first", "second"}
	if len(got) != len(want) {
		t.Fatalf("got %d ads, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("position %d: got %q, want %q", i, got[i].ID, id)
		}
	}
}

// UpdateAd takes **time.Time so that "leave the date alone" and "clear the
// date" are distinguishable. Both directions are easy to get backwards.
func TestUpdateAdClearsAndSetsDates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	end := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	ad := newAd("d1", "Dated")
	ad.EndsAt = &end
	if err := s.CreateAd(ctx, ad); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Untouched by an update that does not mention the field.
	title := "Renamed"
	got, err := s.UpdateAd(ctx, "d1", AdUpdate{Title: &title})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.EndsAt == nil || !got.EndsAt.Equal(end) {
		t.Errorf("EndsAt should have survived a title-only update, got %v", got.EndsAt)
	}

	// Explicitly cleared.
	var none *time.Time
	got, err = s.UpdateAd(ctx, "d1", AdUpdate{EndsAt: &none})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got.EndsAt != nil {
		t.Errorf("EndsAt should be nil after clearing, got %v", got.EndsAt)
	}
}

func TestDeleteReturnsMediaFile(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateAd(ctx, newAd("gone", "Delete me")); err != nil {
		t.Fatalf("create: %v", err)
	}
	file, err := s.DeleteAd(ctx, "gone")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if file != "gone.png" {
		t.Errorf("got media file %q, want gone.png", file)
	}
	if _, err := s.GetAd(ctx, "gone"); err != ErrNotFound {
		t.Errorf("ad should be gone, got %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateSession(ctx, "livehash", "a@b.c", "A", true, time.Hour); err != nil {
		t.Fatalf("create live session: %v", err)
	}
	if err := s.CreateSession(ctx, "deadhash", "a@b.c", "A", true, -time.Hour); err != nil {
		t.Fatalf("create expired session: %v", err)
	}

	if sess, err := s.GetSession(ctx, "livehash"); err != nil || !sess.IsAdmin {
		t.Errorf("live session: %+v, %v", sess, err)
	}
	// An expired session must read as absent, not as a valid one.
	if _, err := s.GetSession(ctx, "deadhash"); err != ErrNotFound {
		t.Errorf("expired session: got %v, want ErrNotFound", err)
	}
}
