#!/usr/bin/env python3
"""Write a fully transparent XCursor theme.

Cage draws a cursor whenever libinput reports a pointer device, and plenty of
USB keyboards expose a second HID interface that claims pointer capability. The
page's `cursor: none` never gets a chance to apply, because with no mouse the
pointer never moves and the browser never receives a pointer-enter event — so
the default arrow just sits in the middle of the TV forever.

A cursor theme whose every shape is a 1x1 transparent pixel removes it at the
compositor level, which works no matter which client has focus.
"""

import os
import struct
import sys

CHUNK_IMAGE = 0xFFFD0002
NOMINAL_SIZES = (24, 32, 48, 64)

# Cursor names a compositor or toolkit may ask for. left_ptr is the one cage
# uses for the default pointer; the rest are aliased to the same empty image so
# nothing can reintroduce a visible shape.
NAMES = (
    "left_ptr", "default", "arrow", "top_left_arrow", "hand1", "hand2",
    "pointer", "text", "xterm", "watch", "wait", "crosshair", "progress",
)


def build() -> bytes:
    """Return an XCursor file holding one 1x1 fully transparent image per size."""
    header = b"Xcur" + struct.pack("<III", 16, 0x00010000, len(NOMINAL_SIZES))

    toc_size = 12 * len(NOMINAL_SIZES)
    image_chunk_size = 36 + 4  # 36-byte image header + one ARGB pixel
    offset = len(header) + toc_size

    toc, chunks = b"", b""
    for size in NOMINAL_SIZES:
        toc += struct.pack("<III", CHUNK_IMAGE, size, offset)
        chunks += struct.pack(
            "<IIIIIIIII",
            36,           # header length
            CHUNK_IMAGE,  # type
            size,         # subtype: nominal size
            1,            # version
            1, 1,         # width, height
            0, 0,         # xhot, yhot
            0,            # delay
        ) + struct.pack("<I", 0x00000000)  # one transparent ARGB pixel
        offset += image_chunk_size

    return header + toc + chunks


def main() -> int:
    theme_dir = sys.argv[1] if len(sys.argv) > 1 else "/usr/share/icons/blank"
    cursors = os.path.join(theme_dir, "cursors")
    os.makedirs(cursors, exist_ok=True)

    primary = os.path.join(cursors, NAMES[0])
    with open(primary, "wb") as fh:
        fh.write(build())

    # Every other name is a symlink to the same empty cursor.
    for name in NAMES[1:]:
        link = os.path.join(cursors, name)
        if os.path.lexists(link):
            os.remove(link)
        os.symlink(NAMES[0], link)

    with open(os.path.join(theme_dir, "index.theme"), "w") as fh:
        fh.write("[Icon Theme]\nName=blank\nComment=Fully transparent cursor\n")

    print(f"wrote {theme_dir} ({len(NAMES)} cursor names)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
