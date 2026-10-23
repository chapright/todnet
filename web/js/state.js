// Single source of truth for the UI. The backend is authoritative; this
// module owns the poll loop, the diffing signal, and the "service lost"
// state that the old UI silently ignored.

import { endpoints } from './api.js';

let current = null;
let previous = null;
let settings = null;
let offline = false;
let inFlight = false;
let timer = 0;
const listeners = new Set();

export function subscribe(fn) {
  listeners.add(fn);
  // A listener hears about the first answer even when there was none: the
  // service can be dead before the window subscribes, and that is exactly
  // the case the offline banner exists for.
  fn(current, previous);
  return () => listeners.delete(fn);
}

export function getState() { return current; }
export function getSettings() { return settings; }
export function isOffline() { return offline; }
export function status() { return current ? current.status : { state: 'disconnected' }; }
export function user() { return (current && current.user) || { signedIn: false }; }

function emit() {
  for (const fn of listeners) {
    try { fn(current, previous); } catch (err) { console.error('[state] listener failed', err); }
  }
}

function apply(data) {
  previous = current;
  current = data;
  settings = data.settings || settings;
  if (offline) offline = false;
  emit();
}

export async function refresh() {
  if (inFlight) return current;
  inFlight = true;
  try {
    const data = await endpoints.state();
    apply(data);
    return data;
  } catch (err) {
    // A dead control plane must not look like a healthy tunnel (audit S-03).
    if (!offline) {
      offline = true;
      emit();
    }
    return null;
  } finally {
    inFlight = false;
  }
}

export function setLocations(locations) {
  if (!current) return;
  current = { ...current, locations };
  emit();
}

export function setSettings(next) {
  settings = next;
  if (current) current = { ...current, settings: next };
  emit();
}

// setStatus moves the state on its own, for the moment between the user
// pressing Connect and the server answering. The poll is a second away, and
// until it came the screen still said "Disconnected" and the button still
// said "Connect", so a second press did nothing at all.
export function setStatus(patch) {
  if (!current) return;
  previous = current;
  current = { ...current, status: { ...current.status, ...patch } };
  emit();
}

export function startPolling(interval = 1000) {
  stopPolling();
  const tick = () => {
    if (document.hidden) return;
    refresh();
  };
  timer = window.setInterval(tick, interval);
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) refresh();
  });
}

export function stopPolling() {
  if (timer) window.clearInterval(timer);
  timer = 0;
}
