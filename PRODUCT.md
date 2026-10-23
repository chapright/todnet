# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Two audiences, treated as peers:

- **Privacy-minded Windows users** who already have, or will create, a free Firefox account and want a simple tunnel without a subscription.
- **Persian-speaking users**, including people on restrictive or censored networks, who need the interface in Persian with correct right-to-left layout. English and Persian are the only supported locales.

Both use Windows 10 (1809+) or Windows 11 on x64, usually on a laptop or desktop where they also work, browse and stream. They are not expected to understand VPN internals.

## Product Purpose

Todnet is an unofficial Windows client for the free 50 GB/month VPN allowance that Mozilla includes with a Firefox account. It exists so a Windows user can use that allowance in a native-feeling app: sign in with the Firefox account they already have, pick a location, connect, and keep the tunnel running from the tray.

Success means the tunnel is up when the user expects it, they can see proof that it is carrying their traffic, and it fails in a way they can understand and fix.

## Positioning

Two things a neighbouring VPN client could not truthfully copy:

1. **Mozilla's free allowance through the user's own Firefox account** — no subscription, no plan tiers, no upsell. The multi-account switcher turns the 50 GB-per-account limit into something a user can work with: add another Firefox account, see each account's remaining allowance, and switch.
2. **A single self-contained Windows executable with its own interface** — no installer, no runtime, no admin rights for the default mode; a drawn title bar, a working tray, and real Windows behaviour instead of a website in a stock frame.

The copy is deliberately honest about what the product is and is not: it is unofficial, it is not Mozilla, and it reports what actually happened (exit IP, logs, errors) instead of optimising the message.

## Operating Context

- The user signs in with a Firefox account email and password; Mozilla may ask for a verification code, or the user may finish verification in a browser.
- Connection modes: **system proxy** (default, covers apps that use Windows proxy settings) and **TUN** (captures every app, requires administrator rights, carries TCP only).
- Transport: **Auto** uses HTTP/2 over TCP on port 2499 and retries with HTTP/3 (QUIC) when the edge refuses; HTTP/2 or HTTP/3 can be forced.
- Locations come from Mozilla's own edge list; *Auto-select* uses Mozilla's anycast edge.
- The window can be closed to the tray while the tunnel stays up; *Launch at startup* is opt-in.
- Settings include bypass domains, direct fallback, and language.
- The app keeps its state in `%APPDATA%\Todnet\`; deleting that folder resets it.

## Capabilities and Constraints

- Windows 10 1809+ and Windows 11, x64, WebView2 (preinstalled on 11; on 10 the app falls back to the default browser when missing).
- One ~10 MB `Todnet.exe`; no installer, no runtime, no admin for the default proxy mode. TUN needs elevation because Windows requires it for a virtual adapter.
- Free allowance: 50 GB/month per Firefox account, as provided by Mozilla; the app respects the account's measured allowance and shows it.
- English and Persian only; Persian is RTL, set in bundled Vazirmatn, with technical terms left in Latin script.
- Upstream limits: Mozilla publishes MASQUE edges only, so there are no P2P/streaming/obfuscated server categories; the edge accepts TCP, and TUN carries TCP only.
- Open decisions (do not invent answers): whether the allowance is per account or per person; whether "Recommended" should measure edge latency or be renamed to describe the anycast edge; whether the welcome screen should be reopenable.

## Brand Commitments

- Name **Todnet**, fox mark; the mark is part of the interface and the window chrome.
- **Unofficial and independent**: never imply affiliation with, endorsement by, or support from Mozilla. The disclaimer stays.
- **Free, no subscription**: no paid plans, upgrade prompts, or upsell surfaces.
- **Windows 10/11 only**: do not design for macOS, Linux, iOS or Android.
- **English + Persian only**: no additional locales and no machine-translated copy.
- Voice: precise, candid, technical when it needs to be; explains the real mechanism and the real failure rather than marketing around it.

## Evidence on Hand

- `README.md` — the authoritative description of behaviour, transports, files, limits and disclaimer.
- `REDESIGN_NOTES.md` — the "Ember" design system: tokens, measured contrast, motion and layout decisions.
- `docs/*.png` — real screenshots of every view and state (light/dark, English/Persian, min/wide).
- `docs/DESIGN_AUDIT_REPORT.md` — prior audit with open questions and known gaps.
- The running app and its source in this repository (Go host + `web/` frontend).

Absent, and must not be invented: testimonials, customer logos, press coverage, independent speed or security benchmarks, or a paid plan.

## Product Principles

1. **Show the truth.** The exit IP, the allowance, the log and the error text are the product's proof; never replace them with reassurance the app cannot back up.
2. **One window, native manners.** The app should behave like a well-made Windows program — title bar, tray, keyboard, DPI, snapping — not like a page in a frame.
3. **Free means free.** Every surface must work without a subscription and without implying one.
4. **Both languages are first-class.** Persian is not a translation layer bolted on; RTL layout, fonts and mirrored controls are part of the design.
5. **A tunnel the user can leave running.** Closing the window, losing the edge, or running out of allowance must be handled explicitly, not silently.

## Accessibility & Inclusion

- Controls are real, keyboard-focusable elements (buttons and switches with ARIA state), not clickable divs.
- Text contrast is measured against every surface it can appear on (≥ 4.5:1); light and dark themes are both first-class.
- `prefers-reduced-motion`, `prefers-contrast: more` and `forced-colors: active` are honoured.
- Full RTL layout for Persian; numbers, addresses and technical terms keep their own direction.
