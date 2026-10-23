# Todnet brand assets

Two files, one animal. The illustrated head is the mark; the glyph is what the
product actually shows when there is no room to draw fur.

| File | Grid | What it is |
|---|---|---|
| `fox-app.svg` | 32 | The current app icon: a graphite squircle with the ember shield-fox. Matches the taskbar icon and the in-app mark. |
| `fox-head.svg` | 512 | The retired illustrated fox head. Kept for reference only; not used by the app or the README. |
| `fox-mark.svg` | 32 | An earlier filled-shape mark, superseded by the shield-fox in `fox-app.svg`. |

## Why the illustration cannot be the icon

At 16 px a rendered illustration collapses: the cream ruff, the amber iris and
the black slit all land on the same three pixels, and the head reads as an
orange blob with a dark smudge. Only a filled or an outlined shape survives
that size, which is why the mark is a separate drawing rather than a scaled
copy.

`fox-head.svg` hides its own micro-detail below 110 px, using a media query
the browser evaluates against the SVG viewport:

```css
@media (max-width: 110px) { .micro { display: none } }
```

So the illustration still holds together on its own in a small slot. It is
still the wrong shape for a 16 px taskbar button.

## Rules

- **The illustrated head never carries a keyhole.** A blue padlock glyph on
  the forehead was the single loudest element in the first version, and it
  sat on the one marking that makes the animal read as a fox. The security
  idea belongs in the interface, not on the animal's face.
- **One light direction, upper left.** The head is drawn once and mirrored,
  so the geometry is symmetric but the light is not: the key light and the
  fill shadow live in root space, outside the mirror, and are therefore not
  flipped. A perfectly mirrored gradient is what made the first version look
  like a flat poster.
- **Eight colours, not eighteen.** The first version carried eighteen
  near-identical oranges and rusts. The palette here is anchored on the
  interface's own `--accent: #FF7A3D`.

## Regenerating the Windows icons

`foxy-windows/tools/make_icon.py` rasterises the shield-fox with headless
Edge at every size Windows asks for and packs the frames into
`web/icon.ico` and `web/icon-tray-off.ico`. The path is duplicated in that
script, in `fox-app.svg`, and in the `#i-fox` sprite in
`foxy-windows/web/index.html`; change them together or the taskbar and the
in-app logo become different animals.
