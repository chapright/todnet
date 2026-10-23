// Accessible primitives. Each one is a real control with a name, a state and
// a keyboard path — the audit's K-01/K-02/A-09 class of bugs cannot recur if
// views only build controls through these helpers.

import { icon } from '../icons.js';
import { t } from '../i18n.js';
import { startOffset } from '../dir.js';

export function h(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k === 'html') node.innerHTML = v;
    else if (k.startsWith('on') && typeof v === 'function') node.addEventListener(k.slice(2).toLowerCase(), v);
    else if (v === true) node.setAttribute(k, '');
    else node.setAttribute(k, v);
  }
  for (const child of [].concat(children)) {
    if (child == null || child === false) continue;
    node.append(child.nodeType ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function button({
  label, variant = 'secondary', size = 'md', iconName = '', onClick, loading = false,
  disabled = false, title = '', ariaLabel = '', type = 'button', cls = '',
} = {}) {
  const classes = ['btn', variant, size === 'sm' ? 'sm' : size === 'lg' ? 'lg' : '', cls].filter(Boolean).join(' ');
  const node = h('button', {
    class: classes + (loading ? ' loading is-fixed' : ''),
    type,
    title: title || null,
    'aria-label': ariaLabel || null,
    disabled: disabled || loading,
    'aria-busy': loading ? 'true' : null,
  });
  const inner = h('span', { class: 'btn-label' + (loading ? ' is-hidden' : '') });
  if (iconName) inner.append(h('span', { html: icon(iconName, { size: size === 'lg' ? 19 : 17 }) }));
  if (label) inner.append(h('span', { text: label }));
  node.append(inner);
  if (loading) node.append(h('span', { class: 'spinner', 'aria-hidden': 'true' }));
  if (onClick) node.addEventListener('click', onClick);
  return node;
}

export function iconButton({ name, label, onClick, size = 'md', disabled = false, cls = '', title = '' } = {}) {
  const node = h('button', {
    class: `icon-btn ${size === 'sm' ? 'sm' : size === 'lg' ? 'lg' : ''} ${cls}`.trim(),
    type: 'button',
    'aria-label': label,
    title: title || label,
    disabled: disabled || null,
  }, [h('span', { html: icon(name, { size: size === 'lg' ? 19 : size === 'sm' ? 15 : 17 }) })]);
  if (onClick) node.addEventListener('click', onClick);
  return node;
}

export function switchControl({ checked, label, onChange, disabled = false, describedBy = '' }) {
  const node = h('button', {
    class: 'switch',
    type: 'button',
    role: 'switch',
    'aria-checked': checked ? 'true' : 'false',
    'aria-label': label,
    'aria-describedby': describedBy || null,
    disabled: disabled || null,
  }, [h('span', { class: 'switch-knob', 'aria-hidden': 'true' })]);
  node.addEventListener('click', () => {
    const next = node.getAttribute('aria-checked') !== 'true';
    node.setAttribute('aria-checked', next ? 'true' : 'false');
    onChange(next);
  });
  return node;
}

export function segmented({ options, value, onChange, label, disabled = false }) {
  const wrap = h('div', { class: 'seg', role: 'group', 'aria-label': label });
  const thumb = h('span', { class: 'seg-thumb', 'aria-hidden': 'true' });
  wrap.append(thumb);
  // The current value lives on the control, not in a closure. It used to be
  // captured at build time, so once the user moved to another option the
  // control still believed it was on the first one — and the first option
  // could then never be selected again.
  wrap._value = String(value);
  const buttons = [];
  options.forEach(([val, text]) => {
    const b = h('button', {
      type: 'button',
      'aria-pressed': String(val === value),
      disabled: disabled || null,
      text,
    });
    b.dataset.value = String(val);
    b.addEventListener('click', () => {
      if (String(val) === wrap._value) return;
      wrap._value = String(val);
      // The control reflects its own state. It used to leave the pressed
      // state to whoever updated it from outside, and a control nobody
      // updated kept its thumb on the first option while the value it
      // reported had already moved.
      for (const other of buttons) {
        other.setAttribute('aria-pressed', String(other === b));
      }
      place();
      onChange(val);
    });
    buttons.push(b);
    wrap.append(b);
  });
  const place = () => {
    const active = buttons.find((b) => b.getAttribute('aria-pressed') === 'true') || buttons[0];
    if (!active || !active.offsetWidth) return;
    thumb.style.width = `${active.offsetWidth}px`;
    // The thumb follows the inline start of the control, which is its own
    // direction and not the page's.
    thumb.style.insetInlineStart = `${startOffset(active, wrap)}px`;
  };
  requestAnimationFrame(place);
  wrap._place = place;
  return wrap;
}

export function segmentedSet(wrap, value) {
  wrap._value = String(value);
  for (const b of wrap.querySelectorAll('button')) {
    b.setAttribute('aria-pressed', String(b.dataset.value === String(value)));
  }
  if (wrap._place) requestAnimationFrame(wrap._place);
}

// The bundled flag set. Anything outside this list falls back to a letter
// tile, so a country that Mozilla adds would look broken rather than
// simply unadorned.
const FLAG_CODES = new Set(('ar at au be bg br ca ch cl co de dk es fi fr gb gh ie in it jp kr mx my nl no nz pe ph pl pt se sg th us za').split(' '));

export function badgeHue(code) {
  const cc = String(code || '').toUpperCase();
  let h1 = 2166136261;
  for (let i = 0; i < cc.length; i++) {
    h1 ^= cc.charCodeAt(i);
    h1 = Math.imul(h1, 16777619);
  }
  return Math.abs(h1 >>> 0) % 360;
}

// The avatar ramp lives in tokens.css so the light theme can have its own
// values; every entry is >= 4.5:1 with white text. They are read once and
// cached, because reading a custom property forces a style recalculation.
let avatarRamp = null;
function loadAvatarRamp() {
  if (avatarRamp) return avatarRamp;
  const styles = getComputedStyle(document.documentElement);
  const ramp = [];
  for (let i = 1; i <= 8; i++) {
    const v = styles.getPropertyValue('--avatar-' + i).trim();
    if (v) ramp.push(v);
  }
  // A stylesheet that failed to load would leave the ramp empty and every
  // avatar unstyled, so a hard-coded fallback keeps the app usable.
  avatarRamp = ramp.length ? ramp : ['#A63D2A', '#8A4B1F', '#7A5A12', '#2F6B3A', '#1F6B63', '#2B4F8A', '#5B3F8A', '#8A2F63'];
  return avatarRamp;
}

export function avatarColor(seed) {
  const s = String(seed || '?');
  let n = 0;
  for (let i = 0; i < s.length; i++) n = (n * 31 + s.charCodeAt(i)) >>> 0;
  const ramp = loadAvatarRamp();
  return ramp[n % ramp.length];
}

export function flagChip(code, { lg = false, auto = false } = {}) {
  const cc = String(code || '').toLowerCase();
  if (auto || !cc) {
    return h('span', { class: `flag-chip${lg ? ' lg' : ''} auto`, 'aria-hidden': 'true' }, [
      h('span', { html: icon('bolt', { size: 13 }) }),
    ]);
  }
  if (FLAG_CODES.has(cc)) {
    return h('span', { class: `flag-chip${lg ? ' lg' : ''}`, 'aria-hidden': 'true' }, [
      h('img', { src: `flags/${cc}.svg`, alt: '', draggable: 'false' }),
    ]);
  }
  return h('span', {
    class: `flag-chip${lg ? ' lg' : ''}`,
    'aria-hidden': 'true',
    style: `background: hsl(${badgeHue(cc)} 32% 24%); color: hsl(${badgeHue(cc)} 60% 82%)`,
    text: cc.slice(0, 2).toUpperCase(),
  });
}

export function signalBars(level) {
  const n = Math.max(1, Math.min(4, level));
  return h('span', { class: `signal s${n}`, 'aria-hidden': 'true' }, [
    h('i'), h('i'), h('i'), h('i'),
  ]);
}

export function copyValue(text, { label = t('common.copy'), onError = null } = {}) {
  const wrap = h('span', { class: 'copy-value' });
  const value = h('span', { class: 'ltr', text });
  const btn = iconButton({
    name: 'copy',
    label,
    size: 'sm',
    cls: 'copy-btn',
    onClick: async () => {
      try {
        await navigator.clipboard.writeText(text);
        wrap.classList.add('is-copied');
        btn.innerHTML = icon('check', { size: 15 });
        btn.setAttribute('aria-label', t('common.copied'));
        setTimeout(() => {
          wrap.classList.remove('is-copied');
          btn.innerHTML = icon('copy', { size: 15 });
          btn.setAttribute('aria-label', label);
        }, 1400);
      } catch {
        // The clipboard can be refused by the browser. Say so rather than
        // leaving the button looking like nothing happened.
        if (typeof onError === 'function') onError();
      }
    },
  });
  wrap.append(value, btn);
  return wrap;
}

export function emptyState({ iconName = 'info', title, text, action = null }) {
  const node = h('div', { class: 'empty' }, [
    h('div', { class: 'empty-icon', html: icon(iconName, { size: 22 }) }),
    h('div', { class: 'empty-title', text: title }),
    h('div', { class: 'empty-text', text }),
  ]);
  if (action) node.append(action);
  return node;
}

export function skeleton(className = '') {
  return h('div', { class: `skeleton ${className}`.trim(), 'aria-hidden': 'true' });
}

let ringSeq = 0;

export function ring({ size = 60, stroke = 6, value = 0, className = '' } = {}) {
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  // The gradient lives inside the SVG, so the id has to be unique even if a
  // second ring is ever mounted. The arc is a brand ramp; warn/danger swap it
  // for a solid semantic colour via CSS.
  const gradId = `ring-grad-${++ringSeq}`;
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('width', size);
  svg.setAttribute('height', size);
  svg.setAttribute('viewBox', `0 0 ${size} ${size}`);
  svg.setAttribute('class', `ring ${className}`.trim());
  svg.setAttribute('aria-hidden', 'true');
  // An SVG circle starts at 3 o'clock. Turn the arc (not the whole SVG) so it
  // starts at 12 and grows clockwise from there. There is deliberately no
  // origin tick: it looked like a grey line cutting the arc, and the rounded
  // cap already says where the value begins.
  svg.innerHTML +=
    `<defs><linearGradient id="${gradId}" gradientUnits="userSpaceOnUse" ` +
    `x1="0" y1="0" x2="${size}" y2="${size}">` +
    `<stop offset="0" class="ring-stop-a"/>` +
    `<stop offset="1" class="ring-stop-b"/>` +
    `</linearGradient></defs>` +
    `<circle class="ring-track" cx="${size / 2}" cy="${size / 2}" r="${r}" fill="none" stroke-width="${stroke}"/>` +
    `<circle class="ring-value" cx="${size / 2}" cy="${size / 2}" r="${r}" fill="none" stroke="url(#${gradId})" stroke-width="${stroke}" ` +
    `transform="rotate(-90 ${size / 2} ${size / 2})" ` +
    `stroke-dasharray="${c}" stroke-dashoffset="${c * (1 - value)}"/>`;
  svg._set = (v) => {
    svg.querySelector('.ring-value').style.strokeDashoffset = String(c * (1 - Math.max(0, Math.min(1, v))));
  };
  return svg;
}

export function tooltip(node, text, pos = 'top') {
  node.setAttribute('data-tip', text);
  if (pos === 'bottom') node.setAttribute('data-tip-pos', 'bottom');
  return node;
}
