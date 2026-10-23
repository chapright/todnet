"""Generate the Todnet application icons as multi-size .ico files.

The mark is defined once as SVG (the same shield-fox the UI draws) and
rasterised by headless Microsoft Edge at each size Windows asks for, so every
frame is drawn from the vector at its native resolution rather than downscaled
from one big bitmap. Frames are stored PNG-compressed, which Windows has
supported since Vista.

Two files are produced:

  web/icon.ico         the warm mark, used for the window and the taskbar
  web/icon-tray-off.ico the cold mark, used in the notification area while
                        the tunnel is down

The tile is graphite, not orange: the ember mark carries the colour, so the
icon reads against both the dark taskbar and a light one. The shield outline
keeps the VPN meaning in the silhouette, while the ears and cut-out eyes make
it unmistakably Todnet.

Edge ships with Windows 10/11, so this needs no third-party dependency.

    python tools/make_icon.py
"""
import os
import struct
import subprocess
import sys
import tempfile
import time
import zlib

SIZES = [16, 24, 32, 48, 64, 128, 256]

EDGE_CANDIDATES = [
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
]

# 32x32 canvas: a graphite squircle with an ember shield-fox. Keeping the two
# in sync matters — a taskbar icon that is a different animal from the in-app
# logo reads as two different products.
#
# The path is the same mark the web UI draws (web/index.html, #i-fox): one
# filled shield with fox ears and cut-out eyes and nose. It has to stay a
# single silhouette, because at 16 pixels anything with interior detail
# collapses into noise. The shield is the VPN meaning; the ears are the fox.
SHIELD_PATH = (
    "M6.2 5.2L12.2 10.0C13.4 9.4 14.7 9.1 16 9.1C17.3 9.1 18.6 9.4 19.8 10.0"
    "L25.8 5.2L26.7 14.6C26.7 19.8 22.6 24.0 16 27.2C9.4 24.0 5.3 19.8 5.3 14.6Z"
    "M11.45 14.35A1.55 1.55 0 1 0 11.45 17.45A1.55 1.55 0 1 0 11.45 14.35Z"
    "M20.55 14.35A1.55 1.55 0 1 0 20.55 17.45A1.55 1.55 0 1 0 20.55 14.35Z"
    "M14.35 19.25L17.65 19.25L16 21.15Z"
)

# The tunnel is up: the graphite tile gets a soft ember halo and an orange
# shield-fox. The halo is deliberately behind the mark, never on top of it:
# at 16 pixels a glow over the face turns the silhouette into a smudge.
SVG_ON = """<svg viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg">
<defs>
<linearGradient id="tile" x1="0" y1="0" x2="1" y2="1">
<stop offset="0" stop-color="#302521"/><stop offset=".55" stop-color="#1C1614"/><stop offset="1" stop-color="#0F0C0B"/>
</linearGradient>
<radialGradient id="glow" cx=".5" cy=".44" r=".58">
<stop offset="0" stop-color="#FF7A3D" stop-opacity=".22"/><stop offset="1" stop-color="#FF7A3D" stop-opacity="0"/>
</radialGradient>
<linearGradient id="mark" x1="0" y1="0" x2=".35" y2="1">
<stop offset="0" stop-color="#FFA06B"/><stop offset=".55" stop-color="#FF7A3D"/><stop offset="1" stop-color="#F06A2C"/>
</linearGradient>
</defs>
<rect width="32" height="32" rx="8.2" fill="url(#tile)"/>
<rect width="32" height="32" rx="8.2" fill="url(#glow)"/>
<rect x=".55" y=".55" width="30.9" height="30.9" rx="7.7" fill="none" stroke="#FFFFFF" stroke-opacity=".10" stroke-width=".9"/>
<path fill="url(#mark)" fill-rule="evenodd" d="%(fox)s"/>
</svg>""" % {"fox": SHIELD_PATH}

# The tunnel is down: same graphite tile, no ember halo, and a solid warm-grey
# shield. A hollow outline disappears at 16 pixels, so the state is carried by
# temperature and brightness rather than by a second drawing.
SVG_OFF = """<svg viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg">
<defs><linearGradient id="tile" x1="0" y1="0" x2="1" y2="1">
<stop offset="0" stop-color="#2B2522"/><stop offset="1" stop-color="#100E0D"/>
</linearGradient></defs>
<rect width="32" height="32" rx="8.2" fill="url(#tile)"/>
<rect x=".55" y=".55" width="30.9" height="30.9" rx="7.7" fill="none" stroke="#FFFFFF" stroke-opacity=".08" stroke-width=".9"/>
<path fill="#C0B3AB" fill-rule="evenodd" d="%(fox)s"/>
</svg>""" % {"fox": SHIELD_PATH}

# The root SVG is sized in px, not vw/vh: at some window sizes a vw/vh-sized
# root box collapses to nothing and the screenshot comes back blank.
HTML = """<!doctype html><html><head><meta charset="utf-8"><style>
html,body{margin:0;padding:0;background:transparent;overflow:hidden}
svg{display:block;width:%dpx;height:%dpx}
</style></head><body>%s</body></html>"""


def find_edge():
    for path in EDGE_CANDIDATES:
        if os.path.exists(path):
            return path
    sys.exit("Microsoft Edge not found; cannot rasterise the icon")


def png_pixels(data):
    """Minimal RGBA8 PNG decoder (colour type 6, no interlace)."""
    pos = 8
    idat = b""
    w = h = 0
    while pos < len(data):
        length, ctype = struct.unpack(">I4s", data[pos:pos + 8])
        body = data[pos + 8:pos + 8 + length]
        if ctype == b"IHDR":
            w, h, depth, colour = struct.unpack(">IIBB", body[:10])
            if depth != 8 or colour != 6:
                sys.exit("unexpected PNG format: depth %d colour %d" % (depth, colour))
        elif ctype == b"IDAT":
            idat += body
        elif ctype == b"IEND":
            break
        pos += 12 + length
    raw = zlib.decompress(idat)
    stride = w * 4
    out = bytearray(w * h * 4)
    prev = bytearray(stride)
    p = 0
    for y in range(h):
        f = raw[p]
        p += 1
        line = bytearray(raw[p:p + stride])
        p += stride
        for i in range(stride):
            a = line[i - 4] if i >= 4 else 0
            b = prev[i]
            c = prev[i - 4] if i >= 4 else 0
            x = line[i]
            if f == 1:
                x += a
            elif f == 2:
                x += b
            elif f == 3:
                x += (a + b) >> 1
            elif f == 4:
                pa, pb, pc = abs(b - c), abs(a - c), abs(a + b - 2 * c)
                pr = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                x += pr
            line[i] = x & 0xFF
        out[y * stride:(y + 1) * stride] = line
        prev = line
    return w, h, out


def coverage(data):
    """Fraction of pixels that are not fully transparent."""
    w, h, px = png_pixels(data)
    opaque = sum(1 for i in range(3, len(px), 4) if px[i] > 8)
    return opaque / float(w * h)


def render(edge, page, size, out_png):
    """Render one frame, retrying if the renderer returns an empty image.

    Headless Edge occasionally screenshots before the first paint; the result
    is a valid but blank PNG, which would ship an invisible icon frame.
    """
    for attempt in range(3):
        if os.path.exists(out_png):
            os.remove(out_png)
        subprocess.run(
            [
                edge,
                "--headless=new",
                "--disable-gpu",
                "--hide-scrollbars",
                "--default-background-color=00000000",
                "--force-device-scale-factor=1",
                "--window-size=%d,%d" % (size, size),
                "--screenshot=" + out_png,
                page,
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        with open(out_png, "rb") as f:
            data = f.read()
        if data[:4] != b"\x89PNG":
            sys.exit("size %d: renderer did not produce a PNG" % size)
        # A wrong window size silently yields a wrongly-sized frame.
        w, h = struct.unpack(">II", data[16:24])
        if (w, h) != (size, size):
            sys.exit("size %d: got %dx%d" % (size, w, h))
        if coverage(data) > 0.15:
            return data
        time.sleep(0.4)
    sys.exit("size %d: renderer produced a blank frame 3 times" % size)


def build_ico(frames):
    """frames: [(size, png_bytes)] -> ICO bytes with PNG payloads."""
    count = len(frames)
    header = struct.pack("<HHH", 0, 1, count)
    entries = b""
    body = b""
    offset = 6 + 16 * count
    for size, png in frames:
        dim = 0 if size >= 256 else size  # 0 encodes 256 in the directory
        entries += struct.pack(
            "<BBBBHHII", dim, dim, 0, 0, 1, 32, len(png), offset + len(body)
        )
        body += png
    return header + entries + body


def build(edge, tmp, svg, name, target):
    frames = []
    for size in SIZES:
        # One page per size, because the SVG is sized in px.
        page = os.path.join(tmp, "%s-%d.html" % (name, size))
        with open(page, "w", encoding="utf-8") as f:
            f.write(HTML % (size, size, svg))
        uri = "file:///" + page.replace("\\", "/").replace(" ", "%20")

        png = os.path.join(tmp, "%s-%d.png" % (name, size))
        data = render(edge, uri, size, png)
        frames.append((size, data))
        print("  %s %dx%d: %d bytes" % (name, size, size, len(data)))

    ico = build_ico(frames)
    with open(target, "wb") as f:
        f.write(ico)
    print("wrote %s (%d bytes, %d frames)" % (target, len(ico), len(frames)))


def main():
    edge = find_edge()
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

    with tempfile.TemporaryDirectory() as tmp:
        build(edge, tmp, SVG_ON, "icon", os.path.join(root, "web", "icon.ico"))
        build(edge, tmp, SVG_OFF, "tray-off", os.path.join(root, "web", "icon-tray-off.ico"))


if __name__ == "__main__":
    main()
