# Todnet

<p align="center"><img src="assets/fox/fox-app.svg" width="160" alt="Todnet"></p>

[English](README.md) · [فارسی](README.fa.md)

A native Windows client for the free VPN data included with a Firefox account — a Windows port of the Android client [Vauth/FoxyVPN](https://github.com/Vauth/FoxyVPN).

Todnet is unofficial and independent. It is not affiliated with, endorsed by, or supported by Mozilla.

## Requirements

- Windows 10 1809+ or Windows 11, 64-bit
- A free [Firefox account](https://accounts.firefox.com)
- Outbound TCP port 2499

WebView2 is built into Windows 11. On Windows 10, the app falls back to your default browser if WebView2 is missing.

## Install

Download from the [latest release](../../releases/latest):

- **Installer** — `Todnet-<version>-setup.exe`
- **Portable** — `Todnet-<version>-windows-x64.zip` (extract and run `Todnet.exe`)

The binaries are not code-signed yet. If Windows SmartScreen appears, choose **More info → Run anyway**.

## Quick start

1. Run Todnet.
2. Sign in with your Firefox account.
3. Pick a location, or leave **Auto-select** on.
4. Press the power button.

## Features

- **System proxy** mode (default, no admin rights) and **TUN** mode (every app, requires administrator rights)
- HTTP and SOCKS5 proxies on `127.0.0.1`
- 33 countries plus a recommended fastest-edge option, with live latency
- Multiple Firefox accounts with per-account allowance, switchable
- Tray icon, optional launch at startup, and live logs
- English and Persian, with full right-to-left layout

TUN mode carries TCP only, including DNS-over-HTTPS. UDP is refused so apps fall back to TCP.

## Data

Settings, accounts, and logs are stored in `%APPDATA%\Todnet\`. Delete that folder to reset the app completely.

## Release assets

Each release includes the installer, the portable zip, and `SHA256SUMS.txt` for verifying the downloads.

## Disclaimer

Unofficial client. Use it with your own Firefox account, under the free 50 GB/month allowance Mozilla provides. Respect Mozilla's terms of service.
