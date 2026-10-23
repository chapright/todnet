---
name: Todnet — Ember
description: A dark-first Windows VPN client where fox-orange behaves like light, not paint.
colors:
  ember: "#FF7A3D"
  ember-hover: "#FF8A52"
  ember-press: "#F06A2C"
  ember-text: "#FF9A66"
  ember-ink: "#1A0C05"
  ember-soft: "rgba(255, 122, 61, 0.14)"
  ember-line: "rgba(255, 122, 61, 0.32)"
  coal: "#0E0C0B"
  ash-1: "#161311"
  ash-2: "#1E1A17"
  ash-3: "#272220"
  linen: "#F5EFEB"
  ash-text: "#B8ABA3"
  smoke: "#978A83"
  hairline: "rgba(255, 255, 255, 0.07)"
  edge: "rgba(255, 255, 255, 0.34)"
  on-accent: "#FFFFFF"
  focus-halo: "#000000"
  green-signal: "#3DDC97"
  amber-signal: "#FFC857"
  coral-signal: "#FF7B72"
  sky-downlink: "#7CC7FF"
  window-edge: "#4A423D"
typography:
  display:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "40px"
    fontWeight: 600
    lineHeight: "44px"
    letterSpacing: "-0.02em"
  headline:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "28px"
    fontWeight: 600
    lineHeight: "34px"
    letterSpacing: "-0.02em"
  title:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "20px"
    fontWeight: 600
    lineHeight: "28px"
    letterSpacing: "-0.01em"
  subtitle:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "16px"
    fontWeight: 600
    lineHeight: "24px"
  readout:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "15px"
    fontWeight: 600
    lineHeight: "22px"
  body:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: "20px"
  small:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: "18px"
  caption:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: "16px"
  micro:
    fontFamily: "Vazirmatn, Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "11px"
    fontWeight: 600
    lineHeight: "14px"
  mono:
    fontFamily: "Cascadia Mono, Consolas, ui-monospace, monospace"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: "19px"
    letterSpacing: "-0.02em"
rounded:
  xs: "6px"
  sm: "10px"
  md: "14px"
  lg: "20px"
  full: "999px"
spacing:
  s-1: "4px"
  s-2: "8px"
  s-3: "12px"
  s-4: "16px"
  s-5: "20px"
  s-6: "24px"
  s-8: "32px"
  s-10: "40px"
  s-14: "56px"
components:
  button-primary:
    backgroundColor: "{colors.ember}"
    textColor: "{colors.ember-ink}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-primary-hover:
    backgroundColor: "{colors.ember-hover}"
    textColor: "{colors.ember-ink}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-primary-press:
    backgroundColor: "{colors.ember-press}"
    textColor: "{colors.ember-ink}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-secondary:
    backgroundColor: "{colors.ash-2}"
    textColor: "{colors.linen}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.ash-text}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-danger:
    backgroundColor: "transparent"
    textColor: "{colors.coral-signal}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  icon-button:
    backgroundColor: "transparent"
    textColor: "{colors.ash-text}"
    rounded: "{rounded.sm}"
    size: "32px"
  input:
    backgroundColor: "{colors.ash-2}"
    textColor: "{colors.linen}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 12px"
    height: "38px"
  switch-track:
    backgroundColor: "{colors.ash-3}"
    rounded: "{rounded.full}"
    width: "44px"
    height: "24px"
  switch-track-on:
    backgroundColor: "{colors.ember}"
    rounded: "{rounded.full}"
    width: "44px"
    height: "24px"
  chip:
    backgroundColor: "{colors.ash-2}"
    textColor: "{colors.ash-text}"
    typography: "{typography.caption}"
    rounded: "{rounded.full}"
    padding: "0 10px"
    height: "26px"
  chip-ok:
    backgroundColor: "{colors.ash-2}"
    textColor: "{colors.green-signal}"
    typography: "{typography.caption}"
    rounded: "{rounded.full}"
    padding: "0 10px"
    height: "26px"
  nav-item:
    backgroundColor: "transparent"
    textColor: "{colors.ash-text}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 11px"
    height: "40px"
  nav-item-active:
    backgroundColor: "{colors.ember-soft}"
    textColor: "{colors.ember-text}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 11px"
    height: "40px"
  card:
    backgroundColor: "{colors.ash-1}"
    textColor: "{colors.linen}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "16px"
  modal:
    backgroundColor: "{colors.ash-1}"
    textColor: "{colors.linen}"
    typography: "{typography.body}"
    rounded: "{rounded.lg}"
    padding: "24px"
    width: "440px"
  titlebar:
    backgroundColor: "{colors.ash-1}"
    textColor: "{colors.ash-text}"
    typography: "{typography.small}"
    height: "40px"
  rail:
    backgroundColor: "{colors.ash-1}"
    textColor: "{colors.ash-text}"
    typography: "{typography.small}"
    width: "208px"
---

# Design System: Todnet — Ember

## Overview

**Creative North Star: "The Ember"**

Ember is a dark-first Windows instrument with a warm graphite body and a single
orange source that behaves like light. The default surface is coal; the panels
above it are warm ash, not neutral grey, so the whole window sits in the same
temperature as the fox-orange accent. Orange is almost absent when the tunnel is
down and is the only light in the room when it is up: a glowing rim on the orb,
a gradient arc on the allowance ring, a 2 px selected edge, one filled button.
It is never a field of paint, never a decoration, and never a large flat fill.

The character is **warm precision**: a calm, dense, native Windows control panel
that measures its contrast instead of trusting its eye, prefers a plain number to
a reassuring sentence, and gives state its own vocabulary (idle, busy, ok,
error) so the same four words mean the same thing in the title bar, the chip, the
orb and the live region. Density is high — 13–14 px body text, 32–44 px controls,
rows padded 13 px — because the person reading it is looking at a tunnel, not
browsing a catalogue. Everything decorative is quiet: there is no illustration,
no stock photography, no marketing band, no gradient mesh outside the ambient
glow behind the connect tile.

The material story is light in a dark room. Connected, the orb is graphite lit
from within — a warm core, a bright rim, a halo on the surface behind it — rather
than a solid orange disc, which at 208 px reads as a warning lamp. Green is
scoped to the chip and never becomes the headline; a green headline next to an
orange core looks like two systems disagreeing. Motion is tactile and quiet:
1 px press travel, a spring on the nav pill and the switch knob, a 360 ms spring
only when a dialog actually arrives. Nothing loops unless it is reporting work,
and nothing runs at all while the window is hidden.

**Key Characteristics:**
- Dark by default, with a light theme that is the same structure on warm paper — every component reads the same token names.
- One accent, used as emitted light: rim, arc, glow, chip text, selected border, and exactly one primary button per screen.
- Four warm surfaces in strict tonal steps; shadows only for things that float.
- A 40 px drawn title bar and a 208 px rail that behave like Windows chrome, not like a website.
- Native-density type: 11–14 px working text, four weights (400 body, 500 tooltip, 600 interface, 700 wordmark and counts), tabular numbers everywhere a value changes.
- Full right-to-left layout for Persian, written with logical properties, with Latin values isolated.
- Contrast measured against every surface a token can land on, in both themes, before the value ships.

## Colors

A warm dark palette with one orange light source, three signal colours held to
small areas, and a single cool blue reserved for the download series.

### Primary
- **Ember Orange** (`--accent`): the light. Orb rim and glyph, allowance arc start, the primary button's fill, the selected nav pill's border, the switch's on track, the brand mark's gradient. Its low-opacity siblings — **Ember Wash** (`--accent-soft`) and **Ember Line** (`--accent-line`) — carry selection and hover tint on rows, cards and chipped states.
- **Ember Hover** / **Ember Press**: the two states of any orange fill, 8–10 % lighter and darker than the base. Nothing else in the system gets a hover-only colour.
- **Ember Ink**: the text colour on orange, and the colour of the switch knob's edge. It exists because white on orange is only 2.59:1.
- **Ember Text**: orange used as a text colour, darkened in the light theme so it clears 4.5:1 on a light surface.

### Secondary
- **Signal Green** (`--ok`): connected, saved, verified. Appears as chip text, a dot, a check glyph, the copy-confirmation icon, the signal bars — never as a headline or a large fill.
- **Signal Amber** (`--warn`): connecting, quota running out, a caution in a settings row. Same small-area discipline as green.
- **Signal Coral** (`--danger`): a real failure, a destructive action, the close button's hover. In the light theme it is darkened to a brick red so it stays legible on paper.
- **Downlink Sky** (`--sky-downlink`): the cool blue of the download line, its arrow and its sparkline. It is the only cool hue in the interface, and it exists to separate download from upload by hue as well as by position.

### Neutral
- **Coal** (`--bg`): the page behind every view, and the ambient glow's backdrop.
- **Ash 1** (`--surface-1`): the rail, the title bar, cards, panels, modals — the working surface.
- **Ash 2** (`--surface-2`): inputs, chips, switches at rest, raised rows, the window-button capsule.
- **Ash 3** (`--surface-3`): tracks, hover above Ash 2, tooltip bodies.
- **Linen** (`--text`): primary text.
- **Ash Text** (`--text-2`): secondary copy, nav labels at rest, card meta.
- **Smoke** (`--text-3`): captions, labels, timestamps, placeholders — the quietest legible step.
- **Hairline** (`--border`): the 1 px line between a card and its surface, the rail's trailing edge, the title bar's underline, menu separators. Invisible until it is missing.
- **Edge** (`--border-strong`): the edge you can actually find — text fields, switches at rest, chips, segmented troughs, scrollbar thumbs, tooltips. It is deliberately visible because it is the only way a control on a dark surface reaches the 3:1 non-text bar; in the light theme it is a much darker warm brown for the same reason.
- **On Accent** (`--on-accent`): white text on an orange or coral fill — the title-bar mark, the close button's hover, avatar initials. Paired with Ember Ink where the fill is orange.
- **Focus Halo** (`--focus-halo`): solid black in the dark theme, solid white in the light theme. Never a decoration: it is the opposite-luminance half of the focus ring, so the indicator is visible on an orange button.
- **Window Edge** (`--window-border`): the 1 px client-edge hairline the interface draws itself on Windows 10, where DWM does not.

### Named Rules

**The Orange Is Light Rule.** Orange marks emitted state and selection. It may
fill a button, a 2 px border, an arc, a dot, a glow, or a few words of text. It
may never fill a card, a panel, a header, a row background at full strength, or
more than roughly a tenth of any screen. If a screen looks orange, the screen is
wrong.

**The Closed Palette Rule.** Every colour in the interface resolves to a token in
`web/css/tokens.css`. A literal hex outside that file is a bug, and the two
themes are two value sets behind one set of names, so no component knows which is
active.

**The Measured Contrast Rule.** A token ships only after its ratio is computed
against every surface it can appear on, in both themes. Text is ≥ 4.5:1; the edge
of a text field and a selected-state border are ≥ 3:1. Where the maths cannot
reach the bar with a tint, the design changes shape — that is why the selected
nav item is a bordered pill and the focus ring is paired with an opposite-luminance halo.

## Typography

**Display Font:** Vazirmatn, then Segoe UI Variable Text → Segoe UI → system-ui (with a sans-serif fallback)
**Body Font:** the same stack, at working sizes
**Mono Font:** Cascadia Mono → Consolas → ui-monospace

**Character:** One humanist UI face does everything, so the hierarchy is built
from size, weight and colour rather than from a second typeface. Vazirmatn is
bundled but limited by `unicode-range` to the Arabic script: in English it is
never downloaded, and in Persian the Latin words — TUN, HTTP/2, QUIC, IP
addresses, product names — keep the Windows shapes they have in English. The
result reads as a Windows program that happens to speak Persian, not as a
translated website.

### Hierarchy
- **Display** (600, 40/44, -0.02em): reserved. The scale exists for a future
  full-width statement; the shipped screens top out at headline.
- **Headline** (600, 28/34, -0.02em): the view title on Locations, Settings and
  Logs, and the state word on the connect tile ("Connected", "Connecting…").
  Exactly one per view.
- **Title** (600, 20/28, -0.01em): modal titles and the allowance figure.
- **Subtitle** (600, 16/24): the app wordmark, a metric pair, a card's number
  when it is a sentence rather than a measurement.
- **Readout** (600, 15/22): the exit IP and other values that are read, not
  parsed. Drops to 11 px with tighter tracking when the address is long.
- **Body** (400, 14/20): default running text and form values. Lines are held by
  their column, not by a `ch` cap — the settings column is 860 px.
- **Small** (400, 13/18): button labels, nav labels, setting labels, table
  content, helper text. This is the workhorse size of the app.
- **Caption** (400, 12/16): descriptions under settings, card labels, chips,
  log lines, error details. **Label** variants are the same size at 600 with
  0.04em tracking and uppercase ("EXIT IP", "ALLOWANCE", "CONNECTION").
- **Micro** (600, 11/14): badges, timestamps, the title bar's state line, the
  settings hint under a warning.
- **Mono** (400, 12/19): machine strings only — IPs, hosts, ports, transport
  names, bypass patterns, the log transcript.

### Named Rules

**The Four Weights Rule.** 400 carries body and running text, 600 carries the
interface (buttons, labels, titles, numbers), 700 belongs to the wordmark,
avatars, log levels and account tiers, and 500 exists only for tooltip text.
There is no light weight and no italic.

**The Tabular Rule.** Any number that can change while the window is open — a
rate, a total, a countdown, an allowance, a port, an ID — is set in tabular
numerals (`.num`). Digits must not reflow the line on every poll.

**The Mono Means Machine Rule.** The mono face is a signal that the string is
copied, pasted, compared or logged. A sentence is never mono; an IP, a hostname,
a port, a transport or a log line always is.

## Layout

The window is a 40 px title bar above a two-column application grid: a 208 px
rail and the main column. The rail holds the wordmark, four destinations, and the
account card at its foot; the main column holds a banner host and a single
scrolling view. Views are absolutely positioned inside their host so a view
change never scrolls the shell, and each view pads 24 px top / 32 px sides /
32 px bottom around a 1120 px content column.

The stack is fixed by the window, not by the document: 40 px bar, then whatever
height is left. The minimum window is 760×560; the app opens at 1000 wide, where
the labelled rail is showing. Below 860 px the rail collapses to 76 px of icons,
the home grid re-weights from 5:7 to 4:6, and labels are dropped rather than
squeezed. Two columns are kept even at the minimum, because the Exit IP card is
the proof the tunnel works and must not fall below the fold.

Home is the one composed screen. A two-column grid: the connect tile on the
left (transparent, no border — it is the page, not a card on it) and a stack of
three cards on the right (exit address, session, allowance) with a details
disclosure underneath. Until a tunnel exists, the stack is absent and the stage
centres itself at 520 px. Settings is a single 860 px column of groups; Locations
is an auto-filling grid of 260 px rows grouped by country, with a sticky search
bar and sticky group headers; Logs is a full-height monospace panel with the
toolbar above it and the newest line always reachable.

Spacing is a 4 px base: 4 / 8 / 12 / 16 / 20 / 24 / 32 / 40 / 56. The working
rhythm is 8 for inline gaps, 12–16 inside cards and rows, 24 between view
sections, 32 at the view edge. Control heights are 28 / 32 / 36 / 44, so a form
row's height is decided by its tallest control and never by its text.

### Named Rules

**The Logical Properties Rule.** Layout is written in `inline-start` /
`inline-end` / `margin-inline` / `padding-inline` throughout; the same CSS must
produce the English and the Persian screen. Physical directions live only in
`web/css/direction.css`, where the few exceptions are listed together: mirrored
chevrons, the shimmer and toast-progress direction, and the values that stay
left-to-right (email, password, IP, port, the six code boxes, the log transcript).

**The Chrome Stays Still Rule.** The title bar and the rail are the window, not
the page. They keep their own view-transition group (and no animation), while
only the content area fades or slides. A navigation that moves the chrome reads
as a page reload.

## Elevation & Depth

Depth is tonal first. Four warm surfaces step up from Coal through Ash 1, 2 and
3, and every card and dialog also carries a 1 px hairline inner top highlight
(`--inner-top`), so it looks lit from above rather than pasted on. Grids, rows,
groups and settings do not leave the surface at all — they separate with 1 px
borders and tonal steps, never with drop shadows.

Shadows are reserved for things that genuinely float above the window: cards at
rest (a soft ambient pair), overlays and toasts, and dialogs at the top of the
stack. Their colour is warm black, not neutral black, and the light theme swaps
them for warm brown at a fraction of the opacity.

### Shadow Vocabulary
- **Ambient** (`--shadow-1`): a card at rest — a 1 px contact shadow plus a wide, low-opacity pool. It should be felt, not seen.
- **Floating** (`--shadow-2`): popovers, toasts, banners, tooltips — anything detached from the page flow.
- **Modal** (`--shadow-3`): the dialog and its popover stack, the only place a genuinely deep shadow is allowed.
- **Inner top** (`--inner-top`): the 1 px top highlight on cards and dialogs in the dark theme; in the light theme it becomes a white inner line.
- **Glow**: not a shadow token but the same vocabulary — the orb's halo, the primary button's under-light, the brand mark's bloom. Glow is always orange, always soft, and always tied to the accent's state.

### Named Rules

**The Tonal-First Rule.** If a surface needs separating from its neighbour, step
the surface colour or add a border. A shadow means the element floats, so a
shadow on a row, a group, a setting or a header is a mistake, not a style choice.

**The Lit-Not-Painted Rule.** The connected orb is a graphite core lit from
inside — warm radial highlight, orange rim, halo behind it. Never swap it for a
solid orange disc: at this size that reads as an alarm.

## Shapes

Corners are soft and consistent, expressed in five steps: 6 px for small
utilitarian shapes (menu items, badges, tooltips, the flag chip, the title-bar
buttons), 10 px default for controls, inputs, buttons, chips at rest and the
window-button capsule, 14 px for cards and popovers, 20 px for dialogs and the
sign-in mark, and full roundness only for pills, switches, avatars, dots and the
allowance ring.

There are no square interactive surfaces: buttons, fields, rows, cards, chips and
overlays all carry a radius, and the only square corners in the window belong to
the window itself — the title bar, the rail and the view. No shape carries two
different radii on one silhouette. Borders are always 1 px; there is no 2 px
stroke except the selected-state line and the focus ring, and the only 2 px inset
mark inside a row is the location selection bar. Clipping follows the radius:
cards and the brand mark hide their overflow so the spotlight and gradient stay
inside the silhouette.

The form language is geometric and calm: circles for state (orb, avatar, switch
knob, status dot, signal bars), rectangles with one soft radius for work
(buttons, fields, rows), and a diagonal 145° orange gradient on the brand mark,
the sign-in mark and the recommendation badge — the three places the fox appears
or is implied. Icons are drawn on a 24 px grid at 1.75 stroke with round caps;
filled variants appear only where an item is active.

### Named Rules

**The One Radius Rule.** A shape has one radius. If a card is 14 px, its inner
row is not also 14 px — it is either flush, or a 6–10 px control sitting inside it.

**The Drawn-Mark Rule.** The brand mark, the icon sprite and the window buttons
are drawn in the same hand: 24 px grid, 1.75 stroke, round caps and joins,
`currentColor`. Default Windows glyphs next to these make the chrome look like a
browser window.

## Components

Every control is built through `web/js/components/ui.js`, so a control's name,
state and keyboard path cannot be forgotten in one view. **The feel is tactile
and quiet:** a 1 px press, a spring on the things that slide, no bounce at rest.

### Buttons
- **Shape:** 10 px radius, 36 px tall (32 small, 44 large), 14 px side padding (10 small, 22 large), 600 weight at 13 px.
- **Primary:** Ember Orange fill, Ember Ink text, a 1 px inner white top edge and a soft orange under-glow. One per screen — the action the screen exists for.
- **Secondary:** Ash 2 fill, 1 px border, Linen text; hover steps to Ash 3 and strengthens the border.
- **Ghost:** transparent, secondary text; hover adds an Ash 2 fill and brightens the label. The default for a third action and for toolbar buttons.
- **Danger:** transparent with coral text; only the hover adds a tint. A destructive confirm uses this and nothing else.
- **On (pressed/filtered):** Ember Wash fill, 1 px Ember Line border, Ember Text label. The border is the state, because a tint alone disappears on light paper.
- **States:** hover 140 ms, press 1 px translate at 80 ms, disabled at 45 % opacity, loading swaps the label for a spinner in a fixed slot so the button cannot change width.

### Icon Button
- **Shape:** 32 px square (28 small, 36 large), 10 px radius, transparent until hovered.
- **Feel:** colour dims to secondary text at rest, brightens to primary on hover, scales to 0.94 on press. Every one carries an accessible name; the copy button stays permanently visible rather than appearing on hover.

### Chips, Pills, Badges
- **Style:** 26 px tall, full roundness, 1 px border-strong edge, Ash 2 fill, 12 px semibold label. Chips carry a 7 px dot; pills carry a value (the transport, in mono, at micro size).
- **Tones:** connected is green text on a green wash with a ringed dot; connecting uses amber with a pulsing dot; failure uses coral; idle uses secondary text with no tint. The label colour, not the container, carries the meaning.
- **Badges:** smaller and squarer than chips — micro, 2×8 padding, no border — for counts and warnings inside a card head.

### Cards
- **Corner Style:** 14 px, clipped.
- **Background:** Ash 1 with a 1 px border and the ambient shadow plus inner top highlight.
- **Internal Padding:** 16 px for dashboard cards, 24 px for dialogs, 13–14 px vertical for rows.
- **Spotlight:** dashboard cards carry a 220 px radial wash of Ember Wash that follows the pointer and fades in at 220 ms. It is an accent, not a hover state — the card never lifts.
- **Labels:** card heads use the uppercase 12 px caption at 0.04em in Smoke, so the value below is the only thing competing for attention.

### Inputs / Fields
- **Style:** 38 px tall, Ash 2 fill, 1 px border-strong edge (deliberately visible: it is the only way to clear 3:1 on a dark field), 10 px radius, 13 px text, 12 px side padding.
- **Focus:** border turns Ember Orange with a 3 px Ember Wash ring and the fill lifts to Ash 1. The global focus ring (2 px Linen outline plus a 4 px opposite-luminance halo) is the fallback for everything else, and it is what makes focus visible on an orange button.
- **Error:** a coral wash block beneath the field at caption size; the field itself keeps its edge and gains no red border, because the message is the affordance.
- **Widths:** search fields cap at 420 px in Locations, 200 px in Logs, and the bypass-domain field is mono, left-to-right, 32 px tall.

### Navigation
- **Rail:** Ash 1 surface, 1 px trailing border, 40 px rows with a 20 px icon and 12 px gap, 13 px semibold labels at secondary text.
- **Active:** an Ember Wash pill with a 1 px Ember Line border slides between rows on a 360 ms spring, and the label turns Ember Text. The pill is a single shared element, so navigation reads as one object moving rather than four states changing.
- **Hover:** an Ash 2 fill and primary text on inactive rows.
- **Compact:** below 860 px the rail is 76 px, icons centre, labels are removed from layout (not truncated), and the wordmark becomes the mark alone.
- **Title bar:** 40 px, Ash 1, wordmark left and window buttons right in every language. The state line sits in the middle: a 6 px dot in the current state colour plus a micro semibold sentence. The three window buttons are a 10 px rounded Ash 2 capsule, each 32×24, with a red close hover.

### The Connect Tile (signature)
The home stage is the product. A 232 px orb: a 1 px track ring, an accent arc that reads as a status ring once connected (not a 100 % progress bar), a 150 px core that breathes at 4 s only while disconnected, and a pointer-magnetised halo on hover. State is carried by the core, not by the heading: idle shows a power glyph in secondary text; busy swaps it for a stop glyph, turns the arc amber and spins it at 1.2 s with a counter-rotating dashed ring; connected lights the core from within and draws a shield-check with a 360 ms stroke-dashoffset; a failure shakes the whole orb for 360 ms and turns the arc coral. The headline stays Linen when connected, the subline holds a fixed 18 px line so the tile never jumps, and the verified exit address is repeated under it as a pill so the proof of a working tunnel is on the stage and not only in a card.

### Overlays
- **Modal:** Ash 1, 20 px radius, 440 px (520 wide), 24 px padding, a full-screen scrim at 55 % black with an 8 px backdrop blur, and a 360 ms spring entrance. Actions stack full-width in the body; the header carries a 40 px tinted icon tile for the dialog's tone.
- **Popover:** 300 px, Ash 1 at 92 % with a 12 px blur, 14 px radius, 6 px padding, fixed positioning from an `--origin` so it scales out of the element that opened it.
- **Toast:** Ash 2 at 10×12 padding, 10 px radius, floating shadow, a 4 s progress hairline in Ember Line that pauses on hover, and one message per cause rather than a stack of duplicates.
- **Banner:** in the flow under the title bar, never over the content — as an overlay it covered exactly the chip and card the warning was about. Coral or amber wash mixed 12 % into the surface, 12 px padding, actions wrapped as one group.
- **Scrim, modal, toast and banner all animate on `--ease-spring` or `--ease-out` and all collapse to an instant change under reduced motion.**

### Motion

- **Durations:** 80 / 140 / 220 / 360 / 700 ms (`--dur-1` … `--dur-5`). Hover 140, press 80, view change 220, modal 360, orb settle and ring sweep 700.
- **Easings:** `--ease-out` for anything entering or leaving, `--ease-in-out` for loops, `--ease-spring` (a `linear()` curve) only for the nav pill, switch knob, segmented thumb, toast and modal entrance.
- **Pause:** `body.motion-paused` (window hidden or unfocused) stops every continuous loop; one-shot entrances are never started from opacity 0, so a view navigated to in the background is still readable when it returns.
- **Reduced motion:** all animation durations drop to 0.001 ms and transitions to instant; list reordering, odometers and theme reveals skip their tween entirely.
- **Theme change:** a 420 ms circular clip-path reveal from the control that was pressed, and nothing at all if view transitions are unavailable.

## Do's and Don'ts

### Do:
- **Do** put one orange action on a screen and let every other action be secondary, ghost or a link.
- **Do** let state live in the chip, the dot and the orb, and keep the headline Linen unless the state is an error.
- **Do** use the surface ladder (Coal → Ash 1 → Ash 2 → Ash 3) and 1 px borders to build hierarchy before reaching for a shadow.
- **Do** set every changing number in `.num` with tabular figures, and every machine string in mono with `dir="ltr"`.
- **Do** build controls from `ui.js` so they arrive with a role, a state, a name and a keyboard path.
- **Do** verify contrast for both themes on the actual surface the element sits on — including the light theme's darker accent-text and success values.
- **Do** write layout in logical properties and keep physical-direction exceptions in `direction.css`.
- **Do** give Persian the same layout, not a narrower one: test the minimum window in both directions, since Persian labels are longer in places and shorter in others.
- **Do** keep the title bar's order fixed — mark leading, window buttons trailing — in every language.

### Don't:
- **Don't** add a colour outside `tokens.css`, and don't add a value that only works in one theme.
- **Don't** fill a card, panel, header or row background with full-strength orange, or let a screen read as orange overall.
- **Don't** use a drop shadow on list rows, groups, settings rows or headers; those separate by tone and border.
- **Don't** turn the connected orb into a solid orange disc, a 100 % progress bar, or a green success badge — the arc fades back to a quiet status ring for a reason.
- **Don't** paint the connected headline green, or let green compete with the orange core.
- **Don't** use 500, italics, or a second typeface to create hierarchy; size, weight and colour do that work.
- **Don't** hide an affordance until hover — the copy button, the remove button and the favourite star stay visible or reachable by focus.
- **Don't** place a banner over the content it is warning about; it belongs in the flow.
- **Don't** animate continuously when nothing is happening, and never start an entrance from `opacity: 0` on a view that may mount while the window is hidden.
- **Don't** use Windows' default caption glyphs in the title bar; they are a different drawing system from the sprite.
