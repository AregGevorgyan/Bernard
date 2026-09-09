package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"bernard/internal/store"
)

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}

type adminAd struct {
	*store.Ad
	Plays   int    `json:"plays"`
	Src     string `json:"src,omitempty"`
	Playing bool   `json:"playing"`
}

func (s *Server) handleAdminList(w http.ResponseWriter, r *http.Request) {
	ads, err := s.store.ListAds(r.Context())
	if err != nil {
		slog.Error("list ads", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load the queue")
		return
	}
	counts, err := s.store.PlayCounts(r.Context())
	if err != nil {
		counts = map[string]int{}
	}
	now := time.Now()
	out := make([]adminAd, 0, len(ads))
	for _, a := range ads {
		row := adminAd{Ad: a, Plays: counts[a.ID], Playing: a.Playing(now)}
		if a.MediaFile != "" {
			row.Src = "/media/" + a.MediaFile
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.CountsByStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load stats")
		return
	}
	live, err := s.store.Playlist(r.Context(), time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load stats")
		return
	}
	var rotationMs int
	for _, a := range live {
		rotationMs += a.DurationMs
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":        counts[store.StatusPending],
		"approved":       counts[store.StatusApproved],
		"rejected":       counts[store.StatusRejected],
		"onScreen":       len(live),
		"rotationMs":     rotationMs,
		"displaysOnline": s.hub.Count(),
	})
}

// handleAdminReview approves or rejects a submission. Approval alone puts the
// ad on screen; there is no separate "activate" step, because in the previous
// build that extra click was the most common reason an approved ad never
// actually appeared.
func (s *Server) handleAdminReview(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var body struct {
		Decision string `json:"decision"` // approve | reject
		Note     string `json:"note"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}
	var status string
	switch body.Decision {
	case "approve":
		status = store.StatusApproved
	case "reject":
		status = store.StatusRejected
	default:
		writeError(w, http.StatusBadRequest, `decision must be "approve" or "reject"`)
		return
	}

	ad, err := s.store.UpdateAd(r.Context(), r.PathValue("id"), store.AdUpdate{
		Status:     &status,
		ReviewNote: &body.Note,
		ReviewedBy: &sess.Email,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such ad")
		return
	}
	if err != nil {
		slog.Error("review ad", "err", err)
		writeError(w, http.StatusInternalServerError, "could not record that decision")
		return
	}
	slog.Info("review", "id", ad.ID, "decision", body.Decision, "by", sess.Email)
	s.hub.PlaylistChanged()
	writeJSON(w, http.StatusOK, ad)
}

func (s *Server) handleAdminPatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      *string `json:"title"`
		DurationMs *int    `json:"durationMs"`
		Fit        *string `json:"fit"`
		Background *string `json:"background"`
		Enabled    *bool   `json:"enabled"`
		StartsAt   *string `json:"startsAt"`
		EndsAt     *string `json:"endsAt"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	u := store.AdUpdate{Title: body.Title, Enabled: body.Enabled}
	if body.DurationMs != nil {
		d := *body.DurationMs
		if d < 1000 {
			d = 1000
		}
		if d > s.cfg.MaxDurationMs {
			d = s.cfg.MaxDurationMs
		}
		u.DurationMs = &d
	}
	if body.Fit != nil && (*body.Fit == "cover" || *body.Fit == "contain") {
		u.Fit = body.Fit
	}
	if body.Background != nil && isCSSColor(*body.Background) {
		u.Background = body.Background
	}
	// An empty string clears the date; a value sets it.
	if body.StartsAt != nil {
		t, _ := parseFormTime(*body.StartsAt)
		u.StartsAt = &t
	}
	if body.EndsAt != nil {
		t, _ := parseFormTime(*body.EndsAt)
		u.EndsAt = &t
	}

	ad, err := s.store.UpdateAd(r.Context(), r.PathValue("id"), u)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such ad")
		return
	}
	if err != nil {
		slog.Error("patch ad", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save that change")
		return
	}
	s.hub.PlaylistChanged()
	writeJSON(w, http.StatusOK, ad)
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	file, err := s.store.DeleteAd(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such ad")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete that ad")
		return
	}
	if err := s.media.Remove(file); err != nil {
		slog.Warn("remove media", "err", err, "file", file)
	}
	s.hub.PlaylistChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminReorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}
	if err := s.store.Reorder(r.Context(), body.IDs); err != nil {
		slog.Error("reorder", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save the new order")
		return
	}
	s.hub.PlaylistChanged()
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminDisplayCommand pushes an instruction straight to every connected
// display over SSE. This replaces the old build's "restart the kiosk process"
// button: nothing needs restarting when the page can just be told what to do.
func (s *Server) handleAdminDisplayCommand(w http.ResponseWriter, r *http.Request) {
	cmd := r.PathValue("command")
	switch cmd {
	case "next", "prev", "reload":
	default:
		writeError(w, http.StatusBadRequest, "command must be next, prev or reload")
		return
	}
	s.hub.Command(cmd)
	writeJSON(w, http.StatusOK, map[string]any{"sent": cmd, "displays": s.hub.Count()})
}
