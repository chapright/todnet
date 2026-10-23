// Overlay primitives: one modal, one popover, one toast stack, one banner.
// The audit found four different error surfaces and two modal dialects; this
// module is the only place that creates any of them now.

import { icon } from '../icons.js';
import { t } from '../i18n.js';
import { h, button, iconButton } from './ui.js';

/* ---------------- focus trap ---------------- */

function trapFocus(root) {
  const selector = 'button:not([disabled]), [href], input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])';
  const onKey = (e) => {
    if (e.key !== 'Tab') return;
    const items = Array.from(root.querySelectorAll(selector)).filter((el) => el.offsetParent !== null);
    if (!items.length) return;
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  };
  root.addEventListener('keydown', onKey);
  return () => root.removeEventListener('keydown', onKey);
}

/* ---------------- modal ---------------- */

let openModals = 0;

export function openModal({
  title, sub = '', iconName = '', tone = '', body = null, actions = [],
  wide = false, variant = '', row = false, dismissible = true, initialFocus = null, onClose = null, closeLabel = t('common.close'),
}) {
  const previous = document.activeElement;
  const layer = document.getElementById('layerHost');
  const titleId = `modal-title-${Date.now()}`;

  const scrim = h('div', { class: 'scrim' });
  const modal = h('div', {
    class: 'modal' + (wide ? ' wide' : '') + (variant ? ' ' + variant : ''),
    role: 'dialog',
    'aria-modal': 'true',
    'aria-labelledby': titleId,
  });

  const head = h('div', { class: 'modal-head' });
  if (iconName) head.append(h('div', { class: `modal-icon ${tone}`.trim(), html: icon(iconName, { size: 20 }) }));
  const headText = h('div', { class: 'grow' }, [
    h('h2', { class: 'modal-title', id: titleId, text: title }),
    sub ? h('p', { class: 'modal-sub', text: sub }) : null,
  ]);
  head.append(headText);

  if (dismissible) {
    const close = iconButton({ name: 'x', label: closeLabel, size: 'sm', cls: 'modal-close' });
    close.addEventListener('click', () => closeModal());
    modal.append(close);
  }
  modal.append(head);
  if (body) modal.append(h('div', { class: 'modal-body' }, [body]));

  const actionWrap = h('div', { class: 'modal-actions' + (row ? ' row' : '') });
  for (const a of actions) {
    const b = button({
      label: a.label,
      variant: a.variant || 'secondary',
      size: a.size || 'md',
      loading: a.loading,
      disabled: a.disabled,
      onClick: () => a.onClick && a.onClick(closeModal),
    });
    if (a.primary) b.classList.add('primary');
    actionWrap.append(b);
  }
  if (actions.length) modal.append(actionWrap);

  scrim.append(modal);
  layer.append(scrim);
  openModals++;
  document.documentElement.classList.add('has-modal');

  let closed = false;
  function closeModal() {
    if (closed) return;
    closed = true;
    openModals = Math.max(0, openModals - 1);
    if (!openModals) document.documentElement.classList.remove('has-modal');
    scrim.classList.add('is-leaving');
    modal.classList.add('is-leaving');
    setTimeout(() => {
      scrim.remove();
      if (previous && previous.focus) previous.focus();
      if (onClose) onClose();
    }, 140);
  }

  const untrap = trapFocus(modal);
  const onKey = (e) => {
    if (e.key === 'Escape' && dismissible) { e.stopPropagation(); closeModal(); }
  };
  document.addEventListener('keydown', onKey, true);
  scrim.addEventListener('mousedown', (e) => {
    if (e.target === scrim && dismissible) closeModal();
  });

  const cleanup = () => { untrap(); document.removeEventListener('keydown', onKey, true); };
  const origClose = closeModal;
  closeModal = () => { cleanup(); origClose(); };

  requestAnimationFrame(() => {
    const target = initialFocus || modal.querySelector('.btn.primary, .btn, input, button');
    if (target) target.focus();
  });

  return closeModal;
}

export function confirmDialog({ title, text, confirmLabel, danger = false, onConfirm }) {
  return openModal({
    title,
    sub: text,
    iconName: danger ? 'alert' : 'info',
    tone: danger ? 'danger' : '',
    actions: [
      { label: t('common.cancel'), variant: 'ghost', onClick: (close) => close() },
      {
        label: confirmLabel,
        variant: danger ? 'danger' : 'primary',
        primary: !danger,
        onClick: async (close) => { close(); await onConfirm(); },
      },
    ],
  });
}

/* ---------------- popover ---------------- */

/* openPopover places a small panel next to an anchor.
 *
 * placement is one of:
 *   'top-start' | 'top-end'      above the anchor, aligned to its edge
 *   'bottom-start' | 'bottom-end' below the anchor
 *   'left-start' | 'left-end'    beside the anchor, vertically aligned
 *   'right-start' | 'right-end'  beside the anchor, vertically aligned
 *
 * 'right-start' exists because the rail is only 76px wide: a panel
 * anchored to a rail button and aligned to its start edge opened on top
 * of the Home screen's primary button and the orb.
 */
export function openPopover({ anchor, content, width = 300, placement = 'top-start', label = '', role = 'dialog', onClose = null }) {
  const layer = document.getElementById('layerHost');
  const rect = anchor.getBoundingClientRect();
  const pop = h('div', {
    class: 'popover',
    role,
    'aria-label': label || null,
    style: `width:${width}px`,
  }, [content]);
  layer.append(pop);

  const place = () => {
    const pr = pop.getBoundingClientRect();
    let top;
    let left;
    if (placement.startsWith('right') || placement.startsWith('left')) {
      top = rect.top;
      // A wider gap than the vertical one: the panel sits beside the
      // stage, and it must not clip the corner of the primary button.
      if (placement.startsWith('right')) left = rect.right + 20;
      else left = rect.left - pr.width - 20;
      if (placement.endsWith('end')) top = rect.top + rect.height - pr.height;
    } else {
      top = placement.startsWith('top') ? rect.top - pr.height - 8 : rect.bottom + 8;
      left = rect.left;
      if (placement.endsWith('end')) left = rect.right - pr.width;
      if (placement.endsWith('center')) left = rect.left + rect.width / 2 - pr.width / 2;
    }
    top = Math.max(8, Math.min(top, innerHeight - pr.height - 8));
    left = Math.max(8, Math.min(left, innerWidth - pr.width - 8));
    pop.style.top = `${top}px`;
    pop.style.left = `${left}px`;
    pop.style.setProperty('--origin', placement.startsWith('right') ? 'center left'
      : placement.startsWith('left') ? 'center right'
      : placement.startsWith('top') ? 'bottom left' : 'top left');
  };
  requestAnimationFrame(place);
  window.addEventListener('resize', place);

  let closed = false;
  function close() {
    if (closed) return;
    closed = true;
    window.removeEventListener('resize', place);
    document.removeEventListener('mousedown', outside, true);
    document.removeEventListener('keydown', onKey, true);
    pop.classList.add('is-leaving');
    setTimeout(() => { pop.remove(); if (onClose) onClose(); }, 140);
  }
  const outside = (e) => {
    if (!pop.contains(e.target) && !anchor.contains(e.target)) close();
  };
  const onKey = (e) => {
    if (e.key === 'Escape') { e.stopPropagation(); close(); }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      const items = Array.from(pop.querySelectorAll('button:not([disabled])'));
      if (!items.length) return;
      e.preventDefault();
      const i = items.indexOf(document.activeElement);
      const next = e.key === 'ArrowDown' ? (i + 1) % items.length : (i - 1 + items.length) % items.length;
      items[next].focus();
    }
  };
  setTimeout(() => {
    document.addEventListener('mousedown', outside, true);
    document.addEventListener('keydown', onKey, true);
  }, 0);

  requestAnimationFrame(() => {
    const first = pop.querySelector('button:not([disabled])');
    if (first) first.focus();
  });

  return close;
}

/* ---------------- toast ---------------- */

// The toasts on screen are in the language they were raised in. A change of
// language leaves them behind, and a toast in the old language is laid out
// in the new direction, so they are cleared instead.
export function clearToasts() {
  const host = document.getElementById('toastHost');
  if (host) host.replaceChildren();
}

export function toast(message, { kind = 'ok', timeout = 4000, action = null } = {}) {
  const host = document.getElementById('toastHost');
  if (!host) return;
  const node = h('div', { class: `toast ${kind}`, role: 'status' });
  node.append(h('span', { class: 'toast-icon', html: icon(kind === 'error' ? 'alert' : kind === 'info' ? 'info' : 'check', { size: 17 }) }));
  node.append(h('span', { class: 'toast-text', text: message }));
  if (action) {
    const a = h('button', { class: 'btn ghost sm', type: 'button', text: action.label });
    a.addEventListener('click', () => { action.onClick(); dismiss(); });
    node.append(a);
  }
  const close = iconButton({ name: 'x', label: t('common.dismiss'), size: 'sm', cls: 'toast-close' });
  node.append(close);
  node.append(h('span', { class: 'toast-progress', 'aria-hidden': 'true' }));
  host.append(node);

  let timer = 0;
  const dismiss = () => {
    if (!node.isConnected) return;
    clearTimeout(timer);
    node.classList.add('is-leaving');
    setTimeout(() => node.remove(), 160);
  };
  close.addEventListener('click', dismiss);
  node.addEventListener('mouseenter', () => clearTimeout(timer));
  node.addEventListener('mouseleave', () => { timer = setTimeout(dismiss, 1200); });
  timer = setTimeout(dismiss, timeout);
  return dismiss;
}

/* ---------------- banner ---------------- */

let bannerEl = null;
// The banner is chrome, not part of a view, so a change of language does
// not rebuild it. It keeps what it is showing, so it can be shown again in
// the new language.
let bannerSpec = null;

export function showBanner({ kind = 'error', title = '', text = '', actions = [], details = '', render = null }) {
  hideBanner(true);
  const host = document.getElementById('bannerHost');
  const el = h('div', { class: `banner ${kind}`, role: 'alert' });
  el.append(h('span', { class: 'banner-icon', html: icon(kind === 'warn' ? 'alert' : 'alert', { size: 18 }) }));
  const body = h('div', { class: 'banner-body' });
  if (title) body.append(h('div', { class: 'banner-title', text: title }));
  if (text) body.append(h('div', { class: 'banner-text', text }));
  if (actions.length) {
    const row = h('div', { class: 'banner-actions' });
    for (const a of actions) {
      const b = button({ label: a.label, variant: a.primary ? 'primary' : 'secondary', size: 'sm', onClick: a.onClick });
      row.append(b);
    }
    body.append(row);
  }
  if (details) {
    const d = h('details', { class: 'banner-details' }, [
      h('summary', { class: 't-caption t-3', text: t('error.detailsSummary') }),
      h('div', { class: 'banner-raw mono t-caption', text: details }),
    ]);
    body.append(d);
  }
  el.append(body);
  const close = iconButton({ name: 'x', label: t('common.dismiss'), size: 'sm', cls: 'banner-close' });
  close.addEventListener('click', () => hideBanner());
  el.append(close);
  host.append(el);
  bannerEl = el;
  bannerSpec = render ? { render } : { kind, title, text, actions, details };
  return el;
}

export function hideBanner(instant = false) {
  if (!bannerEl) return;
  const el = bannerEl;
  bannerEl = null;
  bannerSpec = null;
  if (instant) { el.remove(); return; }
  el.classList.add('is-leaving');
  setTimeout(() => el.remove(), 160);
}

// refreshBanner shows the banner again, in the language of the interface
// now. The words of a failure are chosen when it is raised, so without
// this a banner raised in one language stayed in it after a change.
export function refreshBanner() {
  if (!bannerSpec) return;
  const spec = bannerSpec.render ? bannerSpec.render() : bannerSpec;
  showBanner(spec);
}

export function hasBanner() { return !!bannerEl; }
