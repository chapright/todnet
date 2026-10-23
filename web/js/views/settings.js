// Settings: grouped rows, every control a real control, every save confirmed
// in place. The audit's K-01 (non-focusable divs) and K-03 (silent saves)
// cannot recur because rows are built only from ui.js primitives.

import { t } from '../i18n.js';
import { icon } from '../icons.js';
import { h, button, iconButton, switchControl, segmented, segmentedSet } from '../components/ui.js';
import { toast, openModal } from '../components/overlay.js';

let rowSeq = 0;

export function createSettings(app) {
  const root = h('section', { class: 'view', id: 'view-settings' });
  const inner = h('div', { class: 'view-inner' });
  const col = h('div', { class: 'settings-col' });

  const head = h('div', { class: 'view-head' }, [
    h('div', {}, [
      h('h1', { class: 'view-title', text: t('settings.title') }),
      h('p', { class: 'view-sub', text: t('settings.sub') }),
    ]),
  ]);

  // One polite live region for the whole view: "Saved" is announced once,
  // not per control, and the visible chip is the per-row confirmation.
  const live = h('div', { class: 'sr-only', role: 'status', 'aria-live': 'polite' });

  const controls = {};
  let lastSig = '';
  // null, not '': an empty list has the same signature as "not rendered
  // yet", and the first empty state must still show its empty message.
  let lastBypassSig = null;
  let bypassBusy = false;

  /* ---------------- row helpers ---------------- */

  function row({ key, label, desc, control, disabled = false, warn = false, note = null }) {
    const labelEl = h('div', { class: 'setting-label' }, [h('span', { text: label })]);
    if (warn) labelEl.append(h('span', { class: 'warn-ico', html: icon('alert', { size: 14 }), 'aria-hidden': 'true' }));
    const saved = h('span', { class: 'save-ok', 'aria-hidden': 'true', html: icon('check', { size: 13 }) });
    saved.append(h('span', { text: t('settings.saved') }));
    labelEl.append(saved);

    // Associate the visible explanation with the control, not just the
    // label: a screen reader should hear "Verify exit IP ... Checks ...".
    const descId = desc ? `setting-desc-${++rowSeq}` : null;
    const main = h('div', { class: 'setting-main' }, [
      labelEl,
      desc ? h('div', { class: 'setting-desc', id: descId, text: desc }) : null,
    ]);
    if (descId && control && !control.hasAttribute('aria-describedby')) {
      control.setAttribute('aria-describedby', descId);
    }
    const ctl = h('div', { class: 'setting-control' }, [control]);
    const el = h('div', { class: 'setting' + (disabled ? ' is-disabled' : '') }, [main, ctl]);
    if (note) el.append(note);
    // A row can carry a live hint under its description: the port rows
    // need to say that a change only takes effect on the next connection.
    const hint = h('div', { class: 'setting-hint', hidden: true });
    main.append(hint);
    controls[key] = { row: el, saved, savedTimer: 0, hint };
    return el;
  }

  function setHint(key, text) {
    const c = controls[key];
    if (!c || !c.hint) return;
    if (!text) {
      c.hint.hidden = true;
      c.hint.textContent = '';
      return;
    }
    c.hint.hidden = false;
    c.hint.textContent = text;
  }
  function flashSaved(key) {
    const c = controls[key];
    if (c) {
      c.saved.classList.add('show');
      clearTimeout(c.savedTimer);
      c.savedTimer = setTimeout(() => c.saved.classList.remove('show'), 1600);
    }
    // Bypass rows have no per-row "Saved" chip, but the action still has to
    // be announced and confirmed.
    live.textContent = t('settings.saved');
  }

  async function save(patch, key, revert) {
    try {
      await app.saveSetting(patch);
      // The chip belongs to the row that changed. It may be more than one:
      // the two port rows are saved together.
      for (const one of [].concat(key)) flashSaved(one);
      return true;
    } catch (err) {
      if (revert) revert();
      toast(t('settings.saveFailed'), { kind: 'error' });
      live.textContent = t('settings.saveFailed');
      return false;
    }
  }

  /* ---------------- connection ---------------- */

  const transport = segmented({
    label: t('settings.transport'),
    value: 'auto',
    options: [['auto', t('settings.auto')], ['h2', t('settings.h2')], ['h3', t('settings.h3')]],
    onChange: (v) => save({ transport: v }, 'transport'),
  });

  const verifyExit = switchControl({
    checked: true,
    label: t('settings.verifyExit'),
    onChange: (v) => save({ verifyExit: v }, 'verifyExit'),
  });

  const autoConnect = switchControl({
    checked: false,
    label: t('settings.autoConnect'),
    onChange: (v) => save({ autoConnect: v }, 'autoConnect'),
  });

  const directFallback = switchControl({
    checked: false,
    label: t('settings.directFallback'),
    onChange: (v) => save({ directFallback: v }, 'directFallback'),
  });

  const tunMode = segmented({
    label: t('settings.tunMode'),
    value: 'proxy',
    options: [['proxy', t('settings.proxy')], ['tun', t('settings.tun')]],
    onChange: async (v) => {
      const ok = await save({ tunMode: v === 'tun' }, 'tunMode', () => segmentedSet(tunMode, v === 'tun' ? 'proxy' : 'tun'));
      if (ok) renderNotices();
    },
  });

  const tunNotice = h('div', { class: 'setting-notice', hidden: true }, [
    h('div', {}, [
      h('div', { class: 'label', text: t('settings.tunNotice.title') }),
      h('div', { class: 'desc', text: t('settings.tunNotice.text') }),
    ]),
  ]);
  const elevateBtn = button({
    label: t('settings.tunNotice.action'),
    variant: 'primary',
    size: 'sm',
    onClick: async () => {
      elevateBtn.disabled = true;
      try { await app.elevate(); } catch { elevateBtn.disabled = false; }
    },
  });
  tunNotice.append(elevateBtn);

  const connectionGroup = h('div', { class: 'card group' }, [
    h('div', { class: 'group-head' }, [
      h('span', { class: 'group-icon', html: icon('wifi', { size: 15 }) }),
      h('span', { class: 'group-title', text: t('settings.group.connection') }),
    ]),
    h('div', { class: 'group-body' }, [
      row({ key: 'transport', label: t('settings.transport'), desc: t('settings.transportDesc'), control: transport }),
      row({ key: 'tunMode', label: t('settings.tunMode'), desc: t('settings.tunModeDesc'), control: tunMode }),
      tunNotice,
      row({ key: 'verifyExit', label: t('settings.verifyExit'), desc: t('settings.verifyExitDesc'), control: verifyExit }),
      row({ key: 'autoConnect', label: t('settings.autoConnect'), desc: t('settings.autoConnectDesc'), control: autoConnect }),
      row({
        key: 'directFallback',
        label: t('settings.directFallback'),
        desc: t('settings.directFallbackDesc'),
        control: directFallback,
        warn: true,
      }),
    ]),
  ]);

  /* ---------------- bypass domains ---------------- */

  // A domain pattern is a hostname, or a wildcard suffix. The port is
  // not part of a name, and a pattern with a path or a scheme is a
  // mistake rather than a host.
  function isBypassPattern(value) {
    const p = String(value || '').trim().toLowerCase();
    if (!p) return false;
    const host = p.startsWith('*.') ? p.slice(2) : p;
    if (!host) return false;
    return /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/.test(host);
  }

  const bypassList = h('div', { class: 'bypass-list' });
  const bypassEmpty = h('div', { class: 'bypass-empty', text: t('settings.bypass.empty') });
  const bypassInput = h('input', {
    class: 'input',
    type: 'text',
    id: 'bypassInput',
    placeholder: t('settings.bypass.placeholder'),
    'aria-label': t('settings.bypass.input'),
    autocomplete: 'off',
    spellcheck: 'false',
  });
  const bypassHint = h('div', { class: 'setting-hint', role: 'alert', hidden: true });

  function focusBypassAfter(index) {
    const rows = bypassList.querySelectorAll('.bypass-row');
    const next = rows[Math.min(index, rows.length - 1)];
    const btn = next && next.querySelector('.bypass-remove');
    (btn || bypassInput).focus();
  }

  async function removeBypass(domain) {
    if (bypassBusy) return;
    const current = (app.settings() || {}).bypassDomains || [];
    const index = current.indexOf(domain);
    if (index < 0) return;
    const next = current.filter((d) => d !== domain);
    bypassBusy = true;
    try {
      const ok = await save({ bypassDomains: next }, 'bypass');
      if (ok) {
        // A validation message from an earlier add is no longer relevant.
        bypassHint.hidden = true;
        bypassHint.textContent = '';
        // The list is rebuilt by update(); keep the keyboard in the group
        // instead of dropping focus onto <body> with the removed row.
        focusBypassAfter(index);
      }
    } finally {
      bypassBusy = false;
    }
  }

  function renderBypass(list) {
    bypassList.replaceChildren();
    if (!list.length) {
      bypassList.append(bypassEmpty);
      return;
    }
    for (const domain of list) {
      const row = h('div', { class: 'bypass-row' }, [
        h('span', { class: 'bypass-domain', text: domain, title: domain }),
        iconButton({
          name: 'x',
          label: t('settings.bypass.remove', { domain }),
          size: 'sm',
          cls: 'bypass-remove',
          onClick: () => removeBypass(domain),
        }),
      ]);
      bypassList.append(row);
    }
  }

  async function addBypass() {
    if (bypassBusy) return;
    const value = bypassInput.value.trim();
    if (!value) {
      // Every other rejection says what is wrong. The empty field said
      // nothing at all, so the button looked broken.
      bypassHint.hidden = false;
      bypassHint.textContent = t('settings.bypass.empty');
      bypassInput.focus();
      return;
    }
    const current = (app.settings() || {}).bypassDomains || [];
    if (current.length >= 100) {
      bypassHint.hidden = false;
      bypassHint.textContent = t('settings.bypass.full');
      return;
    }
    if (value.length > 200) {
      bypassHint.hidden = false;
      bypassHint.textContent = t('settings.bypass.tooLong');
      bypassInput.focus();
      return;
    }
    if (!isBypassPattern(value)) {
      bypassHint.hidden = false;
      bypassHint.textContent = t('settings.bypass.invalid');
      bypassInput.focus();
      return;
    }
    if (current.some((d) => String(d).toLowerCase() === value.toLowerCase())) {
      bypassHint.hidden = false;
      bypassHint.textContent = t('settings.bypass.duplicate');
      bypassInput.focus();
      return;
    }
    bypassBusy = true;
    bypassAdd.disabled = true;
    try {
      const ok = await save({ bypassDomains: [...current, value] }, 'bypass');
      if (ok) {
        // Clear only the entry that was sent. A fast typist may already be
        // entering the next domain while this save is in flight.
        if (bypassInput.value.trim() === value) bypassInput.value = '';
        bypassHint.hidden = true;
        bypassInput.focus();
      }
    } finally {
      bypassBusy = false;
      bypassAdd.disabled = false;
    }
  }

  bypassInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      addBypass();
    }
  });

  // Once the user changes the entry, an old "not a domain pattern" or
  // "already on the list" hint is stale; clear it as they type.
  bypassInput.addEventListener('input', () => {
    bypassHint.hidden = true;
    bypassHint.textContent = '';
  });

  const bypassAdd = button({
    label: t('settings.bypass.add'),
    variant: 'secondary',
    size: 'sm',
    onClick: addBypass,
  });

  const bypassGroup = h('div', { class: 'card group' }, [
    h('div', { class: 'group-head' }, [
      h('span', { class: 'group-icon', html: icon('globe', { size: 15 }) }),
      h('span', { class: 'group-title', text: t('settings.bypass.title') }),
    ]),
    h('div', { class: 'group-body' }, [
      h('div', { class: 'bypass-desc' }, [
        h('p', { class: 'setting-desc', text: t('settings.bypass.desc') }),
        h('p', { class: 'setting-desc t-3', text: t('settings.bypass.hint') }),
      ]),
      bypassList,
      h('div', { class: 'bypass-add' }, [bypassInput, bypassAdd]),
      bypassHint,
      h('p', { class: 'setting-desc t-3 bypass-tun', text: t('settings.bypass.tun') }),
    ]),
  ]);

  /* ---------------- windows ---------------- */

  const setSystemProxy = switchControl({
    checked: true,
    label: t('settings.setSystemProxy'),
    onChange: (v) => save({ setSystemProxy: v }, 'setSystemProxy'),
  });
  const launchAtStartup = switchControl({
    checked: false,
    label: t('settings.launchAtStartup'),
    onChange: (v) => save({ launchAtStartup: v }, 'launchAtStartup'),
  });
  const startMinimized = switchControl({
    checked: false,
    label: t('settings.startMinimized'),
    onChange: (v) => save({ startMinimized: v }, 'startMinimized'),
  });

  const proxyRow = row({
    key: 'setSystemProxy',
    label: t('settings.setSystemProxy'),
    desc: t('settings.setSystemProxyDesc'),
    control: setSystemProxy,
  });

  const windowsGroup = h('div', { class: 'card group' }, [
    h('div', { class: 'group-head' }, [
      h('span', { class: 'group-icon', html: icon('sliders', { size: 15 }) }),
      h('span', { class: 'group-title', text: t('settings.group.windows') }),
    ]),
    h('div', { class: 'group-body' }, [
      proxyRow,
      row({ key: 'launchAtStartup', label: t('settings.launchAtStartup'), desc: t('settings.launchAtStartupDesc'), control: launchAtStartup }),
      row({ key: 'startMinimized', label: t('settings.startMinimized'), desc: t('settings.startMinimizedDesc'), control: startMinimized }),
    ]),
  ]);

  /* ---------------- appearance ---------------- */

  const theme = segmented({
    label: t('settings.theme'),
    value: 'system',
    options: [['system', t('settings.system')], ['light', t('settings.light')], ['dark', t('settings.dark')]],
    // The theme is set by the shell, which paints it with a reveal. The
    // row still confirms the save, like every other row in the panel.
    onChange: (v) => {
      app.setTheme(v);
      flashSaved('theme');
    },
  });

  // The language names are written the way each language writes itself,
  // so the control can be read by someone who does not read the current
  // language.
  const language = segmented({
    label: t('settings.language'),
    value: 'en',
    options: [['en', t('lang.en')], ['fa', t('lang.fa')]],
    onChange: (v) => save({ language: v }, 'language'),
  });

  const appearanceGroup = h('div', { class: 'card group' }, [
    h('div', { class: 'group-head' }, [
      h('span', { class: 'group-icon', html: icon('palette', { size: 15 }) }),
      h('span', { class: 'group-title', text: t('settings.group.appearance') }),
    ]),
    h('div', { class: 'group-body' }, [
      row({ key: 'theme', label: t('settings.theme'), desc: t('settings.themeDesc'), control: theme }),
      row({ key: 'language', label: t('settings.language'), control: language }),
    ]),
  ]);

  /* ---------------- advanced ---------------- */

  const socksInput = h('input', {
    class: 'input num',
    type: 'number',
    inputmode: 'numeric',
    min: '1',
    max: '65535',
    id: 'socksPort',
    'aria-label': t('settings.socksPort'),
    style: 'width:110px',
  });
  const httpInput = h('input', {
    class: 'input num',
    type: 'number',
    inputmode: 'numeric',
    min: '1',
    max: '65535',
    id: 'httpPort',
    'aria-label': t('settings.httpPort'),
    style: 'width:110px',
  });

  function portError(msg) {
    toast(msg, { kind: 'error' });
    live.textContent = msg;
  }

  async function commitPorts() {
    const socks = Number(socksInput.value);
    const http = Number(httpInput.value);
    const cfg = app.settings() || {};
    if (!Number.isInteger(socks) || socks < 1 || socks > 65535 || !Number.isInteger(http) || http < 1 || http > 65535) {
      portError(t('settings.portInUse'));
      socksInput.value = String(cfg.socksPort || 1080);
      httpInput.value = String(cfg.httpPort || 8080);
      return;
    }
    if (socks === http) {
      portError(t('settings.portSame'));
      // Both fields go back to what is in force. Only one of them did, so
      // the screen showed two identical ports, neither of which was saved.
      socksInput.value = String(cfg.socksPort || 1080);
      httpInput.value = String(cfg.httpPort || 8080);
      return;
    }
    if (socks === cfg.socksPort && http === cfg.httpPort) return;
    // The chip goes on the row that changed. It always went on the SOCKS5
    // row, so a change to the HTTP port was confirmed on the wrong row and
    // the row that was edited was not confirmed at all.
    const changed = [];
    if (socks !== cfg.socksPort) changed.push('socksPort');
    if (http !== cfg.httpPort) changed.push('httpPort');
    await save({ socksPort: socks, httpPort: http }, changed);
  }

  for (const input of [socksInput, httpInput]) {
    input.addEventListener('blur', commitPorts);
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); input.blur(); }
      if (e.key === 'Escape') {
        const cfg = app.settings() || {};
        socksInput.value = String(cfg.socksPort || 1080);
        httpInput.value = String(cfg.httpPort || 8080);
        input.blur();
      }
    });
  }

  const verboseLogs = switchControl({
    checked: false,
    label: t('settings.verbose'),
    onChange: async (v) => {
      const ok = await save({ verboseLogs: v }, 'verboseLogs');
      if (ok) renderNotices();
    },
  });

  const verboseNotice = h('div', { class: 'setting-notice', hidden: true }, [
    h('div', {}, [
      h('div', { class: 'label', text: t('settings.verboseNotice.title') }),
      h('div', { class: 'desc', text: t('settings.verboseNotice.text') }),
    ]),
  ]);

  const advancedBody = h('div', { class: 'advanced-body' }, [
    h('div', {}, [
      row({ key: 'socksPort', label: t('settings.socksPort'), desc: t('settings.socksPortDesc'), control: socksInput }),
      row({ key: 'httpPort', label: t('settings.httpPort'), desc: t('settings.httpPortDesc'), control: httpInput }),
      row({ key: 'verboseLogs', label: t('settings.verbose'), desc: t('settings.verboseDesc'), control: verboseLogs }),
      verboseNotice,
    ]),
  ]);

  let advancedOpen = false;
  const advancedToggle = h('button', { class: 'advanced-toggle', type: 'button', 'aria-expanded': 'false' }, [
    h('span', { text: t('settings.advancedShow') }),
    h('span', { class: 'chev', html: icon('chev-right', { size: 14 }) }),
  ]);
  advancedToggle.addEventListener('click', () => {
    advancedOpen = !advancedOpen;
    advancedToggle.setAttribute('aria-expanded', String(advancedOpen));
    advancedBody.classList.toggle('open', advancedOpen);
    // A collapsed panel is out of the tab order. It used to stay in it, so
    // the caret could land on a port field that was not on the screen, and
    // typing into it changed the setting with nothing visible to show for
    // it.
    advancedBody.inert = !advancedOpen;
    advancedToggle.firstElementChild.textContent = advancedOpen ? t('settings.advancedHide') : t('settings.advancedShow');
    // The chevron is turned by a class, not by an inline style, so the
    // right-to-left rules can mirror it.
    advancedToggle.classList.toggle('is-open', advancedOpen);
  });
  // The panel starts collapsed, so it starts out of the tab order too.
  advancedBody.inert = true;

  const advancedGroup = h('div', { class: 'card group' }, [
    h('div', { class: 'group-head' }, [
      h('span', { class: 'group-icon', html: icon('bolt', { size: 15 }) }),
      h('span', { class: 'group-title', text: t('settings.group.advanced') }),
    ]),
    advancedToggle,
    advancedBody,
  ]);

  /* ---------------- account ---------------- */

  const accountTitle = h('div', { class: 'setting-label' });
  const accountDesc = h('div', { class: 'setting-desc' });
  const accountAction = h('div', { class: 'setting-control' });
  const accountRow = h('div', { class: 'setting' }, [
    h('div', { class: 'setting-main' }, [accountTitle, accountDesc]),
    accountAction,
  ]);

  const accountGroup = h('div', { class: 'card group' }, [
    h('div', { class: 'group-head' }, [
      h('span', { class: 'group-icon', html: icon('user', { size: 15 }) }),
      h('span', { class: 'group-title', text: t('settings.group.account') }),
    ]),
    h('div', { class: 'group-body' }, [accountRow]),
  ]);

  /* ---------------- version ---------------- */

  const versionMeta = h('div', { class: 'version-meta', id: 'versionMeta', text: '' });
  // The disclaimer lives in the interface, not only in the README: the app
  // was installed from a file, so there is no repository page to point at.
  const aboutBtn = button({
    label: t('settings.about'),
    variant: 'secondary',
    size: 'sm',
    iconName: 'info',
    onClick: () => {
      openModal({
        title: t('about.title'),
        iconName: 'fox',
        body: h('div', { class: 'about-body' }, [
          h('p', { text: t('about.body') }),
          h('p', { class: 'about-disclaimer', text: t('about.disclaimer') }),
        ]),
        actions: [{ label: t('common.close'), variant: 'primary', onClick: (close) => close() }],
      });
    },
  });
  const diagBtn = button({
    label: t('settings.copyDiagnostics'),
    variant: 'secondary',
    size: 'sm',
    iconName: 'copy',
    onClick: async () => {
      const ok = await app.copyDiagnostics();
      toast(ok ? t('settings.diagnosticsCopied') : t('logs.copyFailed'), { kind: ok ? 'ok' : 'error' });
    },
  });
  const versionActions = h('div', { class: 'version-actions' }, [aboutBtn, diagBtn]);
  const versionRow = h('div', { class: 'card version-row' }, [versionMeta, versionActions]);

  col.append(head, live, connectionGroup, bypassGroup, windowsGroup, appearanceGroup, advancedGroup, accountGroup, versionRow);
  inner.append(col);
  root.append(inner);

  /* ---------------- render ---------------- */

  function renderNotices() {
    const cfg = app.settings() || {};
    const st = app.state();
    const elevated = !!(st && st.elevated);
    const needsAdmin = !!cfg.tunMode && !elevated;
    tunNotice.hidden = !needsAdmin;
    elevateBtn.disabled = !needsAdmin;
    verboseNotice.hidden = !cfg.verboseLogs;

    const proxyDisabled = !!cfg.tunMode;
    proxyRow.classList.toggle('is-disabled', proxyDisabled);
    setSystemProxy.disabled = proxyDisabled;
    setSystemProxy.title = proxyDisabled ? t('settings.unusedInTun') : '';

    // A port change only takes effect on the next connection, and the
    // engine quietly moves to another port when the one asked for is
    // taken. Both are said here rather than left to the log.
    const connected = !!(st && st.status && st.status.state === 'connected');
    setHint('socksPort', connected ? t('settings.disabledConnected') : '');
    setHint('httpPort', connected ? t('settings.disabledConnected') : '');

    const liveHttp = st && st.status && st.status.httpAddr;
    const actual = liveHttp ? liveHttp.split(':').pop() : '';
    if (actual && String(actual) !== String(cfg.httpPort || '')) {
      setHint('httpPort', t('settings.portFallback', { port: cfg.httpPort || '', actual }));
    }
  }

  function renderAccount(state) {
    const signedIn = !!(state.user && state.user.signedIn);
    const email = signedIn ? state.user.email || '' : '';
    accountTitle.replaceChildren(h('span', {
      class: 'truncate',
      text: signedIn ? email : t('settings.signedOut'),
      title: email || null,
    }));
    if (!signedIn) {
      accountDesc.textContent = t('settings.signedOutDesc');
      accountAction.replaceChildren(button({
        label: t('settings.signIn'), variant: 'primary', size: 'sm', onClick: () => app.openLogin(),
      }));
      return;
    }
    const accounts = state.accounts || [];
    const parts = [t('settings.signedInDesc')];
    if (accounts.length > 1) parts.push(t('settings.multiAccount', { n: accounts.length }));
    accountDesc.textContent = parts.join(' ');

    // The subscribed tier is in the data model and was never shown, so a
    // subscriber saw the same "free allowance" copy as everyone else.
    const active = accounts.find((a) => a.active);
    if (active && active.subscribed) {
      accountDesc.replaceChildren(
        h('span', { text: t('settings.signedInDesc') }),
        h('span', { class: 'acct-tier', text: ' · ' + t('account.subscribed') }),
      );
    }

    accountAction.replaceChildren(
      button({ label: t('account.add'), variant: 'secondary', size: 'sm', onClick: () => app.openLogin({ add: true }) }),
      button({ label: t('settings.signOut'), variant: 'ghost', size: 'sm', onClick: () => app.confirmSignOut() }),
    );
  }

  function update(state) {
    if (!state) return;
    const cfg = state.settings || {};

    // Bypass changes are as real as any switch. They used to sit after the
    // signature early-return, so a save that only touched this list never
    // repainted it. Check it before the return.
    const bypass = Array.isArray(cfg.bypassDomains) ? cfg.bypassDomains : [];
    const bypassSig = JSON.stringify(bypass);
    if (bypassSig !== lastBypassSig) {
      lastBypassSig = bypassSig;
      renderBypass(bypass);
    }

    const sig = [
      cfg.transport, cfg.verifyExit, cfg.autoConnect, cfg.directFallback, cfg.tunMode,
      cfg.setSystemProxy, cfg.launchAtStartup, cfg.startMinimized, cfg.theme,
      cfg.language, cfg.socksPort, cfg.httpPort, cfg.verboseLogs,
      state.elevated, state.user && state.user.signedIn, state.user && state.user.email,
      (state.accounts || []).length,
    ].join('|');
    if (sig === lastSig) return;
    lastSig = sig;

    segmentedSet(transport, cfg.transport || 'auto');
    segmentedSet(tunMode, cfg.tunMode ? 'tun' : 'proxy');
    segmentedSet(theme, cfg.theme || 'system');
    segmentedSet(language, cfg.language || 'en');

    verifyExit.setAttribute('aria-checked', String(!!cfg.verifyExit));
    autoConnect.setAttribute('aria-checked', String(!!cfg.autoConnect));
    directFallback.setAttribute('aria-checked', String(!!cfg.directFallback));
    setSystemProxy.setAttribute('aria-checked', String(!!cfg.setSystemProxy));
    launchAtStartup.setAttribute('aria-checked', String(!!cfg.launchAtStartup));
    startMinimized.setAttribute('aria-checked', String(!!cfg.startMinimized));
    verboseLogs.setAttribute('aria-checked', String(!!cfg.verboseLogs));

    if (document.activeElement !== socksInput) socksInput.value = String(cfg.socksPort || 1080);
    if (document.activeElement !== httpInput) httpInput.value = String(cfg.httpPort || 8080);

    versionMeta.textContent = `${t('settings.version')} ${state.version || ''} · ${state.platform || ''}`.replace(/\s·\s$/, '');
    renderNotices();
    renderAccount(state);
  }

  return {
    root,
    update,
    // go() calls update again with the live store state immediately after
    // this; reading the live state here avoids briefly rendering a list
    // that changed while another view was open.
    onEnter: () => { const state = app.state(); if (state) update(state); },
    // The shell calls this after a change of language: the view is thrown
    // away and made again when the language changes, so the chip that
    // confirms the save belongs to the new one.
    flashSaved,
  };
}
