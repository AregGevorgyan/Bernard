// Command bernard runs the Startup Shell billboard: the submission portal, the
// admin console, and the playlist API that the TV's browser renders.
//
// Everything ships in one static binary — the SQLite driver is pure Go and the
// web assets are embedded — so deploying is scp plus systemctl restart.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bernard/internal/config"
	"bernard/internal/httpapi"
	"bernard/internal/store"
	"bernard/web"
)

// Version is stamped by the Makefile: -ldflags "-X main.Version=..."
var Version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("bernard", Version)
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	srv := httpapi.New(cfg, st,
		httpapi.MustSub(web.Portal, "portal/dist"),
		httpapi.MustSub(web.Display, "display"))

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv,
		// No WriteTimeout: the SSE stream is meant to stay open for weeks, and
		// a write deadline would sever it on a timer. ReadHeaderTimeout still
		// covers the slow-header attack that timeout is usually there for.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go backgroundJobs(ctx, st, srv)

	slog.Info("bernard starting",
		"version", Version, "addr", cfg.Addr, "data", cfg.DataDir,
		"auth", authMode(cfg), "admins", len(cfg.AdminEmails))
	if !cfg.GoogleEnabled() {
		slog.Warn("running with password auth — configure BERNARD_GOOGLE_CLIENT_ID before exposing this to the internet")
	}
	if len(cfg.AdminEmails) == 0 && cfg.GoogleEnabled() {
		slog.Warn("BERNARD_ADMIN_EMAILS is empty — nobody can reach the admin console")
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func authMode(cfg *config.Config) string {
	if cfg.GoogleEnabled() {
		if len(cfg.AllowedDomains) > 0 {
			return "google (" + strings.Join(cfg.AllowedDomains, ",") + ")"
		}
		return "google (any domain)"
	}
	return "password"
}

// backgroundJobs runs the periodic housekeeping that keeps a machine bolted to
// a wall healthy for months: expiring sessions, orphaned files, an unbounded
// play-event log, and scheduled ads whose window opens or closes.
func backgroundJobs(ctx context.Context, st *store.Store, srv *httpapi.Server) {
	// Scheduled ads change the playlist without anyone touching the console, so
	// the display has to be told. Comparing the playlist's shape once a minute
	// is cheaper and more reliable than trying to set timers per ad.
	schedule := time.NewTicker(time.Minute)
	defer schedule.Stop()
	housekeeping := time.NewTicker(6 * time.Hour)
	defer housekeeping.Stop()

	lastShape := playlistShape(ctx, st)

	for {
		select {
		case <-ctx.Done():
			return

		case <-schedule.C:
			if shape := playlistShape(ctx, st); shape != lastShape {
				slog.Info("playlist changed on schedule")
				lastShape = shape
				srv.Hub().PlaylistChanged()
			}

		case <-housekeeping.C:
			if n, err := st.PurgeExpiredSessions(ctx); err == nil && n > 0 {
				slog.Info("purged expired sessions", "count", n)
			}
			if n, err := st.PruneOldPlayEvents(ctx, 90*24*time.Hour); err == nil && n > 0 {
				slog.Info("pruned old play events", "count", n)
			}
			if n, err := srv.CollectOrphanedMedia(ctx); err != nil {
				slog.Warn("media gc", "err", err)
			} else if n > 0 {
				slog.Info("collected orphaned media", "count", n)
			}
		}
	}
}

// playlistShape is a cheap fingerprint of what should currently be on screen.
func playlistShape(ctx context.Context, st *store.Store) string {
	ads, err := st.Playlist(ctx, time.Now())
	if err != nil {
		return "error"
	}
	var b strings.Builder
	for _, a := range ads {
		fmt.Fprintf(&b, "%s:%d;", a.ID, a.DurationMs)
	}
	return b.String()
}
