package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pngHeader = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R'}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return NewStore(dir), dir
}

func TestSaveAcceptsRealImage(t *testing.T) {
	s, dir := newStore(t)
	body := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{0x42}, 2048)...)

	saved, err := s.Save(context.Background(), bytes.NewReader(body), 1<<20)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Kind != "image" || saved.MIME != "image/png" {
		t.Errorf("got kind=%q mime=%q, want image/image/png", saved.Kind, saved.MIME)
	}
	if !strings.HasSuffix(saved.File, ".png") {
		t.Errorf("stored name %q should end in .png", saved.File)
	}
	if saved.Bytes != int64(len(body)) {
		t.Errorf("got %d bytes, want %d", saved.Bytes, len(body))
	}
	if _, err := os.Stat(filepath.Join(dir, saved.File)); err != nil {
		t.Errorf("file is not on disk: %v", err)
	}
}

// The declared Content-Type is never consulted; only the bytes are. A file that
// claims to be a PNG and is actually a script has to be refused.
func TestSaveRejectsDisguisedFile(t *testing.T) {
	s, dir := newStore(t)

	_, err := s.Save(context.Background(), strings.NewReader("#!/bin/sh\necho pwned\n"), 1<<20)
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("got %v, want ErrUnsupportedType", err)
	}
	assertDirEmpty(t, dir)
}

func TestSaveRejectsOversizeAndLeavesNothingBehind(t *testing.T) {
	s, dir := newStore(t)
	body := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{0x42}, 4096)...)

	if _, err := s.Save(context.Background(), bytes.NewReader(body), 1024); err == nil {
		t.Fatal("oversize upload should have been refused")
	}
	// A rejected upload that leaves its temp file behind fills the NUC's disk
	// one attempt at a time.
	assertDirEmpty(t, dir)
}

func TestSaveRejectsEmptyFile(t *testing.T) {
	s, dir := newStore(t)
	if _, err := s.Save(context.Background(), bytes.NewReader(nil), 1<<20); err == nil {
		t.Fatal("empty upload should have been refused")
	}
	assertDirEmpty(t, dir)
}

func TestSaveAcceptsExactlyTheLimit(t *testing.T) {
	s, _ := newStore(t)
	const limit = 4096
	body := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{0x42}, limit-len(pngHeader))...)

	if _, err := s.Save(context.Background(), bytes.NewReader(body), limit); err != nil {
		t.Fatalf("a file exactly at the limit should be accepted: %v", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Remove("never-existed.png"); err != nil {
		t.Errorf("removing a missing file should be a no-op, got %v", err)
	}
	if err := s.Remove(""); err != nil {
		t.Errorf("removing an empty name should be a no-op, got %v", err)
	}
}

func TestGCKeepsReferencedAndRecentFiles(t *testing.T) {
	s, dir := newStore(t)
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write("referenced.png", 48*time.Hour)
	write("orphan.png", 48*time.Hour)
	write("just-uploaded.png", time.Minute) // still in flight as far as GC knows

	removed, err := s.GC(map[string]bool{"referenced.png": true}, time.Hour)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d files, want 1", removed)
	}
	for _, keep := range []string{"referenced.png", "just-uploaded.png"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s should have survived GC", keep)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "orphan.png")); !os.IsNotExist(err) {
		t.Error("orphan.png should have been collected")
	}
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected an empty media dir, found %v", names)
	}
}
