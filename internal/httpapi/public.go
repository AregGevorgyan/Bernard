package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"bernard/internal/auth"
	"bernard/internal/media"
	"bernard/internal/store"
)

// ─── Public config ────────────────────────────────────────────────────────────

// handlePublicConfig tells the portal how to render the sign-in screen and what
// limits to enforce client-side, so those numbers live in exactly one place.
func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"googleClientId":    s.cfg.GoogleClientID,
		"passwordLogin":     !s.cfg.GoogleEnabled(),
		"allowedDomains":    s.cfg.AllowedDomains,
		"maxUploadBytes":    s.cfg.MaxUploadBytes,
		"defaultDurationMs": s.cfg.DefaultDurationMs,
		"maxDurationMs":     s.cfg.MaxDurationMs,
	})
}

// ─── Playlist ─────────────────────────────────────────────────────────────────

type playlistItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Src         string `json:"src,omitempty"`
	HTML        string `json:"html,omitempty"`
	DurationMs  int    `json:"durationMs"`
	Fit         string `json:"fit"`
	Background  string `json:"background"`
	SubmittedBy string `json:"submittedBy"`
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	ads, err := s.store.Playlist(r.Context(), time.Now())
	if err != nil {
		slog.Error("build playlist", "err", err)
		writeError(w, http.StatusInternalServerError, "could not build the playlist")
		return
	}
	items := make([]playlistItem, 0, len(ads))
	for _, a := range ads {
		item := playlistItem{
			ID: a.ID, Title: a.Title, Kind: a.Kind,
			DurationMs: a.DurationMs, Fit: a.Fit, Background: a.Background,
			SubmittedBy: a.SubmitterName,
		}
		switch a.Kind {
		case store.KindHTML:
			item.HTML = a.HTML
		default:
			item.Src = "/media/" + a.MediaFile
		}
		items = append(items, item)
	}
	// The display must never serve a cached playlist after an approval.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"generatedAt": time.Now().Format(time.RFC3339),
		"reloadHour":  s.cfg.DisplayReloadHour,
	})
}

func (s *Server) handlePlayEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AdID string `json:"adId"`
	}
	if err := decodeJSON(r, &body); err != nil || body.AdID == "" {
		writeError(w, http.StatusBadRequest, "adId is required")
		return
	}
	if err := s.store.RecordPlay(r.Context(), body.AdID); err != nil {
		slog.Warn("record play", "err", err, "ad", body.AdID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Media ────────────────────────────────────────────────────────────────────

// handleMedia serves uploaded files. http.ServeFile gives us range requests for
// free, which is what lets the TV seek and buffer video properly.
func (s *Server) handleMedia() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.PathValue("file"))
		if name == "." || name == "/" || name == ".." || strings.ContainsAny(name, `/\`) {
			http.NotFound(w, r)
			return
		}
		// Stored names are random hex plus an extension and never change, so
		// they are safe to cache hard.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", media.ContentType(name))
		http.ServeFile(w, r, s.media.Path(name))
	})
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		writeError(w, http.StatusBadRequest, "google sign-in is not configured on this server")
		return
	}
	var body struct {
		Credential string `json:"credential"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Credential == "" {
		writeError(w, http.StatusBadRequest, "missing credential")
		return
	}
	id, err := s.google.Verify(r.Context(), body.Credential)
	if err != nil {
		slog.Warn("rejected google sign-in", "err", err)
		writeError(w, http.StatusUnauthorized, "could not verify that Google account")
		return
	}
	if !s.cfg.DomainAllowed(id.Email) {
		writeError(w, http.StatusForbidden,
			"this site is limited to "+strings.Join(s.cfg.AllowedDomains, ", ")+" accounts")
		return
	}
	s.finishLogin(w, r, *id)
}

// handlePasswordLogin is the fallback for installs with no Google client ID.
// It grants admin, because the only reason to run in this mode is local setup.
func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GoogleEnabled() || s.cfg.DevPassword == "" {
		writeError(w, http.StatusBadRequest, "password login is disabled")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}
	if !auth.ConstantTimeEqual(body.Password, s.cfg.DevPassword) {
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		email = "local@bernard"
	}
	s.finishLogin(w, r, auth.Identity{Email: email, Name: email})
}

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	// In password mode there is no identity provider, so everyone who knows the
	// password is an admin; with Google the admin list decides.
	isAdmin := s.cfg.IsAdmin(id.Email) || !s.cfg.GoogleEnabled()
	if err := s.sessions.Issue(r.Context(), w, id, isAdmin); err != nil {
		slog.Error("issue session", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start a session")
		return
	}
	slog.Info("sign-in", "email", id.Email, "admin", isAdmin)
	writeJSON(w, http.StatusOK, map[string]any{
		"email": id.Email, "name": id.Name, "isAdmin": isAdmin,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.Revoke(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.Lookup(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"signedIn": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"signedIn": true,
		"email":    sess.Email,
		"name":     sess.Name,
		"isAdmin":  s.isAdmin(sess),
	})
}

// ─── Submissions ──────────────────────────────────────────────────────────────

// handleCreateSubmission accepts a multipart form: a title, the settings, and
// either a file part or an html field.
//
// The body is streamed straight to disk through r.MultipartReader. The old
// build base64-encoded uploads into a JSON field with a 3 GB cap, which meant a
// single submission could hold four gigabytes of RAM on a machine that has
// eight; this never buffers more than a chunk.
func (s *Server) handleCreateSubmission(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())

	// A little headroom over the file cap for the other form fields.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+(1<<20))

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart form")
		return
	}

	ad := &store.Ad{
		ID:             newID(),
		Status:         store.StatusPending,
		Enabled:        true,
		Fit:            "contain",
		Background:     "#000000",
		DurationMs:     s.cfg.DefaultDurationMs,
		SubmitterEmail: sess.Email,
		SubmitterName:  sess.Name,
	}
	var saved *media.Saved
	var durationGiven bool

	cleanup := func() {
		if saved != nil {
			_ = s.media.Remove(saved.File)
		}
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			writeError(w, http.StatusBadRequest, "could not read the upload: "+err.Error())
			return
		}

		switch part.FormName() {
		case "file":
			if part.FileName() == "" {
				part.Close()
				continue
			}
			saved, err = s.media.Save(r.Context(), part, s.cfg.MaxUploadBytes)
			part.Close()
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, media.ErrUnsupportedType) {
					err = errors.New("that file type is not supported — use JPEG, PNG, GIF, WebP, AVIF, MP4, WebM or MOV")
				}
				writeError(w, status, err.Error())
				return
			}
			ad.Kind, ad.MediaFile, ad.MIME, ad.Bytes = saved.Kind, saved.File, saved.MIME, saved.Bytes

		default:
			value, err := readFieldValue(part)
			part.Close()
			if err != nil {
				cleanup()
				writeError(w, http.StatusBadRequest, "form field too large")
				return
			}
			switch part.FormName() {
			case "title":
				ad.Title = strings.TrimSpace(value)
			case "html":
				if v := strings.TrimSpace(value); v != "" {
					ad.Kind, ad.HTML = store.KindHTML, v
				}
			case "durationMs":
				if n, err := strconv.Atoi(value); err == nil {
					ad.DurationMs, durationGiven = n, true
				}
			case "fit":
				if value == "cover" || value == "contain" {
					ad.Fit = value
				}
			case "background":
				if isCSSColor(value) {
					ad.Background = value
				}
			case "startsAt":
				if t, ok := parseFormTime(value); ok {
					ad.StartsAt = t
				}
			case "endsAt":
				if t, ok := parseFormTime(value); ok {
					ad.EndsAt = t
				}
			}
		}
	}

	if ad.Title == "" {
		cleanup()
		writeError(w, http.StatusBadRequest, "give your ad a title")
		return
	}
	if ad.Kind == "" {
		writeError(w, http.StatusBadRequest, "attach an image or video, or write an HTML slide")
		return
	}
	// A submitter who did not choose a duration gets the video's real length,
	// so a 6-second clip is not padded out to the 10-second default.
	if !durationGiven && saved != nil && saved.DurationMs > 0 {
		ad.DurationMs = saved.DurationMs
	}
	if ad.DurationMs < 1000 {
		ad.DurationMs = 1000
	}
	if ad.DurationMs > s.cfg.MaxDurationMs {
		ad.DurationMs = s.cfg.MaxDurationMs
	}
	if ad.StartsAt != nil && ad.EndsAt != nil && ad.EndsAt.Before(*ad.StartsAt) {
		cleanup()
		writeError(w, http.StatusBadRequest, "the end date is before the start date")
		return
	}

	if err := s.store.CreateAd(r.Context(), ad); err != nil {
		cleanup()
		slog.Error("create ad", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save your submission")
		return
	}
	slog.Info("submission", "id", ad.ID, "by", sess.Email, "kind", ad.Kind, "bytes", ad.Bytes)
	writeJSON(w, http.StatusCreated, ad)
}

// readFieldValue reads a non-file form field, capped so a malicious client
// cannot send an unbounded "title".
func readFieldValue(r io.Reader) (string, error) {
	const maxField = 512 << 10 // generous: HTML slides come through here
	b, err := io.ReadAll(io.LimitReader(r, maxField+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxField {
		return "", errors.New("field too large")
	}
	return string(b), nil
}

func (s *Server) handleMySubmissions(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ads, err := s.store.ListBySubmitter(r.Context(), sess.Email)
	if err != nil {
		slog.Error("list submissions", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load your submissions")
		return
	}
	counts, err := s.store.PlayCounts(r.Context())
	if err != nil {
		counts = map[string]int{}
	}
	type row struct {
		*store.Ad
		Plays int `json:"plays"`
	}
	out := make([]row, 0, len(ads))
	for _, a := range ads {
		out = append(out, row{Ad: a, Plays: counts[a.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteMySubmission(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	id := r.PathValue("id")

	ad, err := s.store.GetAd(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such submission")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load that submission")
		return
	}
	// Ownership is checked against the session, not against an email the
	// client sends along with the request.
	if ad.SubmitterEmail != sess.Email {
		writeError(w, http.StatusForbidden, "that is not your submission")
		return
	}

	file, err := s.store.DeleteAd(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not withdraw that submission")
		return
	}
	if err := s.media.Remove(file); err != nil {
		slog.Warn("remove media", "err", err, "file", file)
	}
	if ad.Playing(time.Now()) {
		s.hub.PlaylistChanged()
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Small helpers ────────────────────────────────────────────────────────────

func parseFormTime(v string) (*time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	// Browsers send datetime-local without a zone; interpret it as local time,
	// which is what the person filling the form meant.
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return &t, true
		}
	}
	return nil, false
}

// isCSSColor accepts only #rgb / #rrggbb, which is all the picker emits and
// all we are willing to interpolate into a style attribute.
func isCSSColor(v string) bool {
	if len(v) != 4 && len(v) != 7 {
		return false
	}
	if v[0] != '#' {
		return false
	}
	for _, c := range v[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

func init() {
	// Ubuntu Server images ship a thin mime.types; register what we serve so
	// video/* is never guessed as octet-stream.
	for ext, typ := range map[string]string{
		".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
		".avif": "image/avif", ".webp": "image/webp",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}
