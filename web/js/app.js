// Application shell: routing, the rail, the primary action, and every call
// the views make into the control plane. This is the only file that knows
// both the DOM and the API.

import { endpoints, ApiError } from './api.js';
import * as store from './state.js';
import { t, setLocale, getLocale, locale, isRTL } from './i18n.js';
import { startOffset } from './dir.js';
import { icon } from './icons.js';
import { h, button, iconButton, avatarColor, flagChip } from './components/ui.js';
import { openModal, openPopover, toast, showBanner, hideBanner, hasBanner, refreshBanner, clearToasts } from './components/overlay.js';
import { viewTransition, themeReveal, announce, watchVisibility, enableSpotlight } from './motion.js';
import { describeState, describeError, formatBytes, countryName, ltrIsolate, preferredLocation } from './describeState.js';
import { createHome } from './views/home.js';
import { createLocations } from './views/locations.js';
import { createSettings } from './views/settings.js';
import { createLogs } from './views/logs.js';
import { openLogin as openLoginView } from './views/auth.js';

// The native shell opens the page with ?native=1. The marker lets the
// Windows 10 hairline and the WebView2-only behaviour stay out of a plain
// browser preview (and out of the documentation screenshots).
const shellParams = new URLSearchParams(location.search);
const nativeShell = shellParams.has('native') ||
  !!(window.chrome && window.chrome.webview);
if (nativeShell) {
  document.body.classList.add('native-shell');
  if (shellParams.has('rounded')) document.body.classList.add('has-rounded-window');
}

/* ---------------- preferences that stay on the device ---------------- */

const THEME_KEY = 'todnet.theme';
const LANG_KEY = 'todnet.lang';
const FAV_KEY = 'todnet.favorites';
const RECENT_KEY = 'todnet.recent';

// Before the rename these four preferences lived under `foxy.*`. Copy them
// once so the name change does not reset the theme, the language or the
// saved locations.
for (const [key, legacy] of [
  [THEME_KEY, 'foxy.theme'],
  [LANG_KEY, 'foxy.lang'],
  [FAV_KEY, 'foxy.favorites'],
  [RECENT_KEY, 'foxy.recent'],
]) {
  try {
    if (localStorage.getItem(key) === null) {
      const value = localStorage.getItem(legacy);
      if (value !== null) localStorage.setItem(key, value);
    }
  } catch { /* storage unavailable */ }
}

function readList(key) {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((x) => typeof x === 'string') : [];
  } catch { return []; }
}

function writeList(key, list) {
  try { localStorage.setItem(key, JSON.stringify(list.slice(0, 40))); } catch { /* storage full */ }
}

let favorites = readList(FAV_KEY);
let recent = readList(RECENT_KEY);

/* ---------------- theme ---------------- */

const systemLight = window.matchMedia('(prefers-color-scheme: light)');
let themeMode = 'system';
let lastPointer = { x: innerWidth / 2, y: innerHeight / 2 };

function resolvedTheme(mode) {
  if (mode === 'light' || mode === 'dark') return mode;
  return systemLight.matches ? 'light' : 'dark';
}

function paintTheme(mode) {
  themeMode = mode;
  const resolved = resolvedTheme(mode);
  document.documentElement.dataset.theme = resolved;
  document.body.dataset.theme = resolved;
  try { localStorage.setItem(THEME_KEY, mode); } catch { /* private mode */ }
}

systemLight.addEventListener('change', () => {
  if (themeMode === 'system') paintTheme('system');
});

document.addEventListener('pointerdown', (e) => {
  lastPointer = { x: e.clientX, y: e.clientY };
}, { passive: true, capture: true });

/* ---------------- shell elements ---------------- */

const els = {
  titlebar: document.getElementById('titlebar'),
  titlebarState: document.getElementById('titlebarState'),
  titlebarDot: document.getElementById('titlebarDot'),
  titlebarStateText: document.getElementById('titlebarStateText'),
  tbMin: document.getElementById('tbMin'),
  tbMax: document.getElementById('tbMax'),
  tbClose: document.getElementById('tbClose'),
  nav: document.getElementById('nav'),
  navPill: document.getElementById('navPill'),
  viewHost: document.getElementById('viewHost'),
  accountBtn: document.getElementById('accountBtn'),
  accountAvatar: document.getElementById('accountAvatar'),
  accountMail: document.getElementById('accountMail'),
  accountSub: document.getElementById('accountSub'),
  brandMark: document.getElementById('brandMark'),
};

const ORDER = ['home', 'locations', 'settings', 'logs'];

// The title of a view is its name in the rail. It is looked up when the
// view is shown rather than kept in a table, because the table would go
// stale the moment the language changed.
function viewTitle(name) { return t('nav.' + name); }

// Everything but the sign-in is behind an account.
function isSignedIn() {
  const st = store.getState();
  return !!(st && st.user && st.user.signedIn);
}

let views = {};
let activeView = '';
let busy = false;
// connecting is separate from busy: the connect request is held open for
// the whole handshake, so the "busy" lock would have swallowed the Cancel
// click for the entire attempt.
let connecting = false;
let cancelling = false;
// cancelRequested is set by the Cancel action and is only cleared when a
// new attempt starts. It cannot live in a finally block: the connect
// request and the cancel request finish in an unpredictable order, and a
// cancelled attempt must never raise an error banner.
let cancelRequested = false;
let lastStatus = null;
let closeLocationPopover = null;

/* ---------------- router ---------------- */

function viewFromHash() {
  const raw = (location.hash || '').replace(/^#\/?/, '').split('?')[0];
  return ORDER.includes(raw) ? raw : 'home';
}

function placePill(name, instant = false) {
  const item = els.nav.querySelector(`.nav-item[data-view="${name}"]`);
  if (!item) return;
  if (instant) els.navPill.classList.add('no-anim');
  els.navPill.style.top = `${item.offsetTop}px`;
  els.navPill.style.height = `${item.offsetHeight}px`;
  // The pill marks the selected item, so it is placed from the inline
  // start: the right-hand side in a right-to-left layout.
  els.navPill.style.insetInlineStart = `${startOffset(item, els.nav)}px`;
  els.navPill.style.width = `${item.offsetWidth}px`;
  els.navPill.style.opacity = '1';
  if (instant) requestAnimationFrame(() => els.navPill.classList.remove('no-anim'));
  for (const node of els.nav.querySelectorAll('.nav-item')) {
    const on = node.dataset.view === name;
    node.classList.toggle('is-active', on);
    node.setAttribute('aria-current', on ? 'page' : 'false');
    const use = node.querySelector('use');
    if (use) {
      const base = use.getAttribute('href').replace('-fill', '');
      use.setAttribute('href', on ? `${base}-fill` : base);
    }
  }
}

function ensureView(name) {
  if (views[name]) return views[name];
  const factory = { home: createHome, locations: createLocations, settings: createSettings, logs: createLogs }[name];
  const view = factory(app);
  view.root.hidden = true;
  els.viewHost.append(view.root);
  views[name] = view;
  return view;
}

function focusView(view) {
  const heading = view.root.querySelector('h1');
  if (!heading) return;
  heading.setAttribute('tabindex', '-1');
  heading.focus({ preventScroll: true });
}

function go(name, { instant = false, focus = false } = {}) {
  if (!ORDER.includes(name)) name = 'home';
  // The sign-in comes first: there is nothing else to look at without an
  // account, so any other route falls back to the home screen.
  if (!isSignedIn() && name !== 'home') {
    name = 'home';
    if (location.hash !== '#/home') history.replaceState(null, '', '#/home');
  }
  const prevIndex = ORDER.indexOf(activeView);
  const nextIndex = ORDER.indexOf(name);
  const direction = prevIndex === -1 || nextIndex >= prevIndex ? 'forward' : 'back';

  const apply = (opts) => {
    const view = ensureView(name);
    if (activeView && views[activeView] && activeView !== name) {
      const old = views[activeView];
      if (old.onLeave) old.onLeave();
      old.root.hidden = true;
      old.root.classList.remove('is-entering-forward', 'is-entering-back');
    }
    view.root.hidden = false;
    view.root.scrollTop = view.root.scrollTop || 0;
    // The entrance animation is the fallback for a browser that cannot do
    // a view transition. With one running, the transition itself moves
    // the view, and doing both made the new view jump once the
    // transition finished.
    if (!instant && !(opts && opts.viewTransition)) {
      view.root.classList.remove('is-entering-forward', 'is-entering-back');
      void view.root.offsetWidth;
      view.root.classList.add(`is-entering-${direction}`);
    }
    activeView = name;
    placePill(name, instant);
    document.title = `${viewTitle(name)} · Todnet`;
    if (view.onEnter) view.onEnter();
    const state = store.getState();
    if (state && typeof view.update === 'function') view.update(augmented(state));
    // A route change is a context change: send keyboard and screen-reader
    // users to the new view's heading instead of leaving focus on the rail.
    if (focus) focusView(view);
  };

  if (instant) apply();
  else viewTransition(apply, { direction });
}

function navigate(name) {
  const target = `#/${name}`;
  if (location.hash === target) { go(name, { focus: true }); return; }
  location.hash = target;
}

window.addEventListener('hashchange', () => {
  const name = viewFromHash();
  if (!isSignedIn() && name !== 'home') {
    history.replaceState(null, '', '#/home');
    go('home', { focus: true });
    return;
  }
  go(name, { focus: true });
});

/* ---------------- state plumbing ---------------- */

function augmented(st) {
  if (!st) return st;
  return {
    ...st,
    settings: { ...(st.settings || {}), favorites, recent },
  };
}

function onState(state, prev) {
  // The offline banner goes first: it is about the state we do not have,
  // so it has to be read before the one that decides whether the state
  // can be used. A dead service answered nothing at all, and the early
  // return below meant the banner never appeared — the window looked
  // exactly like a healthy disconnected one.
  updateOffline(state);
  if (!state) return;
  // The language can change outside the settings view — another window of
  // the app, or a hand-edited settings file — and the whole interface has
  // to follow it, including the direction the native title bar draws its
  // buttons on.
  const lang = state.settings && state.settings.language;
  if (lang && lang !== getLocale()) applyLocale(lang);
  const view = views[activeView];
  if (view && typeof view.update === 'function') view.update(augmented(state));
  // The view owns the parts of the screen that move on their own: the
  // elapsed counter and the advice for an attempt that is taking too long.
  // Nobody called it, so the counter read "0s" for the whole attempt and
  // the advice never came.
  if (view && typeof view.tick === 'function') view.tick();
  updateRail(state, prev);
  checkConnectionOutcome(state, prev);
}

/* ---------------- access ---------------- */

// The sign-in comes first. Without an account there is nothing else to
// look at, so the rail goes away and every route falls back to the home
// screen, which shows the sign-in.
function applyAccess(signedIn) {
  if (document.body.classList.contains('is-signed-out') === !signedIn) return;
  document.body.classList.toggle('is-signed-out', !signedIn);
  if (!signedIn && activeView && activeView !== 'home') {
    history.replaceState(null, '', '#/home');
    go('home', { focus: true });
  }
}

// The account card's second line. It says what the account is — a
// subscriber, or how much allowance is left — and nothing else.
function accountSubtitle(state) {
  const accounts = state.accounts || [];
  const account = accounts.find((a) => a.active);
  if (!account) return '';
  if (account.subscribed) return t('account.subscribed');
  if (account.quotaMax) return t('home.allow.left', { left: ltrIsolate(formatBytes(account.quotaLeft || 0)) });
  return '';
}

function updateRail(state, prev) {
  const st = state.status || {};
  const signedIn = !!(state.user && state.user.signedIn);
  const desc = describeState(st, { signedIn });

  // The title bar carries the state, so the window says what it is doing
  // even when it is in the background.
  if (els.titlebarStateText) {
    const bits = [desc.label];
    if (st.state === 'connected') {
      if (st.location) {
        // The server sends Mozilla's own English name; the interface shows
        // the name in its own language.
        const cc = st.location.countryCode;
        bits.push(countryName(cc) || st.location.countryName || cc || '');
      }
      if (st.mode === 'tun') bits.push('TUN');
    }
    els.titlebarStateText.textContent = bits.filter(Boolean).join(' · ');
  }
  if (els.titlebarDot) els.titlebarDot.dataset.tone = desc.tone;
  document.body.classList.toggle('has-rounded-window', !!state.rounded);
  updateWindowButton(!!state.maximized);
  applyAccess(signedIn);

  if (signedIn) {
    const email = state.user.email || '';
    const sub = accountSubtitle(state);
    els.accountMail.textContent = email;
    els.accountSub.textContent = sub;
    const initial = (email.split('@')[0] || '?').slice(0, 1);
    els.accountAvatar.textContent = initial;
    els.accountAvatar.style.background = avatarColor(email);
    els.accountAvatar.classList.add('has-letter');
    // The avatar's letter is decoration. Without this the button's name
    // began "a alex@example.com ...", and at the compact width the email is
    // invisible, so the tooltip carries it.
    els.accountBtn.setAttribute('aria-label', `${t('account.title')}: ${email}${sub ? ' — ' + sub : ''}`);
    els.accountBtn.title = sub ? `${email} · ${sub}` : email;
  } else {
    els.accountMail.textContent = t('home.signIn');
    els.accountSub.textContent = t('account.title');
    els.accountAvatar.style.background = '';
    els.accountAvatar.classList.remove('has-letter');
    els.accountAvatar.innerHTML = `<svg aria-hidden="true"><use href="#i-user"/></svg>`;
    els.accountBtn.setAttribute('aria-label', t('account.title'));
    els.accountBtn.title = t('account.title');
  }

  const prevState = prev && prev.status ? prev.status.state : null;
  if (st.state && st.state !== prevState) {
    if (st.state === 'connected') announce(`${t('state.connected')}. ${desc.hint}`);
    else if (st.state === 'disconnected' && prevState) announce(t('state.disconnected'));
    else if (st.state === 'connecting') announce(t('state.connecting'));
    else if (st.state === 'error') announce(t('state.error'));
  }
}

// The control plane can stop answering while the window is open. The flag
// existed but nothing read it, so a dead service looked exactly like a
// healthy tunnel.
let offlineShown = false;
function updateOffline(state) {
  const off = store.isOffline();
  if (off === offlineShown) return;
  offlineShown = off;
  if (off) {
    showBanner({
      kind: 'warn',
      title: t('common.offline'),
      text: t('common.offlineText'),
    });
    announce(t('common.offline'));
  } else if (hasBanner()) {
    hideBanner();
  }
}

function checkConnectionOutcome(state, prev) {
  const before = prev && prev.status ? prev.status.state : null;
  const now = state.status ? state.status.state : null;
  if (now === 'error' && before !== 'error') {
    const info = describeError(state.status.error || '');
    showFailureBanner(info.code, state.status.error || '');
  } else if (now === 'connected' && before !== 'connected') {
    hideBanner();
  }
}

// A failure banner, in the language of the interface now.
//
// The words are chosen when the failure is raised, so the banner keeps
// them; a change of language left it in the old one. The banner therefore
// remembers the failure and is shown again when the language changes.
function showFailureBanner(code, details, kind = 'error') {
  const spec = () => ({
    kind,
    title: t(`error.${code}.title`),
    text: t(`error.${code}.text`),
    details,
    actions: recoveryActions(code),
  });
  showBanner({ ...spec(), render: spec });
}

// Each failure gets the actions that can actually resolve it. Offering "Try
// again" after the monthly allowance ran out is a dead end (audit X-05).
function recoveryActions(code) {
  const retry = { label: t('error.actions.retry'), primary: true, onClick: () => { hideBanner(); connect(); } };
  switch (code) {
    case 'allowance':
      return [
        { label: t('account.title'), primary: true, onClick: () => { hideBanner(); openAccountMenu(); } },
        { label: t('settings.title'), onClick: () => { hideBanner(); navigate('settings'); } },
      ];
    case 'tun':
      return [
        { label: t('dialog.tun.restart'), primary: true, onClick: () => { hideBanner(); showTunDialog(); } },
        {
          label: t('dialog.tun.switch'),
          onClick: async () => {
            hideBanner();
            try { await saveSetting({ tunMode: false }); } catch { toast(t('settings.saveFailed'), { kind: 'error' }); }
          },
        },
      ];
    case 'quic':
      return [
        retry,
        { label: t('settings.transport'), onClick: () => { hideBanner(); navigate('settings'); } },
      ];
    default:
      return [
        retry,
        { label: t('error.actions.location'), onClick: () => { hideBanner(); navigate('locations'); } },
      ];
  }
}

// The TUN refusal is the one failure that used to produce no feedback at
// all: the server answers 403 with a code, and the client dropped every
// coded error on the floor. It gets a real modal, because a banner on a
// screen that repaints once a second flickers.
function showTunDialog() {
  openModal({
    title: t('dialog.tun.title'),
    sub: t('dialog.tun.text'),
    iconName: 'alert',
    tone: 'warn',
    actions: [
      { label: t('dialog.tun.restart'), variant: 'primary', onClick: () => { elevate(); } },
      {
        label: t('dialog.tun.switch'),
        variant: 'secondary',
        onClick: async () => {
          try { await saveSetting({ tunMode: false }); } catch { toast(t('settings.saveFailed'), { kind: 'error' }); }
        },
      },
      { label: t('dialog.later'), variant: 'ghost', onClick: (close) => close() },
    ],
  });
  announce(t('error.tun.title'));
}

/* ---------------- window buttons ---------------- */

// The maximise button has to be able to give the window back, so the
// glyph and the action follow the window's real state.
let windowMaximized = false;

function updateWindowButton(maximized) {
  const changed = windowMaximized !== maximized;
  windowMaximized = maximized;
  // A maximized window has no visible outer corner or edge: hiding the
  // hairline there is what Windows itself does.
  document.body.classList.toggle('is-maximized', !!maximized);
  const btn = els.tbMax;
  if (!btn) return;
  if (changed) {
    const use = btn.querySelector('use');
    if (use) use.setAttribute('href', maximized ? '#i-tb-restore' : '#i-tb-max');
  }
  // The name is re-read every time, because a language change has to
  // retranslate it even when the window has not moved.
  const label = maximized ? t('window.restore') : t('window.maximize');
  if (btn.getAttribute('aria-label') !== label) {
    btn.setAttribute('aria-label', label);
    btn.title = label;
  }
}

/* ---------------- language ---------------- */

// The shell's own words live in the markup with a data-i18n attribute, so
// they can be re-read when the language changes.
function translateStatic(root = document) {
  for (const node of root.querySelectorAll('[data-i18n]')) {
    node.textContent = t(node.dataset.i18n);
  }
  for (const node of root.querySelectorAll('[data-i18n-title]')) {
    const label = t(node.dataset.i18nTitle);
    const sc = node.dataset.shortcut;
    node.title = sc ? `${label} (Ctrl+${sc})` : label;
  }
  for (const node of root.querySelectorAll('[data-i18n-label]')) {
    node.setAttribute('aria-label', t(node.dataset.i18nLabel));
  }
}

// applyLocale switches the interface into another language.
//
// The views build their static copy when they are created, so they are
// thrown away and made again: a label that was written once in English
// cannot be re-read in Persian.
function applyLocale(code) {
  if (!setLocale(code)) return;
  const info = locale();
  document.documentElement.lang = code;
  document.documentElement.dir = info.dir;
  try { localStorage.setItem(LANG_KEY, code); } catch { /* private mode */ }

  translateStatic();
  pushTrayNotice();
  // A panel that is open would keep the old language, and it is anchored
  // to a node that is about to be replaced.
  if (closeLocationPopover) {
    closeLocationPopover();
    closeLocationPopover = null;
  }
  const names = Object.keys(views);
  for (const name of names) {
    const view = views[name];
    if (view.onLeave) view.onLeave();
    view.root.remove();
    delete views[name];
  }
  if (names.length) {
    const state = store.getState();
    go(activeView || 'home', { instant: true });
    if (state) updateRail(state, null);
  }
  // The banner and the toasts are chrome, not part of a view, so they are
  // not rebuilt above. The banner is shown again in the new language, and
  // the toasts go: a toast in the old language is laid out in the new
  // direction, which is worse than no toast.
  refreshBanner();
  clearToasts();
  updateWindowButton(windowMaximized);
}

/* ---------------- actions ---------------- */

function connect() {
  if (busy || connecting) return;
  busy = true;
  connecting = true;
  cancelling = false;
  cancelRequested = false;
  hideBanner();
  // The screen says "Connecting" straight away. It used to wait for the next
  // poll, which is a second away, and until then the heading still said
  // "Disconnected" and the button still said "Connect", so a second press
  // did nothing at all.
  store.setStatus({ state: 'connecting' });
  const run = async () => {
    try {
      await endpoints.connect({});
    } catch (err) {
      await store.refresh();
      if (cancelRequested) {
        // The attempt was abandoned on purpose, so the failure it ends with
        // is not something to report.
      } else if (err instanceof ApiError && err.code === 'tun-needs-admin') {
        // The one coded error the API produces. It used to be swallowed.
        showTunDialog();
        showFailureBanner('tun', '', 'warn');
        announce(t('error.tun.title'));
      } else if (!(err instanceof ApiError) || !err.code) {
        const info = describeError(err && err.message ? err.message : '');
        // The actions belong to the banner. Without them the banner that
        // this failure raised replaced the one the state poll had already
        // shown with its actions, so the screen said "Try again — Choose
        // another location" and offered neither.
        showFailureBanner(info.code, (err && err.message) || '');
        announce(info.title);
      }
    } finally {
      busy = false;
      connecting = false;
      cancelling = false;
      await store.refresh();
    }
  };
  run();
}

// Cancel abandons the attempt. It does not go through the "busy" lock:
// that lock is held for the whole connect request, so routing the cancel
// through it meant the button did nothing at all.
//
// The attempt may not be this window's: "Connect on launch" and the tray
// start one behind the interface, and the button says "Cancel" whenever
// the state says "connecting". So the state decides, not the page's own
// flag, which is only set for an attempt this window started.
async function cancel() {
  if (!connecting && store.status().state !== 'connecting') return;
  cancelling = true;
  cancelRequested = true;
  try {
    await endpoints.cancel();
    announce(t('state.hint.cancelled'));
    // The screen said nothing about the attempt being abandoned. The words
    // went to the live region, and the next poll wrote "Disconnected" over
    // them before anyone could read them.
    toast(t('state.hint.cancelled'), { kind: 'info' });
  } catch (err) {
    toast(err.message || t('settings.saveFailed'), { kind: 'error' });
  } finally {
    cancelling = false;
    await store.refresh();
  }
}

async function disconnect() {
  if (busy) return;
  busy = true;
  try {
    await endpoints.disconnect();
  } catch (err) {
    toast(err.message || t('settings.saveFailed'), { kind: 'error' });
  } finally {
    busy = false;
    await store.refresh();
  }
}

function onPrimary() {
  const st = store.getState();
  if (!st) return;
  const signedIn = !!(st.user && st.user.signedIn);
  const status = st.status || {};
  if (!signedIn) { openLogin(); return; }
  switch (status.state) {
    case 'connecting':
      cancel();
      break;
    case 'connected':
      disconnect();
      break;
    case 'disconnecting':
      break;
    case 'error':
    case 'disconnected':
    default:
      connect();
  }
}

let locationsError = '';

async function refreshLocations(force = true, { silent = false } = {}) {
  const view = views.locations;
  if (view && view.setRefreshBusy) view.setRefreshBusy(true);
  try {
    const list = await endpoints.locations(force);
    store.setLocations(list);
    locationsError = '';
    if (view && view.setError) view.setError(false);
    return list;
  } catch (err) {
    locationsError = err.message || t('loc.error.text');
    if (view && view.setError) view.setError(true);
    // A background fetch that fails is not worth a toast: the list is
    // fetched before the interface is on screen, and the Locations view
    // has an error state of its own for it. A manual one is, and it says
    // the same words as the page: the server's own error is English.
    if (!silent) toast(t('loc.error.title'), { kind: 'error' });
    return null;
  } finally {
    if (view && view.setRefreshBusy) view.setRefreshBusy(false);
  }
}

async function saveSetting(patch) {
  const current = (store.getState() && store.getState().settings) || {};
  const next = { ...current, ...patch };
  delete next.favorites;
  delete next.recent;
  const saved = await endpoints.settings(next);
  store.setSettings(saved);
  if (saved.theme) paintTheme(saved.theme);
  if (saved.language) {
    const changed = applyLocale(saved.language);
    if (changed) {
      // The language change throws the settings view away and makes it
      // again, so the chip that confirms the save belongs to the new one.
      const view = views.settings;
      if (view && view.flashSaved) view.flashSaved('language');
    }
  }
  return saved;
}

function setTheme(mode) {
  const origin = lastPointer;
  const apply = () => {
    paintTheme(mode);
    const view = views.settings;
    if (view) view.update(augmented(store.getState() || {}));
  };
  themeReveal(origin.x, origin.y, apply);
  saveSetting({ theme: mode }).catch(() => {
    toast(t('settings.saveFailed'), { kind: 'error' });
  });
}

// setLanguage switches the interface into another language. The setting
// is the authority, so the whole interface is rebuilt around it.
async function setLanguage(code) {
  try {
    await saveSetting({ language: code });
  } catch (err) {
    toast(t('settings.saveFailed'), { kind: 'error' });
  }
}

// The balloon Windows draws when the window is closed to the notification
// area is not part of the page, so the words have to be handed over.
function pushTrayNotice() {
  endpoints.trayNotice(t('common.trayHint'), t('common.trayHintText')).catch(() => {});
}

async function elevate() {
  try {
    await endpoints.elevate();
    toast(t('settings.tunNotice.action'), { kind: 'info', timeout: 6000 });
  } catch (err) {
    toast(err.message || t('settings.saveFailed'), { kind: 'error' });
  }
}

async function copyDiagnostics() {
  try {
    const text = await endpoints.diagnostics();
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

// A link that leaves the app. The interface is a local page with no
// outbound navigation of its own, so this goes through the control plane
// and opens in the user's real browser.
async function openURL(url) {
  try {
    await endpoints.openURL(url);
  } catch (err) {
    // The message is the interface's own, so it is in the language the user
    // reads. The server's words are English.
    toast(t('login.openFailed'), { kind: 'error' });
  }
}

function pushRecent(loc) {
  const key = String(loc.countryCode || '').toUpperCase();
  if (!key) return;
  recent = [key, ...recent.filter((c) => c !== key)].slice(0, 8);
  writeList(RECENT_KEY, recent);
}

function toggleFavorite(code) {
  const cc = String(code || '').toUpperCase();
  if (!cc) return;
  favorites = favorites.includes(cc) ? favorites.filter((c) => c !== cc) : [cc, ...favorites];
  writeList(FAV_KEY, favorites);
  const st = store.getState();
  if (st) store.setSettings({ ...(st.settings || {}) });
}

async function selectLocation(loc) {
  if (!loc) return;
  const cc = String(loc.countryCode || '').toUpperCase();
  const city = String(loc.cityCode || '');
  pushRecent(loc);
  const wasConnected = store.status().state === 'connected';
  try {
    await saveSetting({ countryCode: cc, cityCode: city });
  } catch {
    toast(t('settings.saveFailed'), { kind: 'error' });
    return;
  }
  const name = countryName(cc) || loc.countryName;
  if (wasConnected) {
    // No toast: the stage already says "Contacting <country> · 3s", and a
    // toast repeating it with a zero elapsed time read as noise.
    await disconnect();
    connect();
  } else {
    toast(name, { kind: 'ok', timeout: 1800 });
    navigate('home');
  }
}

/* ---------------- location picker ---------------- */

function openLocationPicker(anchor) {
  const st = store.getState() || {};
  const list = st.locations || [];
  const body = h('div', { class: 'popover-body' });
  const bodyInner = h('div', { class: 'acct-list' });

  // Favorites and recents used to be one undifferentiated list, so the
  // user could not tell which was which.
  const quick = [];
  const addQuick = (cc, tag) => {
    if (quick.some((q) => q.loc.countryCode === cc)) return;
    const loc = list.find((l) => String(l.countryCode).toUpperCase() === cc);
    if (loc) quick.push({ loc, tag });
  };
  for (const cc of favorites.slice(0, 3)) addQuick(String(cc).toUpperCase(), 'fav');
  for (const cc of recent.slice(0, 3)) addQuick(String(cc).toUpperCase(), 'recent');

  // The country the app is set to. It used to be only the live edge, which
  // is nothing while the tunnel is down, so no entry in the panel ever
  // carried the tick that says "this is the one you are on".
  const chosen = (st.status && st.status.location) || preferredLocation(st.settings, st.locations);
  const selectedCc = String((chosen && chosen.countryCode) || '').toUpperCase();

  if (!quick.length) {
    bodyInner.append(h('div', { class: 'acct-meta', style: 'padding:10px 12px', text: t('home.location.fastest') }));
  } else {
    for (const { loc, tag } of quick) {
      const row = h('button', { class: 'menu-item', type: 'button' }, [
        flagChip(loc.countryCode),
        h('span', { class: 'truncate', text: countryName(loc.countryCode) || loc.countryName }),
        h('span', { class: 'acct-tier', text: tag === 'fav' ? t('loc.favTag') : t('loc.recentTag') }),
        String(loc.countryCode).toUpperCase() === selectedCc
          ? h('span', { class: 'loc-end', html: '<svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true"><path class="loc-check" d="M5 12.5l4.4 4.4L19 7.2" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>' })
          : null,
      ]);
      row.addEventListener('click', () => { if (closeLocationPopover) closeLocationPopover(); selectLocation(loc); });
      bodyInner.append(row);
    }
  }
  body.append(bodyInner);

  // "Fastest available" is a choice, not a label.
  const rec = list.find((l) => String(l.countryCode).toUpperCase() === 'REC');
  if (rec) {
    const recRow = h('button', { class: 'menu-item', type: 'button' }, [
      h('span', { html: icon('bolt', { size: 16 }) }),
      h('span', { class: 'grow', text: t('loc.recommended') }),
      h('span', { class: 'acct-meta', text: t('home.location.fastest') }),
    ]);
    recRow.addEventListener('click', () => { if (closeLocationPopover) closeLocationPopover(); selectLocation(rec); });
    body.append(h('div', { class: 'popover-sep' }), recRow);
  }

  const all = h('button', { class: 'menu-item', type: 'button' }, [
    h('span', { html: icon('pin', { size: 16 }) }),
    h('span', { class: 'grow', text: t('loc.title') }),
    h('span', { class: 'chev', html: icon('chev-right', { size: 14 }) }),
  ]);
  all.addEventListener('click', () => { if (closeLocationPopover) closeLocationPopover(); navigate('locations'); });
  body.append(h('div', { class: 'popover-sep' }), all);

  closeLocationPopover = openPopover({
    anchor,
    content: body,
    width: 288,
    // The panel opens beside the chip rather than above it. Above, it
    // landed on top of the Connect button. In a right-to-left layout the
    // free side is the other one.
    placement: isRTL() ? 'left-start' : 'right-start',
    label: t('home.location.change'),
    onClose: () => { closeLocationPopover = null; },
  });
}

/* ---------------- account menu ---------------- */

/* The account list is a dialog rather than a flyout.
 *
 * The rail is 208px wide and the menu needs about 300px, so a flyout
 * anchored to the account card had nowhere to go but over the Home
 * screen's primary button. A dialog is never clipped, never covers a
 * control, and gives the per-account allowance room to be read. */
function openAccountMenu() {
  const st = store.getState() || {};
  const accounts = st.accounts || [];
  const signedIn = !!(st.user && st.user.signedIn);
  const body = h('div', { class: 'acct-list acct-list-modal' });

  // The list is drawn again whenever the accounts change. It used to be
  // drawn once, so switching left the tick on the account that had been
  // active before the switch.
  const renderAccounts = () => {
    const list = (store.getState() || {}).accounts || [];
    body.replaceChildren();
    if (!list.length) {
      body.append(h('p', { class: 'form-hint', text: t('settings.signedOutDesc') }));
      return;
    }
    for (const acc of list) {
      const row = h('div', { class: 'acct-row' + (acc.active ? ' is-active' : '') });
      const pick = h('button', { class: 'acct-pick', type: 'button', role: 'menuitemradio', 'aria-checked': String(!!acc.active) }, [
        h('span', {
          class: 'acct-avatar',
          text: (acc.email || '?').slice(0, 1),
          style: `background:${avatarColor(acc.email)}`,
          'aria-hidden': 'true',
        }),
        h('span', { class: 'acct-text' }, [
          h('span', { class: 'acct-mail truncate' }, [
            h('span', { text: acc.email }),
            acc.subscribed ? h('span', { class: 'acct-tier', text: ' · ' + t('account.subscribed') }) : null,
          ]),
          h('span', {
            class: 'acct-meta' + (acc.exhausted ? ' is-empty' : ''),
            text: acc.exhausted
              ? t('account.outOfData')
              : acc.quotaMax ? t('account.quota', { left: ltrIsolate(formatBytes(acc.quotaLeft || 0)), total: ltrIsolate(formatBytes(acc.quotaMax)) }) : '—',
          }),
        ]),
        acc.active ? h('span', { class: 'loc-end', html: '<svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true"><path class="loc-check" d="M5 12.5l4.4 4.4L19 7.2" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>' }) : null,
      ]);
      pick.addEventListener('click', async () => {
        if (acc.active) { close(); return; }
        try {
          await endpoints.switchAccount(acc.email);
          await store.refresh();
          renderAccounts();
          toast(t('account.switched', { email: acc.email }), { kind: 'ok' });
        } catch (err) {
          toast(err.message || t('settings.saveFailed'), { kind: 'error' });
        }
      });
      row.append(pick);
      if (list.length > 1) {
        const rm = iconButton({
          name: 'trash',
          label: t('account.remove', { email: acc.email }),
          size: 'sm',
          cls: 'acct-remove',
          onClick: () => { close(); confirmRemoveAccount(acc); },
        });
        row.append(rm);
      }
      body.append(row);
    }
  };

  renderAccounts();

  const actions = [];
  actions.push({ label: t('account.add'), variant: 'primary', onClick: (close) => { close(); openLogin({ add: true }); } });
  if (signedIn) {
    actions.push({ label: t('account.signOut'), variant: 'danger', onClick: (close) => { close(); confirmSignOut(); } });
  }

  const close = openModal({
    title: t('account.title'),
    sub: accounts.length ? t('account.saved', { n: accounts.length }) : '',
    iconName: 'user',
    body,
    actions,
    // The card says whether its dialog is open. It always said "closed",
    // even with the dialog on screen.
    onClose: () => els.accountBtn.setAttribute('aria-expanded', 'false'),
  });
  els.accountBtn.setAttribute('aria-expanded', 'true');
  return close;
}

function confirmRemoveAccount(acc) {
  openModal({
    title: t('account.removeConfirm', { email: acc.email }),
    sub: t('account.removeConfirmText'),
    iconName: 'alert',
    tone: 'danger',
    row: true,
    actions: [
      { label: t('common.cancel'), variant: 'ghost', onClick: (close) => close() },
      {
        label: t('common.remove'),
        variant: 'danger',
        primary: true,
        onClick: async (close) => {
          close();
          try {
            await endpoints.removeAccount(acc.email);
            await store.refresh();
            toast(t('account.removed', { email: acc.email }), { kind: 'ok' });
          } catch (err) {
            toast(err.message || t('settings.saveFailed'), { kind: 'error' });
          }
        },
      },
    ],
  });
}

function confirmSignOut() {
  const st = store.getState() || {};
  const email = (st.user && st.user.email) || '';
  openModal({
    title: t('account.signOutConfirm', { email }),
    sub: t('account.signOutConfirmText'),
    iconName: 'alert',
    tone: 'danger',
    row: true,
    actions: [
      { label: t('common.cancel'), variant: 'ghost', onClick: (close) => close() },
      {
        label: t('account.signOut'),
        variant: 'danger',
        primary: true,
        onClick: async (close) => {
          close();
          try {
            await endpoints.logout();
            await store.refresh();
          } catch (err) {
            toast(err.message || t('settings.saveFailed'), { kind: 'error' });
          }
        },
      },
    ],
  });
}

/* ---------------- login ---------------- */

function openLogin({ add = false } = {}) {
  return openLoginView({ add, onOpenURL: openURL, endpoints });
}

/* ---------------- keyboard ---------------- */

document.addEventListener('keydown', (e) => {
  const mod = e.ctrlKey || e.metaKey;
  if (!mod) return;
  // Ctrl+1..4 are global view jumps, but they must not steal a keystroke
  // from a field the user is editing (a query would otherwise vanish with
  // the view). Ctrl+F still belongs to the app's search.
  const target = e.target;
  const editing = target instanceof Element && (
    target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' ||
    target.tagName === 'SELECT' || target.isContentEditable
  );
  if (e.key >= '1' && e.key <= '4') {
    if (editing) return;
    e.preventDefault();
    navigate(ORDER[Number(e.key) - 1]);
    return;
  }
  if (e.key.toLowerCase() === 'f') {
    e.preventDefault();
    if (activeView === 'locations' && views.locations) views.locations.focusSearch();
    else if (activeView === 'logs' && views.logs) views.logs.focusSearch();
    else {
      // The key opens the list of locations, and the cursor goes into its
      // search box. The view is built a frame later — the route change is
      // animated — so the focus waits for it. Otherwise the user still has
      // to click the field before they can type.
      navigate('locations');
      let tries = 0;
      const focus = () => {
        if (views.locations) {
          views.locations.focusSearch();
          return;
        }
        if (tries++ > 120) return;
        requestAnimationFrame(focus);
      };
      requestAnimationFrame(focus);
    }
  }
});

/* ---------------- app context for views ---------------- */

const app = {
  state: () => augmented(store.getState()),
  settings: () => store.getSettings(),
  onPrimary,
  connect,
  disconnect,
  cancel,
  isCancelling: () => cancelling,
  openLocationPicker,
  refreshLocations,
  locationsError: () => locationsError,
  toggleFavorite,
  selectLocation,
  saveSetting,
  setTheme,
  setLanguage,
  elevate,
  copyDiagnostics,
  openLogin,
  openAccountMenu,
  openURL,
  confirmSignOut,
  fetchLogs: (since) => endpoints.logs(since),
  navigate,
};

/* ---------------- boot ---------------- */

async function boot() {
  // Theme first so the first frame is already correct.
  const storedTheme = (() => {
    try { return localStorage.getItem(THEME_KEY) || 'system'; } catch { return 'system'; }
  })();
  paintTheme(storedTheme);

  // The language, and the writing direction that comes with it. The
  // cached copy is what the first frame was painted with; the server
  // has the authority and corrects it a moment later.
  const storedLang = (() => {
    try { return localStorage.getItem(LANG_KEY) || 'en'; } catch { return 'en'; }
  })();
  setLocale(storedLang);
  document.documentElement.lang = getLocale();
  document.documentElement.dir = locale().dir;
  translateStatic();

  // The window's own title bar. The native caption is gone, so the
  // minimise, maximise and close buttons are the interface's.
  //
  // The maximise button asks for a toggle, and the reply carries the state
  // the window is in after the action. Asking for "toggle" means the button
  // is right even if the interface's own copy of the state is a second old,
  // which is what happens when the bar was double-clicked a moment before.
  const tb = (el, action) => {
    if (!el) return;
    el.addEventListener('click', () => {
      const what = typeof action === 'function' ? action() : action;
      endpoints.windowAction(what).then((res) => {
        if (what !== 'toggle-maximize' && what !== 'maximize' && what !== 'restore') return;
        if (!res || typeof res.maximized !== 'boolean') return;
        updateWindowButton(res.maximized);
        // A maximized window has no edge to size from, so an edge cursor
        // left over from before the click is no longer true.
        showResizeEdge('');
      }).catch((err) => {
        toast(err.message || t('settings.saveFailed'), { kind: 'error' });
      });
    });
  };
  tb(els.tbMin, 'minimize');
  tb(els.tbMax, 'toggle-maximize');
  tb(els.tbClose, 'close');

  els.nav.addEventListener('click', (e) => {
    const item = e.target.closest('.nav-item');
    if (!item) return;
    navigate(item.dataset.view);
  });
  els.accountBtn.addEventListener('click', openAccountMenu);
  els.brandMark.addEventListener('click', () => navigate('home'));

  // The window has no system frame: the WebView covers the whole client
  // area, so the window manager's own resize border is unreachable. The
  // page knows where the pointer is, so it reports the edges itself, and
  // the cursor says what a press there will do.
  //
  // Keep resizeBorder in step with win.TitleBarResizeBorder.
  const resizeBorder = 6;
  const resizeCursors = {
    left: 'ew-resize',
    right: 'ew-resize',
    top: 'ns-resize',
    bottom: 'ns-resize',
    'top-left': 'nwse-resize',
    'bottom-right': 'nwse-resize',
    'top-right': 'nesw-resize',
    'bottom-left': 'nesw-resize',
  };

  function resizeEdgeAt(e) {
    // A maximized window has no edge to size from.
    if (windowMaximized) return '';
    const left = e.clientX < resizeBorder;
    const right = e.clientX >= window.innerWidth - resizeBorder;
    const top = e.clientY < resizeBorder;
    const bottom = e.clientY >= window.innerHeight - resizeBorder;
    if (top && left) return 'top-left';
    if (top && right) return 'top-right';
    if (bottom && left) return 'bottom-left';
    if (bottom && right) return 'bottom-right';
    if (left) return 'left';
    if (right) return 'right';
    if (top) return 'top';
    if (bottom) return 'bottom';
    return '';
  }

  let resizeEdge = '';
  function showResizeEdge(edge) {
    if (edge === resizeEdge) return;
    resizeEdge = edge;
    const root = document.documentElement;
    if (edge) {
      root.dataset.resizeEdge = edge;
      root.style.setProperty('--resize-cursor', resizeCursors[edge]);
    } else {
      delete root.dataset.resizeEdge;
    }
  }

  window.addEventListener('mousemove', (e) => {
    showResizeEdge(resizeEdgeAt(e));
  }, { passive: true });
  document.addEventListener('mouseleave', () => showResizeEdge(''));

  // Capture, so the press never reaches the title bar's drag handler or a
  // control under the edge.
  window.addEventListener('mousedown', (e) => {
    if (e.button !== 0) return;
    const edge = resizeEdgeAt(e);
    if (!edge) return;
    e.stopPropagation();
    endpoints.windowAction('resize', { edge }).catch(() => {});
  }, true);

  // The title bar drags the window.
  //
  // The bar is drawn by the WebView, so a press on it is a press on the
  // page: it never becomes the non-client click that would start the
  // system's move loop. The bar asks for the loop itself instead, and the
  // native side starts it.
  //
  // A press only becomes a drag after the pointer has actually moved. The
  // old code started the loop on mousedown, so the small wobble between the
  // two clicks of a double click was a drag of its own and moved the window
  // before the user meant to move it. Waiting for the threshold means a
  // plain click never moves anything, and the second click still reaches the
  // page as a double click: the bar can then maximize the way a caption does.
  const dragThreshold = 5; // CSS pixels, about the system's double-click slop
  let dragOrigin = null;

  const isDragHandle = (target) =>
    !target.closest('.tb-btn') && !target.closest('.brand-mark');

  els.titlebar.addEventListener('mousedown', (e) => {
    if (e.button !== 0 || !isDragHandle(e.target)) return;
    dragOrigin = { x: e.screenX, y: e.screenY };
  });

  window.addEventListener('mousemove', (e) => {
    if (!dragOrigin) return;
    if ((e.buttons & 1) === 0) {
      dragOrigin = null;
      return;
    }
    const dx = Math.abs(e.screenX - dragOrigin.x);
    const dy = Math.abs(e.screenY - dragOrigin.y);
    if (dx < dragThreshold && dy < dragThreshold) return;
    dragOrigin = null;
    endpoints.windowAction('drag').catch(() => {});
  }, { passive: true });

  window.addEventListener('mouseup', () => { dragOrigin = null; });
  window.addEventListener('blur', () => { dragOrigin = null; });

  els.titlebar.addEventListener('dblclick', (e) => {
    if (!isDragHandle(e.target)) return;
    // The native side asks the window manager, so the toggle is right even
    // when the interface's copy of the maximized state is a second old. The
    // reply says what the window is doing now, so the glyph does not wait
    // for the next poll either.
    endpoints.windowAction('toggle-maximize').then((res) => {
      if (!res || typeof res.maximized !== 'boolean') return;
      updateWindowButton(res.maximized);
      showResizeEdge('');
    }).catch(() => {});
  });

  window.addEventListener('resize', () => placePill(activeView, true));

  enableSpotlight(document);
  watchVisibility(() => { /* CSS handles the pause via body.motion-paused */ });

  const initial = await store.refresh();
  // The server's theme is the authority; the cached one is only what the
  // first frame was painted with.
  if (initial && initial.settings && initial.settings.theme) paintTheme(initial.settings.theme);
  // Subscribing calls the handler straight away, and that is where the
  // server's language is applied: the sign-in screen is built from it, so
  // it has to be in place before the first state is shown.
  store.subscribe(onState);
  store.startPolling(1000);
  pushTrayNotice();

  go(viewFromHash(), { instant: true });

  // The native window is created hidden and the host shows it when this
  // request arrives, so the first thing the user sees is the rendered
  // interface instead of a blank frame that is then painted over. Fire and
  // forget: the host has its own timeout, and the call is harmless when the
  // interface is running in a browser.
  endpoints.ready().catch(() => {});

  // Locations are public, so fetch them in the background even when signed
  // out. A failure here is silent: the Locations view shows the error.
  if (!initial || !initial.locations || !initial.locations.length) {
    refreshLocations(false, { silent: true }).catch(() => {});
  }
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', boot, { once: true });
} else {
  boot();
}
