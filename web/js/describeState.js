// The only place that turns a backend status into words. Every surface —
// orb, rail chip, tray label, aria-live — reads from here (D-05, X-07).

import { t } from './i18n.js';

export const STATES = ['disconnected', 'connecting', 'connected', 'disconnecting', 'error'];

const TONES = {
  disconnected: 'idle',
  connecting: 'busy',
  connected: 'ok',
  disconnecting: 'busy',
  error: 'error',
};

const ACTIONS = {
  disconnected: 'connect',
  connecting: 'cancel',
  connected: 'disconnect',
  disconnecting: 'none',
  error: 'retry',
};

export function describeState(status, ctx = {}) {
  const raw = status && status.state;
  const state = STATES.includes(raw) ? raw : 'disconnected';
  const signedIn = !!ctx.signedIn;
  const mode = status && status.mode === 'tun' ? 'tun' : 'proxy';
  const verifying = !!(status && status.verifying);
  const connected = state === 'connected';

  let label = t('state.' + state);
  let hint = '';
  let action = ACTIONS[state];

  if (state === 'disconnected') {
    hint = signedIn ? t('state.hint.disconnected') : t('state.hint.signedOut');
    action = signedIn ? 'connect' : 'signin';
  } else if (state === 'connecting') {
    hint = t('state.hint.connecting', { country: ctx.country || '…', elapsed: ctx.elapsed || 0 });
  } else if (state === 'disconnecting') {
    hint = t('state.hint.disconnecting');
  } else if (state === 'connected') {
    if (verifying) hint = t('state.hint.connected.verifying');
    else hint = t(mode === 'tun' ? 'state.hint.connected.tun' : 'state.hint.connected.proxy');
  } else if (state === 'error') {
    hint = '';
  }

  return {
    state,
    label,
    hint,
    chip: label,
    tray: label,
    tone: TONES[state],
    action,
    coverage: mode === 'tun' ? 'all' : 'proxy',
    mode,
  };
}

// The location the app is set to: the one it is connected to, or the one
// the settings name. The two are not the same while the tunnel is down, and
// the list of locations marks the second one.
export function preferredLocation(settings, locations) {
  if (!settings || !Array.isArray(locations) || !locations.length) return null;
  const cc = String(settings.countryCode || '').toUpperCase();
  if (!cc) return null;
  const matches = locations.filter((l) => String(l.countryCode || '').toUpperCase() === cc);
  if (!matches.length) return null;
  const city = String(settings.cityCode || '').toLowerCase();
  return matches.find((l) => String(l.cityCode || '').toLowerCase() === city) || matches[0];
}

// Maps the engine's known failures (engine.go:305-317) onto human copy.
// Unknown errors stay honest: title says the tunnel did not start and the raw
// text is available under Details.
export function describeError(message = '') {
  const m = String(message).toLowerCase();
  if (m.includes('allowance is used up') || m.includes('quota')) {
    return { title: t('error.allowance.title'), text: t('error.allowance.text'), code: 'allowance' };
  }
  if (m.includes('administrator') || m.includes('tun-needs-admin')) {
    return { title: t('error.tun.title'), text: t('error.tun.text'), code: 'tun' };
  }
  if (m.includes('http/3') || m.includes('quic') || m.includes('udp')) {
    return { title: t('error.quic.title'), text: t('error.quic.text'), code: 'quic' };
  }
  if (m.includes('timed out') || m.includes('timeout')) {
    return { title: t('error.timeout.title'), text: t('error.timeout.text'), code: 'timeout' };
  }
  if (m.includes('502') || m.includes('refused') || m.includes('bad gateway')) {
    return { title: t('error.refused.title'), text: t('error.refused.text'), code: 'refused' };
  }
  return { title: t('error.generic.title'), text: t('error.generic.text'), code: 'generic' };
}

export function formatDuration(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, '0')}m`;
}

export function formatBytes(n) {
  const v = Number(n) || 0;
  if (v < 1024) return `${v} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let x = v / 1024;
  let i = 0;
  while (x >= 1024 && i < units.length - 1) { x /= 1024; i++; }
  const digits = x >= 100 ? 0 : x >= 10 ? 1 : 2;
  return `${x.toFixed(digits)} ${units[i]}`;
}

// ltrIsolate keeps a value together and reads it left to right.
//
// A duration is two parts — "12m 05s" — and in a right-to-left sentence
// the parts would be swapped: "05s 12m". The isolate marks are what the
// Unicode bidirectional algorithm uses to say "this is one left-to-right
// run, whatever is around it".
export function ltrIsolate(text) {
  return '\u2066' + String(text) + '\u2069';
}

export function formatDate(value) {
  if (!value) return '';
  const raw = String(value).trim();
  let d;
  // A bare calendar date is not an instant. `new Date('2026-11-01')` reads it
  // as UTC midnight, which is still 31 October in the Americas - so a reset
  // on the 1st was shown on the 31st for everyone west of Greenwich. The
  // server means the day it named, so build that day in local time.
  const dateOnly = /^(\d{4})-(\d{2})-(\d{2})$/.exec(raw);
  if (dateOnly) {
    const year = Number(dateOnly[1]);
    const month = Number(dateOnly[2]) - 1;
    const day = Number(dateOnly[3]);
    d = new Date(year, month, day);
    // Date rolls 31 November into December instead of failing; a header that
    // does not name a real day has no date to show.
    if (d.getFullYear() !== year || d.getMonth() !== month || d.getDate() !== day) return '';
  } else if (/^\d+(?:\.\d+)?$/.test(raw)) {
    // Some deployments send the reset as epoch seconds instead of ISO.
    const n = Number(raw);
    d = new Date(n < 1e12 ? n * 1000 : n);
  } else {
    d = new Date(raw);
  }
  if (Number.isNaN(d.getTime())) return '';
  // The month is written in the interface's own language, but the date is
  // the Gregorian one the quota is counted in, and the digits stay Latin
  // like every other number in the interface.
  const lang = document.documentElement.lang || 'en';
  try {
    return d.toLocaleDateString(lang, {
      month: 'short', day: 'numeric', calendar: 'gregory', numberingSystem: 'latn',
    });
  } catch {
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  }
}

export function countryName(code) {
  if (!code) return '';
  // "Recommended" is a synthetic pick, not a country, so Intl has no name for
  // it and used to fall back to the raw "REC" code in the location chip.
  if (String(code).toUpperCase() === 'REC') return t('loc.recommended');
  try {
    return new Intl.DisplayNames([document.documentElement.lang || 'en'], { type: 'region' }).of(code) || code;
  } catch {
    return code;
  }
}
