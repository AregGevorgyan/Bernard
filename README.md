# Bernard

Billboard software for [Startup Shell](https://startupshell.org). Members upload
an image, video or HTML slide from their phone; an organizer approves it; it
appears on the TV within a second.

One static Go binary serves the portal, the admin console, the media files and
the playlist. The TV is a Chromium window in a Wayland kiosk pointed at
`/display`. No Node, no runtime dependencies, no database server.

```
        member's phone                 organizer's laptop
              │                                │
              ▼                                ▼
        ┌───────────────────────────────────────────┐
        │  bernard  (one binary, :8080)             │
        │  React portal · SQLite · media · SSE      │
        └───────────────────┬───────────────────────┘
                            │  /display + /api/events
                            ▼
                   Chromium in cage  ──HDMI──▶  TV
```

## Why this is a rewrite and not a port

It replaces [ShellNews-Bernard](https://github.com/exoad/ShellNews-Bernard) by
[Jack Meng (exoad)](https://exoad.net/), which was a Wails desktop app with a Go
launcher supervising it. That build is where the whole idea came from — members
submit, an organizer approves, the TV rotates — and this one keeps that model,
the review pipeline and the ad settings almost unchanged. What changed is the
platform: it was written for Windows, and the NUC runs Ubuntu Server.

Four things in the old design were load-bearing problems, and each one is gone
here rather than carried across:

| Old | Now |
| --- | --- |
| Admin password hardcoded in the source and printed to the log; Google sign-in decoded client-side, so the server trusted whatever email the browser sent | Google ID tokens verified server-side against Google's JWKS; admins are an env-var allowlist re-checked every request |
| Kiosk restarted every 3 hours to stay healthy | Two reused DOM layers, explicit media release, and one page reload a day at 4am |
| Uploads base64'd into a JSON body with a 3 GB cap, buffered in memory | Multipart streamed to disk, capped at 256 MB, type sniffed from the file's own bytes |
| Hourly self-update pulling a Windows zip from a personal GitHub account | `make deploy` — scp and restart |

## Quick start on the NUC

```bash
git clone git@github.com:AregGevorgyan/Bernard.git && cd Bernard
make build            # needs Go 1.24+ and Node 20+
sudo ./deploy/install.sh
sudo nano /etc/bernard/bernard.env      # Google client ID, admin emails
sudo systemctl restart bernard
sudo systemctl start bernard-kiosk      # TV lights up
```

Building somewhere else and shipping the result works too, and is the usual way
to update:

```bash
make build-nuc               # static linux/amd64, no cross-compiler needed
make deploy NUC=kiosk@bernard.local
```

External access goes through a Cloudflare Tunnel — see
[deploy/cloudflared.md](deploy/cloudflared.md). No port forward, no public IP.

## Development

```bash
make dev     # portal on :5173 (proxying the API), display on :8080/display
```

With no `BERNARD_GOOGLE_CLIENT_ID` set, Bernard falls back to password login
(`BERNARD_DEV_PASSWORD`, default `dev` under `make dev`) and grants admin to
anyone who knows it. That is fine on a laptop and wrong on the NUC; the server
logs a warning at every start in that mode.

The display page is a single hand-written file, `web/display/index.html`, with
no build step. It is the piece that has to run for months unattended, so it is
deliberately the piece with no framework, no dependencies, and no bundler.

`make test` runs the Go tests. `?debug` on the display URL shows an overlay with
the current slide, the SSE state and uptime; `d` toggles it from a keyboard
plugged into the NUC, and the arrow keys step through slides.

## Configuration

Everything is environment variables; see
[deploy/bernard.env.example](deploy/bernard.env.example) for the annotated set.
The ones that matter:

| Variable | Meaning |
| --- | --- |
| `BERNARD_GOOGLE_CLIENT_ID` | Google OAuth web client ID. Its absence is what enables password mode. |
| `BERNARD_ALLOWED_DOMAINS` | Email domains allowed to submit. Empty means any Google account. |
| `BERNARD_ADMIN_EMAILS` | Exact addresses allowed to approve. |
| `BERNARD_PUBLIC_URL` | External origin. An `https://` value makes the session cookie `Secure`. |
| `BERNARD_DISPLAY_RELOAD_HOUR` | Hour the TV page reloads itself. `-1` disables. |

## How an ad reaches the screen

1. A member signs in with Google and uploads a file. The server verifies the
   token, streams the file to `/var/lib/bernard/media`, sniffs its real type,
   and — for video — asks `ffprobe` how long it actually is.
2. It lands as `pending`. Nothing else happens.
3. An organizer approves it. There is no second "activate" step: approving puts
   it on screen, because in the old build that extra click was the most common
   reason an approved ad never appeared.
4. The server pushes an SSE nudge; every connected display re-fetches the
   playlist and folds the new ad into the rotation without restarting it.
5. Optional: a take-down date removes it on its own. A background job notices
   the window closing within a minute and nudges the displays again.

## Operating notes

- **Video is muted.** Browsers only autoplay muted video, and a lobby TV that
  makes noise is a lobby TV someone unplugs.
- **Hardware decode matters.** `install.sh` pulls the Intel VA-API drivers;
  check them with `vainfo`. Without them 1080p h264 will peg the NUC's CPU.
- **HDMI-CEC** (turning the TV on and off on a schedule) needs a Pulse-Eight
  adapter — Intel NUCs do not expose CEC over HDMI. Until then, use the TV's own
  sleep timer.
- **Backups** are one file: `/var/lib/bernard/bernard.db`, plus the `media/`
  directory beside it.
- **Boot time is worth checking** with `systemd-analyze blame`. A NIC that is
  configured but unplugged makes `systemd-networkd-wait-online` burn its full
  120-second timeout, and since the kiosk starts at `graphical.target` the TV
  stays black for all of it. Either mark the unused interface `optional: true`
  in netplan, or drop in an override:

  ```ini
  # /etc/systemd/system/systemd-networkd-wait-online.service.d/any-link.conf
  [Service]
  ExecStart=
  ExecStart=/lib/systemd/systemd-networkd-wait-online --any -o routable --timeout=20
  ```

  `install.sh` deliberately does not do this for you — it is a system-wide
  network policy, and a server install may legitimately want to wait.
- `journalctl -u bernard -f` and `journalctl -u bernard-kiosk -f`.

## Credits

The original Bernard was built by [Jack Meng](https://exoad.net/) — [exoad/ShellNews-Bernard](https://github.com/exoad/ShellNews-Bernard).
The submission-and-approval flow, the ad settings and the name are all his; this
is a Linux rewrite of his idea, not a new one.
