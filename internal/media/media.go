// Package media handles uploaded files: streaming them to disk, checking that
// they are actually the media type they claim to be, and probing videos for
// their real duration.
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrUnsupportedType = errors.New("unsupported file type")

// allowedTypes maps a sniffed MIME type to the ad kind it produces and the
// extension we store it under. Anything not listed here is refused; the sniff
// is done on the file's own bytes, never on the client's Content-Type.
var allowedTypes = map[string]struct {
	Kind string
	Ext  string
}{
	"image/jpeg":      {"image", ".jpg"},
	"image/png":       {"image", ".png"},
	"image/gif":       {"image", ".gif"},
	"image/webp":      {"image", ".webp"},
	"image/avif":      {"image", ".avif"},
	"video/mp4":       {"video", ".mp4"},
	"video/webm":      {"video", ".webm"},
	"video/quicktime": {"video", ".mov"},
}

// Store writes uploads into a single flat directory.
type Store struct{ dir string }

func NewStore(dir string) *Store { return &Store{dir: dir} }

// Saved describes a file that made it to disk.
type Saved struct {
	File       string // basename, e.g. "9f2c...ab.mp4"
	Kind       string // image | video
	MIME       string
	Bytes      int64
	DurationMs int // from ffprobe for videos; 0 when unknown
}

// Save streams r to disk, refusing anything over maxBytes or of a type we do
// not render. Nothing is written to the final path until the whole file has
// been read and accepted, so a rejected or aborted upload leaves no residue.
func (s *Store) Save(ctx context.Context, r io.Reader, maxBytes int64) (*Saved, error) {
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	// Read the sniff window first so we can reject a bad type before spending
	// disk on the rest of the body.
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		cleanup()
		return nil, fmt.Errorf("read upload: %w", err)
	}
	head = head[:n]
	if len(head) == 0 {
		cleanup()
		return nil, fmt.Errorf("empty file")
	}

	detected := http.DetectContentType(head)
	if i := strings.IndexByte(detected, ';'); i >= 0 {
		detected = strings.TrimSpace(detected[:i])
	}
	spec, ok := allowedTypes[detected]
	if !ok {
		cleanup()
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, detected)
	}

	written, err := io.Copy(tmp, io.MultiReader(bytes.NewReader(head), io.LimitReader(r, maxBytes-int64(len(head))+1)))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("write upload: %w", err)
	}
	if written > maxBytes {
		cleanup()
		return nil, fmt.Errorf("file exceeds the %s limit", humanBytes(maxBytes))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("close upload: %w", err)
	}

	name := randomName() + spec.Ext
	final := filepath.Join(s.dir, name)
	if err := os.Rename(tmpPath, final); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("store upload: %w", err)
	}
	if err := os.Chmod(final, 0o644); err != nil {
		return nil, fmt.Errorf("chmod upload: %w", err)
	}

	saved := &Saved{File: name, Kind: spec.Kind, MIME: detected, Bytes: written}
	if spec.Kind == "video" {
		saved.DurationMs = probeDurationMs(ctx, final)
	}
	return saved, nil
}

func (s *Store) Path(file string) string { return filepath.Join(s.dir, filepath.Base(file)) }

// Remove deletes a stored file, ignoring one that is already gone.
func (s *Store) Remove(file string) error {
	if file == "" {
		return nil
	}
	err := os.Remove(s.Path(file))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// GC removes files in the media dir that no ad references, plus abandoned
// temp files. Called on a timer so a crashed upload cannot leak disk forever.
func (s *Store) GC(referenced map[string]bool, olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if referenced[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue // too new to be sure it is not an in-flight upload
		}
		if os.Remove(filepath.Join(s.dir, name)) == nil {
			removed++
		}
	}
	return removed, nil
}

// probeDurationMs asks ffprobe how long a video actually is, so a submitter
// does not have to guess and a 6-second clip does not sit on screen for 30.
// ffprobe is optional: without it we return 0 and the caller keeps its default.
func probeDurationMs(ctx context.Context, path string) int {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error", "-show_entries", "format=duration",
		"-print_format", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(out, &probe) != nil {
		return 0
	}
	secs, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return int(secs * 1000)
}

func randomName() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// ContentType returns the MIME type to serve a stored file with, based on its
// extension. Serving video/* with the right type is what lets the browser seek.
func ContentType(file string) string {
	if t := mime.TypeByExtension(filepath.Ext(file)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func humanBytes(n int64) string {
	const unit = 1 << 10
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
