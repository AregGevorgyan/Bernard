#!/usr/bin/env python3
"""Stop libinput treating keyboards as pointers, so the TV shows no cursor.

Many USB keyboards expose a second HID interface for their multimedia keys that
also advertises relative axes. libinput reads those axis bits directly and gives
the device `pointer` capability, so the compositor creates a pointer and parks a
cursor in the middle of the screen. Nothing ever moves it, so the browser never
receives a pointer-enter event and the page's `cursor: none` never applies.

A device reporting BOTH keyboard and pointer is the signature of that quirk — a
real mouse reports pointer alone and is left working, so plugging one in to
debug still behaves normally.

Writes udev rules to /etc/udev/rules.d and reloads. Run as root.
"""

import re
import subprocess
import sys

RULES_PATH = "/etc/udev/rules.d/99-bernard-kiosk-phantom-pointer.rules"


def list_devices() -> list[dict]:
    """Parse `libinput list-devices` into one dict per device block."""
    try:
        out = subprocess.run(
            ["libinput", "list-devices"], capture_output=True, text=True, check=True
        ).stdout
    except (subprocess.CalledProcessError, FileNotFoundError) as err:
        print(f"cannot run libinput list-devices: {err}", file=sys.stderr)
        return []

    devices, current = [], {}
    for line in out.splitlines():
        if not line.strip():
            if current:
                devices.append(current)
                current = {}
            continue
        if ":" in line:
            key, _, value = line.partition(":")
            current[key.strip().lower()] = value.strip()
    if current:
        devices.append(current)
    return devices


def udev_props(devnode: str) -> dict:
    try:
        out = subprocess.run(
            ["udevadm", "info", "-q", "property", "-n", devnode],
            capture_output=True, text=True, check=True,
        ).stdout
    except subprocess.CalledProcessError:
        return {}
    return dict(
        line.split("=", 1) for line in out.splitlines() if "=" in line
    )


def main() -> int:
    phantoms = []
    for dev in list_devices():
        caps = dev.get("capabilities", "").split()
        if "keyboard" in caps and "pointer" in caps:
            phantoms.append(dev)

    if not phantoms:
        print("no phantom pointers found — nothing to do")
        return 0

    lines = [
        "# Written by deploy/ignore-phantom-pointers.py.",
        "# These devices report both keyboard and pointer capability, which makes",
        "# the compositor draw a cursor on a display that has no mouse.",
        "",
    ]
    for dev in phantoms:
        node = dev.get("kernel", "")
        props = udev_props(node)
        vendor = props.get("ID_VENDOR_ID")
        model = props.get("ID_MODEL_ID")
        iface = props.get("ID_USB_INTERFACE_NUM")
        name = dev.get("device", "unknown")

        if not (vendor and model):
            print(f"skipping {name} ({node}): no USB ids to match on", file=sys.stderr)
            continue

        match = [
            'SUBSYSTEM=="input"',
            'KERNEL=="event*"',
            f'ATTRS{{idVendor}}=="{vendor}"',
            f'ATTRS{{idProduct}}=="{model}"',
        ]
        # Pin to the offending interface so the real typing interface of the
        # same keyboard keeps working.
        if iface:
            match.append(f'ENV{{ID_USB_INTERFACE_NUM}}=="{iface}"')
        lines.append(f"# {name} ({node})")
        lines.append(", ".join(match) + ', ENV{LIBINPUT_IGNORE_DEVICE}="1"')
        print(f"ignoring {name} ({node}) — {vendor}:{model} interface {iface}")

    with open(RULES_PATH, "w") as fh:
        fh.write("\n".join(lines) + "\n")
    print(f"wrote {RULES_PATH}")

    subprocess.run(["udevadm", "control", "--reload"], check=False)
    subprocess.run(
        ["udevadm", "trigger", "--subsystem-match=input", "--action=change"], check=False
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
