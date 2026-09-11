#!/usr/bin/env bash
#
# Sets up Bernard on a fresh Ubuntu Server install. Safe to re-run: it will not
# overwrite an existing /etc/bernard/bernard.env or the database.
#
#   sudo ./deploy/install.sh              # server + TV kiosk (the NUC)
#   sudo ./deploy/install.sh --server-only # server only (a VPS, or testing)
#
set -euo pipefail

KIOSK=true
[[ "${1:-}" == "--server-only" ]] && KIOSK=false

if [[ $EUID -ne 0 ]]; then
  echo "Run this with sudo." >&2
  exit 1
fi

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARY="$REPO/bin/bernard"

if [[ ! -x "$BINARY" ]]; then
  echo "No binary at $BINARY — run 'make build' first (or 'make build-nuc' on another machine and copy it here)." >&2
  exit 1
fi

echo "==> Packages"
apt-get update -qq
PACKAGES=(ca-certificates curl ffmpeg)
if $KIOSK; then
  # cage is the Wayland kiosk compositor. The VA-API driver gives Chromium
  # hardware video decoding on Intel graphics, without which 1080p h264 will
  # peg the CPU and drop frames.
  PACKAGES+=(cage seatd intel-media-va-driver-non-free vainfo
             libinput-tools fonts-liberation fonts-noto-color-emoji)

  # Chromium's package name has moved around: "chromium" on Debian and older
  # Ubuntu, "chromium-browser" on current Ubuntu (where it is a transitional
  # package that installs the snap). Take whichever this release actually has.
  CHROMIUM_PKG=""
  for candidate in chromium chromium-browser; do
    if [[ -n "$(apt-cache policy "$candidate" 2>/dev/null | awk '/Candidate:/ {print $2}' | grep -v '(none)')" ]]; then
      CHROMIUM_PKG="$candidate"
      break
    fi
  done
  if [[ -z "$CHROMIUM_PKG" ]]; then
    echo "No chromium package found in apt. Install a Chromium or Chrome build by hand, then re-run." >&2
    exit 1
  fi
  PACKAGES+=("$CHROMIUM_PKG")
  echo "    chromium package: $CHROMIUM_PKG"

  # mesa-va-drivers was dropped in Ubuntu 26.04; on releases that still carry
  # it, it fills in for non-Intel GPUs.
  if [[ -n "$(apt-cache policy mesa-va-drivers 2>/dev/null | awk '/Candidate:/ {print $2}' | grep -v '(none)')" ]]; then
    PACKAGES+=(mesa-va-drivers)
  fi
fi
DEBIAN_FRONTEND=noninteractive apt-get install -y "${PACKAGES[@]}"

echo "==> Users and directories"
id -u bernard &>/dev/null || useradd --system --home /var/lib/bernard --shell /usr/sbin/nologin bernard
install -d -o bernard -g bernard -m 750 /var/lib/bernard /var/lib/bernard/media
install -d -o root -g bernard -m 750 /etc/bernard

echo "==> Binary"
install -o root -g root -m 755 "$BINARY" /usr/local/bin/bernard

echo "==> Configuration"
if [[ -f /etc/bernard/bernard.env ]]; then
  echo "    /etc/bernard/bernard.env exists — leaving it alone"
else
  install -o root -g bernard -m 640 "$REPO/deploy/bernard.env.example" /etc/bernard/bernard.env
  echo "    wrote /etc/bernard/bernard.env — EDIT IT before going live"
fi

echo "==> Server service"
install -o root -g root -m 644 "$REPO/deploy/bernard.service" /etc/systemd/system/bernard.service

if $KIOSK; then
  echo "==> Kiosk user and service"
  id -u kiosk &>/dev/null || useradd --create-home --shell /bin/bash kiosk
  # seat access is what lets cage open the GPU and the HDMI output.
  usermod -aG video,render,input,seat kiosk 2>/dev/null || usermod -aG video,render,input kiosk
  echo "==> Cursor"
  # Two layers, because a TV should never show a pointer:
  #   1. Drop the phantom pointer capability that many USB keyboards advertise
  #      on their multimedia-key interface. That is the usual culprit, and it is
  #      the only one that removes the cursor outright.
  #   2. A fully transparent cursor theme, in case a real pointer ever shows up.
  python3 "$REPO/deploy/ignore-phantom-pointers.py" || true
  python3 "$REPO/deploy/make-blank-cursor.py" /usr/share/icons/blank

  install -o root -g root -m 755 "$REPO/deploy/bernard-kiosk" /usr/local/bin/bernard-kiosk
  install -o root -g root -m 644 "$REPO/deploy/bernard-kiosk.service" /etc/systemd/system/bernard-kiosk.service
  systemctl enable seatd 2>/dev/null || true

  echo "==> Console blanking"
  # Ubuntu blanks tty1 after ten minutes. On a TV that reads as a broken NUC.
  if ! grep -q "consoleblank=0" /etc/default/grub 2>/dev/null; then
    sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"$/GRUB_CMDLINE_LINUX_DEFAULT="\1 consoleblank=0"/' /etc/default/grub
    update-grub
    echo "    added consoleblank=0 — takes effect after a reboot"
  fi

  # A display has no reason to suspend, and a suspended NUC shows a black TV.
  systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target 2>/dev/null || true
fi

echo "==> Starting"
systemctl daemon-reload
systemctl enable --now bernard
$KIOSK && systemctl enable bernard-kiosk

echo
echo "Bernard is installed."
echo
echo "  1. Edit /etc/bernard/bernard.env — set BERNARD_GOOGLE_CLIENT_ID and BERNARD_ADMIN_EMAILS."
echo "  2. sudo systemctl restart bernard"
if $KIOSK; then
echo "  3. sudo systemctl start bernard-kiosk   (the TV should light up)"
echo "  4. Reboot once so consoleblank=0 takes effect."
fi
echo
echo "Logs:   journalctl -u bernard -f"
$KIOSK && echo "        journalctl -u bernard-kiosk -f"
echo "Portal: http://$(hostname -I | awk '{print $1}'):8080"
