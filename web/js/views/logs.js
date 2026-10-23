// Logs: an append-only stream that never rebuilds itself. New records are
// appended one line at a time, follow mode is explicit, and a cleared view can
// be undone (the audit's U-12: clearing was destructive and silent).

import { t } from '../i18n.js';
import { icon } from '../icons.js';
import { h, button, iconButton, emptyState, segmented, segmentedSet } from '../components/ui.js';
import { toast } from '../components/overlay.js';

const MAX_LINES = 2000;
const POLL_MS = 1500;

export function createLogs(app) {
  // The log is a technical transcript: timestamps, levels and English
  // messages. It stays left to right whatever the interface's language is,
  // so a line is read the same way in both.
  const root = h('section', { class: 'view logs-view', id: 'view-logs', dir: 'ltr' });
  const inner = h('div', { class: 'view-inner' });

  // The transcript is LTR for everyone, but the header is prose in the
  // interface's language: let it take its direction from its own words.
  const head = h('div', { class: 'view-head', dir: 'auto' }, [
    h('div', {}, [
      h('h1', { class: 'view-title', text: t('logs.title') }),
      h('p', { class: 'view-sub', text: t('logs.sub') }),
    ]),
  ]);

  /* ---------------- toolbar ---------------- */

  let level = 'ALL';
  // DEBUG is a real level the engine emits when verbose logging is on, and
  // the filter did not offer it: the list was full of DEBUG lines the user
  // could not isolate.
  const levelSeg = segmented({
    label: t('logs.level'),
    value: level,
    options: [['ALL', t('logs.all')], ['INFO', 'Info'], ['DEBUG', 'Debug'], ['WARN', 'Warn'], ['ERROR', 'Error']],
    onChange: (v) => {
      level = v;
      rebuild();
    },
  });

  const search = h('input', {
    class: 'input',
    type: 'search',
    id: 'logSearch',
    placeholder: t('logs.search'),
    'aria-label': t('logs.search'),
    autocomplete: 'off',
  });
  search.addEventListener('input', () => rebuild());
  const searchWrap = h('div', { class: 'input-wrap logs-search' }, [
    h('span', { html: icon('search', { size: 16 }) }),
    search,
  ]);

  let follow = true;
  const followBtn = button({
    label: t('logs.follow'),
    variant: 'secondary',
    size: 'sm',
    iconName: 'down',
    onClick: () => {
      setFollow(!follow);
    },
  });
  // The button and the view have to agree about whether the log is being
  // followed. "Jump to latest" used to turn following on without saying so,
  // and the button then read as if it were off.
  const setFollow = (on) => {
    follow = on;
    followBtn.classList.toggle('is-on', follow);
    followBtn.setAttribute('aria-pressed', String(follow));
    if (follow) scrollToEnd();
    updateJump();
  };
  followBtn.setAttribute('aria-pressed', 'true');
  followBtn.classList.add('is-on');

  const copyBtn = iconButton({
    name: 'copy',
    label: t('logs.copy'),
    onClick: async () => {
      const text = visible().map((r) => `${fmtTime(r.entry.time)} ${r.entry.level} ${r.entry.message}`).join('\n');
      try {
        await navigator.clipboard.writeText(text);
        toast(t('logs.copied'), { kind: 'ok' });
      } catch {
        toast(t('logs.copyFailed'), { kind: 'error' });
      }
    },
  });

  const diagBtn = iconButton({
    name: 'doc',
    label: t('logs.diagnostics'),
    onClick: async () => {
      const ok = await app.copyDiagnostics();
      toast(ok ? t('settings.diagnosticsCopied') : t('logs.copyFailed'), { kind: ok ? 'ok' : 'error' });
    },
  });

  const clearBtn = button({
    label: t('logs.clear'),
    variant: 'ghost',
    size: 'sm',
    onClick: () => {
      if (!records.length) return;
      const stash = records;
      records = [];
      rebuild();
      toast(t('logs.cleared'), {
        kind: 'info',
        action: {
          label: t('logs.undo'),
          onClick: () => {
            // The lines that arrived after the clear are kept. The stash
            // replaced them, so a line written in between was lost from the
            // view for good: the poll's sequence number had already moved
            // past it.
            const inStash = new Set(stash.map((r) => r.seq));
            const kept = records.filter((r) => !inStash.has(r.seq));
            records = [...stash, ...kept].sort((a, b) => a.seq - b.seq);
            rebuild();
          },
        },
      });
    },
  });

  // The actions stay together. The toolbar is inside the log's own
  // left-to-right island, so it cannot mirror to make room for the longer
  // Persian labels: without the group, the last action wrapped onto a
  // second row on its own, away from the others.
  const toolbarActions = h('div', { class: 'logs-actions' }, [
    followBtn,
    copyBtn,
    diagBtn,
    clearBtn,
  ]);

  const toolbar = h('div', { class: 'logs-toolbar' }, [
    levelSeg,
    searchWrap,
    h('span', { class: 'grow' }),
    toolbarActions,
  ]);

  const panel = h('div', { class: 'logs-panel', id: 'logsPanel', role: 'log', 'aria-label': t('logs.title'), tabindex: '0' });
  const jumpBtn = button({
    label: t('logs.jump'),
    variant: 'secondary',
    size: 'sm',
    iconName: 'down',
    cls: 'log-jump',
    // Following starts again, and the button says so. It used to start
    // following silently, so the Follow button read as if it were off
    // while the view was following, and pressing it then turned following
    // off.
    onClick: () => { setFollow(true); },
  });
  jumpBtn.hidden = true;

  inner.append(head, toolbar, panel);
  root.append(inner, jumpBtn);

  /* ---------------- data ---------------- */

  let records = [];
  let seq = 0;
  let timer = 0;
  let inFlight = false;
  let active = false;
  let lineEls = [];
  let clearedAt = 0;

  function fmtTime(value) {
    if (!value) return '--:--:--';
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return '--:--:--';
    // A timestamp is a number: 24 hours, and the digits the rest of the
    // interface uses.
    const lang = document.documentElement.lang || 'en';
    try {
      return d.toLocaleTimeString(lang, { hour12: false, numberingSystem: 'latn' });
    } catch {
      return d.toLocaleTimeString(undefined, { hour12: false });
    }
  }

  function matches(rec) {
    if (level !== 'ALL' && String(rec.entry.level).toUpperCase() !== level) return false;
    const q = search.value.trim().toLowerCase();
    if (!q) return true;
    // The whole line is searched: the level and the time are on the screen
    // too, so typing "WARN" or a time has to find the lines that show
    // them. It used to look at the message only, so the level filter's
    // own words found nothing.
    const haystack = `${rec.entry.level || ''} ${rec.entry.message || ''} ${fmtTime(rec.entry.time)}`.toLowerCase();
    return haystack.includes(q);
  }

  function visible() { return records.filter(matches); }

  function makeLine(rec) {
    const lvl = String(rec.entry.level || 'INFO').toUpperCase();
    return h('div', { class: 'log-line', 'data-seq': String(rec.seq) }, [
      h('span', { class: 'log-time', text: fmtTime(rec.entry.time) }),
      h('span', { class: `log-lvl ${lvl}`, text: lvl }),
      h('span', { class: 'log-msg', text: rec.entry.message }),
    ]);
  }

  function rebuild() {
    panel.replaceChildren();
    lineEls = [];
    const list = visible();
    if (!list.length) {
      const empty = emptyState({
        iconName: records.length ? 'search' : 'doc',
        title: records.length ? t('logs.none.title') : t('logs.empty.title'),
        text: records.length ? t('logs.none.text') : t('logs.empty.text'),
      });
      empty.classList.add('log-empty');
      panel.append(empty);
      jumpBtn.hidden = true;
      return;
    }
    for (const rec of list) {
      const el = makeLine(rec);
      lineEls.push(el);
      panel.append(el);
    }
    if (follow) scrollToEnd(); else updateJump();
  }

  function appendNew(list) {
    const filtered = list.filter(matches);
    if (!filtered.length) return;
    // Drop the empty state if it is showing.
    const empty = panel.querySelector('.empty');
    if (empty) panel.replaceChildren();
    for (const rec of filtered) {
      const el = makeLine(rec);
      lineEls.push(el);
      panel.append(el);
    }
    while (lineEls.length > MAX_LINES) {
      const gone = lineEls.shift();
      gone.remove();
    }
    if (follow) scrollToEnd();
    else updateJump();
  }

  function scrollToEnd() {
    requestAnimationFrame(() => { panel.scrollTop = panel.scrollHeight; updateJump(); });
  }

  function updateJump() {
    const away = panel.scrollHeight - panel.scrollTop - panel.clientHeight > 64;
    jumpBtn.hidden = !(away && lineEls.length > 0);
  }

  panel.addEventListener('scroll', () => {
    const away = panel.scrollHeight - panel.scrollTop - panel.clientHeight > 64;
    if (away && follow) {
      follow = false;
      followBtn.classList.remove('is-on');
      followBtn.setAttribute('aria-pressed', 'false');
    }
    updateJump();
  }, { passive: true });

  async function poll() {
    if (inFlight || !active) return;
    inFlight = true;
    try {
      const data = await app.fetchLogs(seq);
      if (!data) return;
      const list = data.logs || [];
      if (list.length) {
        for (const rec of list) {
          records.push(rec);
          if (rec.seq > seq) seq = rec.seq;
        }
        while (records.length > MAX_LINES) records.shift();
        appendNew(list);
      }
      if (typeof data.seq === 'number' && data.seq > seq) seq = data.seq;
    } catch {
      /* the control plane may be restarting; the next poll will recover */
    } finally {
      inFlight = false;
    }
  }

  function start() {
    if (active) return;
    active = true;
    poll();
    timer = window.setInterval(poll, POLL_MS);
  }

  function stop() {
    active = false;
    if (timer) window.clearInterval(timer);
    timer = 0;
  }

  return {
    root,
    onEnter: () => {
      start();
      segmentedSet(levelSeg, level);
      // The view is display:none until it is mounted, so the first append
      // cannot measure scrollHeight. Re-anchor once it is visible.
      requestAnimationFrame(() => { if (follow) scrollToEnd(); });
    },
    onLeave: stop,
    focusSearch: () => { search.focus(); search.select(); },
    start,
    stop,
  };
}
