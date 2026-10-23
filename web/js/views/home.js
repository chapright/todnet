// Home: the state is the hero. One orb, one primary action, four quiet cards.
// Everything updates by patching stable nodes, so the 1 s poll never restarts
// an animation or steals focus.

import { t, getLocale } from '../i18n.js';
import { icon } from '../icons.js';
import { h, button, iconButton, flagChip, copyValue, ring, segmented, segmentedSet, skeleton } from '../components/ui.js';
import { toast } from '../components/overlay.js';
import { describeState, describeError, formatBytes, formatDuration, formatDate, countryName, ltrIsolate } from '../describeState.js';
import { tween } from '../motion.js';

const ORB = 232;
const R = 104;
const CIRC = 2 * Math.PI * R;
const RATE_WINDOW = 5;

export function createHome(app) {
  const root = h('section', { class: 'view', id: 'view-home' });
  const inner = h('div', { class: 'view-inner home-inner' });

  // --- top row ---
  // Only transient notices live here. The connection state itself is read
  // from the stage: the big button, the headline and the arc already say it,
  // so a second "Disconnected" / "Not protected" pill was noise.
  const bypass = h('span', { class: 'chip tone-busy bypass-chip', hidden: true }, [
    h('span', { html: icon('alert', { size: 13 }) }),
    h('span', { text: t('home.bypass') }),
  ]);
  const modePill = h('span', { class: 'pill', id: 'modePill', hidden: true });
  const transportPill = h('span', { class: 'pill', id: 'transportPill', hidden: true });
  // The tiles hold what the tunnel is doing. They are only there to be
  // looked at, so they can be put away.
  const tilesBtn = iconButton({
    name: 'chev-down',
    label: t('home.tiles.show'),
    size: 'sm',
    cls: 'tiles-toggle',
  });
  const top = h('div', { class: 'home-top' }, [
    h('div', { class: 'home-badges' }, [bypass]),
    h('span', { class: 'grow' }),
    h('div', { class: 'home-pills' }, [modePill, transportPill]),
    tilesBtn,
  ]);

  // --- orb ---
  const arc = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
  arc.setAttribute('class', 'arc');
  arc.setAttribute('cx', String(ORB / 2));
  arc.setAttribute('cy', String(ORB / 2));
  arc.setAttribute('r', String(R));
  arc.setAttribute('fill', 'none');
  arc.setAttribute('stroke-width', '3');
  arc.setAttribute('stroke-dasharray', String(CIRC));
  arc.setAttribute('stroke-dashoffset', String(CIRC));
  arc.setAttribute('transform', `rotate(-90 ${ORB / 2} ${ORB / 2})`);

  const dash = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
  dash.setAttribute('class', 'arc-dash');
  dash.setAttribute('cx', String(ORB / 2));
  dash.setAttribute('cy', String(ORB / 2));
  dash.setAttribute('r', String(R + 8));
  dash.setAttribute('fill', 'none');
  dash.setAttribute('stroke-width', '1');
  dash.setAttribute('stroke-dasharray', '3 9');
  dash.setAttribute('opacity', '0');

  const ringSvg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  ringSvg.setAttribute('class', 'orb-ring');
  ringSvg.setAttribute('viewBox', `0 0 ${ORB} ${ORB}`);
  ringSvg.setAttribute('aria-hidden', 'true');
  const track = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
  track.setAttribute('class', 'track');
  track.setAttribute('cx', String(ORB / 2));
  track.setAttribute('cy', String(ORB / 2));
  track.setAttribute('r', String(R));
  track.setAttribute('fill', 'none');
  track.setAttribute('stroke-width', '3');
  ringSvg.append(track, arc, dash);

  const orbBtn = h('button', { class: 'orb-btn', type: 'button', id: 'orbBtn', 'aria-label': t('home.connect') });
  const core = h('div', { class: 'orb-core' }, [
    h('span', { class: 'orb-glyph power', html: icon('power', { size: 46 }) }),
    h('span', { class: 'orb-glyph stop', html: icon('stop', { size: 38 }) }),
    h('span', { class: 'orb-glyph shield', html: icon('shield-check', { size: 46 }) }),
  ]);
  const orb = h('div', { class: 'orb-wrap', id: 'orbWrap' }, [
    h('div', { class: 'orb-glow', 'aria-hidden': 'true' }),
    h('div', { class: 'orb-glow is-warn', 'aria-hidden': 'true' }),
    ringSvg,
    core,
    orbBtn,
  ]);

  const headline = h('h1', { class: 'stage-headline', id: 'stageHeadline', text: t('state.disconnected') });
  const subline = h('p', { class: 'stage-sub', id: 'stageSub' });
  // The exit address is repeated on the stage so the proof that the tunnel
  // works is visible even when the card below has been pushed off screen
  // by a narrow window.
  const stageExit = h('div', { class: 'stage-exit', id: 'stageExit', hidden: true });
  const stageBody = h('div', { class: 'stage-body' }, [orb, headline, subline, stageExit]);

  const primary = button({ label: t('home.connect'), variant: 'primary', size: 'lg' });
  primary.id = 'primaryAction';
  primary.classList.add('is-fixed');

  const locChip = h('button', { class: 'loc-chip', id: 'locChip', type: 'button', title: t('home.location.change') });
  const actions = h('div', { class: 'stage-actions' }, [primary, locChip]);

  const stage = h('div', { class: 'card stage', id: 'stage', 'data-state': 'idle' }, [
    h('div', { class: 'ambient', 'aria-hidden': 'true' }, [h('span', { class: 'mesh' })]),
    stageBody,
    actions,
  ]);

  // --- exit IP ---
  const exitValue = h('div', { class: 'ip-value num', id: 'exitIp' });
  const exitMeta = h('div', { class: 'ip-meta', id: 'exitMeta' });
  const exitCard = h('div', { class: 'card home-card spotlight' }, [
    h('div', { class: 'card-head' }, [
      h('span', { class: 'card-label', text: t('home.exit.title') }),
      h('span', { class: 't-3', html: icon('lock', { size: 14 }), 'aria-hidden': 'true' }),
    ]),
    exitValue,
    exitMeta,
  ]);

  // --- session ---
  // The unit is part of the initial value so the readout does not start as
  // "0 B" and turn into "0 B/s" a second later.
  const upEl = h('b', { class: 'num', id: 'upVal', text: '0 B/s' });
  const downEl = h('b', { class: 'num', id: 'downVal', text: '0 B/s' });
  const activeEl = h('span', { class: 't-caption t-3', id: 'activeVal' });
  const totalEl = h('div', { class: 'metric-total', id: 'totalVal' });
  const sparkHost = h('div', { class: 'spark', id: 'sparkHost', 'aria-hidden': 'true' });
  const legend = h('div', { class: 'spark-legend', id: 'sparkLegend', hidden: true }, [
    h('span', { class: 'key up' }, [
      h('span', { class: 'key-ico', html: icon('up', { size: 13 }) }),
      t('home.session.up'),
    ]),
    h('span', { class: 'key down' }, [
      h('span', { class: 'key-ico', html: icon('down', { size: 13 }) }),
      t('home.session.down'),
    ]),
  ]);
  const sessionCard = h('div', { class: 'card home-card spotlight is-session' }, [
    h('div', { class: 'card-head' }, [
      h('span', { class: 'card-label', text: t('home.session.title') }),
      activeEl,
    ]),
    h('div', { class: 'metric-row' }, [
      h('span', { class: 'metric up' }, [h('span', { class: 'metric-ico', html: icon('up', { size: 14 }) }), upEl]),
      h('span', { class: 'metric down' }, [h('span', { class: 'metric-ico', html: icon('down', { size: 14 }) }), downEl]),
    ]),
    totalEl,
    sparkHost,
    legend,
  ]);

  // --- allowance ---
  const quotaRing = ring({ size: 60, stroke: 6, value: 0 });
  const allowBig = h('div', { class: 'allow-big num', id: 'allowBig', text: '—' });
  const allowSub = h('div', { class: 'allow-sub', id: 'allowSub', text: t('home.allow.unknown') });
  const allowWarn = h('div', { class: 'allow-warn', id: 'allowWarn', hidden: true });
  const allowCard = h('div', { class: 'card home-card spotlight' }, [
    h('div', { class: 'card-head' }, [
      h('span', { class: 'card-label', text: t('home.allow.title') }),
    ]),
    h('div', { class: 'allow-row' }, [quotaRing, h('div', { class: 'allow-text' }, [allowBig, allowSub, allowWarn])]),
  ]);

  // --- details ---
  const detailList = h('dl', { class: 'details-list', id: 'detailList' });
  const details = h('details', { class: 'disclosure' }, [
    h('summary', {}, [h('span', { class: 'chev', html: icon('chev-right', { size: 14 }) }), h('span', { text: t('home.details.title') })]),
    h('div', { class: 'disclosure-body' }, [h('div', {}, [detailList])]),
  ]);
  const detailCard = h('div', { class: 'card home-card' }, [details]);

  // --- sign-in ---
  // The app is behind a sign-in, so until there is an account this is
  // all there is: two buttons, and the language.
  const language = segmented({
    label: t('settings.language'),
    value: getLocale(),
    options: [['en', t('lang.en')], ['fa', t('lang.fa')]],
    onChange: (v) => app.setLanguage(v),
  });
  const signin = h('div', { class: 'signin' }, [
    h('div', { class: 'signin-mark' }, [
      h('span', { html: icon('fox', { size: 46 }) }),
    ]),
    h('div', { class: 'signin-intro' }, [
      h('h1', { class: 'sr-only', text: t('signin.title') }),
      h('p', { class: 'signin-lede', text: t('signin.what') }),
      h('p', { class: 'signin-note', text: t('signin.unofficial') }),
    ]),
    h('div', { class: 'signin-actions' }, [
      button({ label: t('signin.primary'), variant: 'primary', size: 'lg', onClick: () => app.openLogin() }),
      button({
        label: t('signin.create'),
        variant: 'secondary',
        size: 'lg',
        onClick: () => app.openURL('https://accounts.firefox.com/signup'),
      }),
    ]),
    h('div', { class: 'signin-foot' }, [language]),
  ]);

  const stack = h('div', { class: 'home-stack' }, [exitCard, sessionCard, allowCard, detailCard]);
  const grid = h('div', { class: 'home-grid' }, [stage, stack]);
  inner.append(top, grid);
  root.append(inner);

  // --- local state ---
  let last = null;
  let samples = [];
  let lastCounters = null;
  let displayedUp = 0;
  let displayedDown = 0;
  let drawnSamples = 0;
  let drawnMax = 0;
  // What the chart area currently holds: nothing, a skeleton while the exit
  // is being verified, or the real curves. It is rebuilt only on a change.
  let sparkMode = '';
  // null, not the empty string: the first render has to build the
  // placeholder, and the empty string is also what an unknown address
  // looks like.
  let shownIP = null;
  let cancelUp = null;
  let cancelDown = null;
  // Whether the tiles are put away. The user decides, and the choice
  // holds until they change it.
  let tilesVisible = true;
  let signedOut = null;
  // What the stage's primary button does while the last connect attempt
  // failed: open the account menu when the allowance is spent, open the log
  // for anything else. Empty means the normal connect/cancel/disconnect.
  let stageFailureAction = '';

  function setText(el, value) {
    if (el.textContent !== value) el.textContent = value;
  }

  // The screen changes shape with the account: no account means the
  // sign-in and nothing else.
  function render() {
    inner.replaceChildren(...(signedOut ? [signin] : [top, grid]));
  }

  function setOrbState(tone) {
    const map = { idle: 'idle', busy: 'busy', ok: 'ok', error: 'error' };
    const s = map[tone] || 'idle';
    if (stage.dataset.state !== s) stage.dataset.state = s;
  }

  function updateAction(desc, signedIn, failure) {
    let label = t('home.connect');
    let aria = t('home.connect');
    stageFailureAction = '';
    if (!signedIn) { label = t('home.signIn'); aria = t('home.signIn'); }
    else if (failure) {
      // A failure that cannot be retried must not offer Retry. The stage
      // names the one place that can help and leaves the explanation and
      // the recovery action to the banner above (audit X-05).
      stageFailureAction = failure.code === 'allowance' ? 'accounts' : 'details';
      label = stageFailureAction === 'accounts' ? t('account.title') : t('error.details');
      aria = label;
    }
    else if (desc.action === 'cancel') { label = t('home.cancel'); aria = t('home.cancel'); }
    else if (desc.action === 'disconnect') { label = t('home.disconnect'); aria = t('home.disconnect'); }
    else if (desc.action === 'retry') { label = t('common.retry'); aria = t('common.retry'); }
    // While the cancel is in flight the button says so, so the user can see
    // that the click was heard.
    if (app.isCancelling && app.isCancelling()) {
      label = t('home.cancelling');
      aria = t('home.cancelling');
    }
    const span = primary.querySelector('.btn-label span:last-child');
    if (span) setText(span, label);
    // On a failure the banner owns the recovery action. The stage's Details
    // is a way into the log, not a second call to action, so it must not
    // shout in accent orange beside the banner's Try again.
    const quiet = stageFailureAction === 'details';
    primary.classList.toggle('primary', !quiet);
    primary.classList.toggle('secondary', quiet);
    primary.setAttribute('aria-label', aria);
    orbBtn.setAttribute('aria-label', aria);
    primary.disabled = desc.action === 'none' && !failure;
    orbBtn.disabled = !!failure || desc.action === 'none';
  }

  function updateLocationChip(status, settings, locations) {
    const loc = status.location || preferred(settings, locations);
    const code = loc ? loc.countryCode : '';
    // The server sends Mozilla's own English name; the interface shows the
    // name in its own language.
    const name = loc ? (countryName(code) || loc.countryName) : t('home.location.auto');
    const key = code + '|' + (loc ? loc.cityCode : '');
    if (locChip.dataset.key === key) return;
    locChip.dataset.key = key;
    locChip.replaceChildren(
      flagChip(code, { auto: !code || code === 'REC' }),
      h('span', { class: 'truncate', text: name }),
      h('span', { class: 'chev', html: icon('chev-down', { size: 14 }) }),
    );
  }

  function preferred(settings, locations) {
    if (!locations || !locations.length || !settings) return null;
    const cc = String(settings.countryCode || '').toUpperCase();
    if (!cc) return null;
    const matches = locations.filter((l) => String(l.countryCode).toUpperCase() === cc);
    if (!matches.length) return null;
    const city = String(settings.cityCode || '').toLowerCase();
    return matches.find((l) => String(l.cityCode || '').toLowerCase() === city) || matches[0];
  }

  // pushSample records the bytes moved since the previous poll, and how
  // long that took.
  //
  // The elapsed time is part of the sample because the poll is not a
  // metronome: a poll that took two seconds moved twice the bytes of one
  // that took a second, and dividing by a fixed sample count read it as
  // half the speed.
  function pushSample(status) {
    const up = Number(status.bytesUp) || 0;
    const down = Number(status.bytesDown) || 0;
    const now = Date.now();
    if (!lastCounters) { lastCounters = { up, down, at: now }; return; }
    const dt = (now - lastCounters.at) / 1000;
    if (dt <= 0) return;
    const du = Math.max(0, up - lastCounters.up);
    const dd = Math.max(0, down - lastCounters.down);
    lastCounters = { up, down, at: now };
    samples.push({ up: du, down: dd, dt });
    if (samples.length > 60) samples.shift();
  }

  // The rate is the traffic over the window divided by the time it took.
  function currentRate() {
    if (!samples.length) return { up: 0, down: 0 };
    const win = samples.slice(-RATE_WINDOW);
    let up = 0;
    let down = 0;
    let dt = 0;
    for (const s of win) { up += s.up; down += s.down; dt += s.dt; }
    if (dt <= 0) return { up: 0, down: 0 };
    return { up: Math.round(up / dt), down: Math.round(down / dt) };
  }

  // The chart is only redrawn when the data actually changed. It was
  // rebuilt on every poll, and it kept drawing after a disconnect, because
  // the samples kept arriving with a zero delta.
  function resetSession() {
    samples = [];
    lastCounters = null;
    drawnSamples = 0;
    sparkMode = '';
    sparkHost.replaceChildren();
    legend.hidden = true;
  }

  function drawSpark(state) {
    // A single flat sample drew a straight orange rule across the card,
    // which read as a divider rather than a chart. Draw nothing until there
    // is a real curve to show; the container keeps its height so nothing
    // shifts. While the tunnel is up but the exit is still being verified,
    // the area carries a skeleton rather than an unexplained void.
    if (samples.length < 3) {
      const wanting = state && state.status && state.status.state === 'connected' ? 'skeleton' : 'empty';
      if (sparkMode !== wanting) {
        sparkHost.replaceChildren(...(wanting === 'skeleton' ? [skeleton('spark-skeleton')] : []));
        sparkMode = wanting;
      }
      legend.hidden = true;
      drawnSamples = 0;
      return;
    }
    sparkMode = 'chart';
    // Each series is scaled to its own maximum, so a fast download and
    // a slow upload are both readable instead of one of them being a flat
    // line along the floor.
    const upMax = Math.max(...samples.map((s) => s.up));
    const downMax = Math.max(...samples.map((s) => s.down));
    if (upMax <= 0 && downMax <= 0) {
      if (drawnSamples !== 0) { sparkHost.replaceChildren(); legend.hidden = true; drawnSamples = 0; }
      return;
    }
    // Nothing changed since the last draw.
    if (drawnSamples === samples.length && drawnMax === upMax + downMax) return;
    drawnSamples = samples.length;
    drawnMax = upMax + downMax;
    legend.hidden = false;
    const w = 300;
    const hgt = 34;
    const step = w / Math.max(1, samples.length - 1);
    const point = (v, i, top) => [i * step, hgt - (v / top) * (hgt - 6) - 3];
    const path = (key, top) => samples
      .map((s, i) => { const p = point(s[key], i, top); return `${i ? 'L' : 'M'}${p[0].toFixed(1)},${p[1].toFixed(1)}`; })
      .join(' ');
    const upLine = upMax > 0 ? path('up', upMax) : '';
    const downLine = downMax > 0 ? path('down', downMax) : '';
    // The strokes are not scaled with the drawing: the chart is stretched
    // to fill the card, and a scaled stroke turns a 1.6px line into a
    // 7px slab.
    const parts = [`<svg viewBox="0 0 ${w} ${hgt}" preserveAspectRatio="none">`];
    if (downLine) parts.push(`<path class="line down" vector-effect="non-scaling-stroke" d="${downLine}"/>`);
    if (upLine) parts.push(`<path class="line" vector-effect="non-scaling-stroke" d="${upLine}"/>`);
    parts.push('</svg>');
    sparkHost.innerHTML = parts.join('');
  }

  function update(state, prev) {
    if (!state) return;
    const st = state.status || {};
    const signedIn = !!(state.user && state.user.signedIn);
    // The attempt is over: the next one counts from its own start.
    if (st.state !== 'connecting') connectingSince = 0;

    // The signed-out home is the sign-in and nothing else.
    if (signedOut !== !signedIn) {
      signedOut = !signedIn;
      render();
    }
    if (signedOut) {
      segmentedSet(language, (state.settings && state.settings.language) || getLocale());
      return;
    }

    const elapsed = st.state === 'connecting'
      ? Math.max(0, Math.floor((Date.now() - (st.connectedAt ? new Date(st.connectedAt).getTime() : Date.now())) / 1000))
      : 0;
    const desc = describeState(st, { signedIn, country: countryOf(st, state.settings, state.locations), elapsed });

    // The tiles report what the tunnel is doing, so there is nothing to
    // report until there is a tunnel. They arrive with the connection and
    // can be put away again.
    const connected = st.state === 'connected';
    const showTiles = connected && tilesVisible;
    if (stack.hidden === showTiles) {
      stack.hidden = !showTiles;
      grid.classList.toggle('is-single', !showTiles);
    }
    tilesBtn.hidden = !connected;
    const tileLabel = tilesVisible ? t('home.tiles.hide') : t('home.tiles.show');
    if (tilesBtn.getAttribute('aria-label') !== tileLabel) {
      tilesBtn.setAttribute('aria-label', tileLabel);
      tilesBtn.title = tileLabel;
      tilesBtn.classList.toggle('is-open', tilesVisible);
    }

    setOrbState(desc.tone);
    const failure = st.state === 'error' ? describeError(st.error) : null;
    // The stage names the cause; the banner above names it and explains it.
    // It used to say the generic "Could not connect" here, so the specific
    // reason existed only in the banner (and disappeared when it was closed).
    setText(headline, failure ? failure.title : desc.label);
    // Choosing another location cannot refill an allowance, so the exhausted
    // state does not offer one (audit X-05).
    locChip.hidden = !!(failure && failure.code === 'allowance');

    if (failure) {
      // The banner holds the explanation and the actions. The stage says
      // only what happened, so the same sentence is not on screen twice.
      setText(subline, '');
    } else if (st.state === 'connected' && st.connectedAt) {
      setText(subline, `${desc.hint} · ${ltrIsolate(formatDuration(Date.now() - new Date(st.connectedAt).getTime()))}`);
    } else {
      setText(subline, desc.hint);
    }
    updateAction(desc, signedIn, failure);

    // arc
    let offset = CIRC;
    if (st.state === 'connected') offset = 0;
    else if (st.state === 'connecting' || st.state === 'disconnecting') offset = CIRC * 0.28;
    else if (st.state === 'error') offset = CIRC * 0.12;
    arc.style.strokeDashoffset = String(offset);
    dash.setAttribute('opacity', st.state === 'connecting' ? '.7' : '0');

    // protection notices
    bypass.hidden = !(state.settings && state.settings.directFallback);
    modePill.hidden = st.state !== 'connected' || !st.mode;
    if (!modePill.hidden) setText(modePill, st.mode === 'tun' ? t('state.mode.tun') : t('state.mode.proxy'));
    transportPill.hidden = st.state !== 'connected' || !st.transport;
    if (!transportPill.hidden) {
      // The pill used to print the machine token ("h2") while Settings said
      // "HTTP/2" for the same thing.
      const label = { h2: 'settings.h2', h3: 'settings.h3', auto: 'settings.auto' }[st.transport];
      setText(transportPill, label ? t(label) : st.transport);
    }
    // The row carries the toggle, so it stays while the toggle is on
    // screen.
    top.hidden = bypass.hidden && modePill.hidden && transportPill.hidden && tilesBtn.hidden;

    updateLocationChip(st, state.settings, state.locations);

    // exit IP
    const ip = st.exitIp || '';
    const verifying = st.state === 'connected' && (st.verifying || !ip);
    // The address is only rebuilt when it changes. It was rebuilt on every
    // poll, which recreated the copy button and could swallow a click on
    // it, and it made the card flicker.
    if (ip !== shownIP) {
      shownIP = ip;
      exitValue.replaceChildren();
      if (ip) {
        exitValue.className = 'ip-value num' + (ip.length > 19 ? ' is-long' : '');
        exitValue.append(copyValue(ip, {
          label: t('common.copy'),
          onError: () => toast(t('logs.copyFailed'), { kind: 'error' }),
        }));
      } else if (verifying) {
        exitValue.className = 'ip-value';
        exitValue.append(h('span', { class: 'skeleton', style: 'width:210px;height:20px;display:block' }));
      } else {
        exitValue.className = 'ip-value';
        exitValue.append(h('span', { class: 't-3', text: '—' }));
      }
    }
    exitMeta.replaceChildren();
    if (ip) {
      const secs = st.exitLatencyMs ? (st.exitLatencyMs / 1000).toFixed(1) : '';
      exitMeta.append(
        h('span', { text: countryName(st.exitCountry) || st.exitCountry || '' }),
        secs ? h('span', { class: 'sep', text: '·' }) : null,
        secs ? h('span', { text: t('home.exit.verified', { seconds: secs }) }) : null,
      );
    } else {
      // "Verified after connecting" was shown for a failed connection too,
      // which reads as an invitation rather than a diagnosis.
      const msg = st.state === 'error' ? t('home.exit.unavailable') : (verifying ? t('home.exit.verifying') : t('home.exit.empty'));
      exitMeta.append(h('span', { text: msg }));
    }

    // The same address on the stage, so it is visible without scrolling.
    // A full IPv6 address is 39 characters and does not fit beside the
    // country, so the country is dropped when the address needs the room.
    stageExit.hidden = !ip;
    if (ip) {
      const country = st.exitCountry ? (countryName(st.exitCountry) || st.exitCountry) : '';
      const long = ip.length > 24;
      stageExit.replaceChildren(...[
        h('span', { html: icon('shield-check', { size: 13 }), 'aria-hidden': 'true' }),
        h('span', { class: 'mono', text: ip }),
        country && !long ? h('span', { class: 'sep', text: '·' }) : null,
        country && !long ? h('span', { text: country }) : null,
      ].filter(Boolean));
    }

    // session
    const live = st.state === 'connected';
    if (!live) {
      // A disconnected tunnel has no traffic, and the chart kept drawing
      // anyway because the samples kept arriving with a zero delta.
      resetSession();
    } else {
      pushSample(st);
      drawSpark(state);
    }
    const rate = currentRate();
    if (rate.up !== displayedUp) {
      if (cancelUp) cancelUp();
      cancelUp = tween(displayedUp, rate.up, (v) => {
        displayedUp = v;
        // The tween runs through fractions, and a byte count with a
        // fraction is not a thing. It is rounded before it is shown.
        upEl.textContent = ltrIsolate(formatBytes(Math.round(v)) + '/s');
      }, 400);
    }
    if (rate.down !== displayedDown) {
      if (cancelDown) cancelDown();
      cancelDown = tween(displayedDown, rate.down, (v) => {
        displayedDown = v;
        downEl.textContent = ltrIsolate(formatBytes(Math.round(v)) + '/s');
      }, 400);
    }
    setText(activeEl, st.activeConns ? t('home.session.active', { n: st.activeConns }) : (live ? '' : t('home.session.noData')));
    const totalUp = Number(st.bytesUp) || 0;
    const totalDown = Number(st.bytesDown) || 0;
    setText(totalEl, totalUp || totalDown
      ? t('home.session.total', { up: ltrIsolate(formatBytes(totalUp)), down: ltrIsolate(formatBytes(totalDown)) })
      : '');

    // allowance
    const max = Number(st.quotaMax) || 0;
    const left = Number(st.quotaLeft) || 0;
    if (max > 0) {
      // The ring shows what is left, matching the label beside it. Filling by
      // "used" made a healthy account look nearly empty.
      const remaining = Math.max(0, Math.min(1, left / max));
      quotaRing._set(remaining);
      quotaRing.classList.toggle('is-warn', remaining > 0 && remaining < 0.2);
      quotaRing.classList.toggle('is-danger', remaining <= 0);
      allowBig.textContent = t('home.allow.left', { left: ltrIsolate(formatBytes(left)) });
      allowSub.textContent = [t('home.allow.of', { total: ltrIsolate(formatBytes(max)) }), st.quotaReset ? t('home.allow.resets', { date: formatDate(st.quotaReset) }) : ''].filter(Boolean).join(' · ');
      const low = remaining > 0 && remaining < 0.2;
      allowWarn.hidden = !low;
      if (low) setText(allowWarn, t('home.allow.low', { date: st.quotaReset ? formatDate(st.quotaReset) : '' }));
    } else {
      quotaRing._set(0);
      quotaRing.classList.remove('is-warn', 'is-danger');
      // An empty track alone read as a loading spinner; hide it until there
      // is a real number to show.
      quotaRing.classList.add('is-empty');
      allowBig.textContent = '—';
      allowSub.textContent = st.state === 'connected' ? t('home.allow.waiting') : t('home.allow.unknown');
      allowWarn.hidden = true;
    }
    if (max > 0) quotaRing.classList.remove('is-empty');

    // details
    const rows = [];
    if (st.transport) rows.push([t('home.details.protocol'), st.transport]);
    if (st.location) rows.push([t('home.details.edge'), st.location.host || st.location.countryName]);
    if (st.state === 'connected') rows.push([t('home.details.dns'), st.mode === 'tun' ? t('home.details.dnsTun') : t('home.details.dnsProxy')]);
    if (st.mode) rows.push([t('home.details.mode'), st.mode === 'tun' ? t('state.mode.tun') : t('state.mode.proxy')]);
    const sig = JSON.stringify(rows);
    if (detailList.dataset.sig !== sig) {
      detailList.dataset.sig = sig;
      detailList.replaceChildren();
      for (const [k, v] of rows) {
        detailList.append(h('dt', { text: k }), h('dd', { class: 'ltr', text: v }));
      }
      if (!rows.length) detailList.append(h('dt', { text: '—' }), h('dd', { text: '' }));
    }

    last = state;
  }

  // countryOf names the country the attempt is about. While the attempt is
  // running the state names none, so the chosen country is the one that
  // answers: the stage said "Contacting …" while the chip right below it
  // said "Germany".
  function countryOf(st, settings, locations) {
    if (st && st.location) {
      const cc = String(st.location.countryCode || '').toUpperCase();
      if (cc === 'REC' && st.exitCountry) return countryName(st.exitCountry);
      return countryName(cc) || st.location.countryName;
    }
    const chosen = preferred(settings, locations);
    return chosen ? countryName(chosen.countryCode) || chosen.countryName : '';
  }

  // When the attempt started. It lives here rather than on the state,
  // because the state is a fresh object on every poll and the timestamp
  // would be gone before the next second was counted.
  let connectingSince = 0;

  function tick() {
    if (!last) return;
    const st = last.status;
    if (st.state === 'connected' && st.connectedAt) {
      setText(subline, `${describeState(st, { signedIn: true }).hint} · ${ltrIsolate(formatDuration(Date.now() - new Date(st.connectedAt).getTime()))}`);
    } else if (st.state === 'connecting') {
      if (!connectingSince) connectingSince = Date.now();
      const elapsed = Math.floor((Date.now() - connectingSince) / 1000);
      const desc = describeState(st, { signedIn: true, country: countryOf(st, last.settings, last.locations), elapsed });
      setText(subline, elapsed > 25 ? t('state.hint.tooLong') : desc.hint);
    }
  }

  primary.addEventListener('click', () => {
    if (stageFailureAction === 'accounts') app.openAccountMenu();
    else if (stageFailureAction === 'details') app.navigate('logs');
    else app.onPrimary();
  });
  orbBtn.addEventListener('click', () => app.onPrimary());
  locChip.addEventListener('click', (e) => app.openLocationPicker(e.currentTarget));
  tilesBtn.addEventListener('click', () => {
    tilesVisible = !tilesVisible;
    if (last) update(last, null);
  });
  return { root, update, tick };
}
