// Motion engine. Every animation in the app is either a CSS transition or
// one of these helpers. Nothing here runs when the window is hidden, and all
// of it collapses to an instant state change under prefers-reduced-motion.

export const DUR = { 1: 80, 2: 140, 3: 220, 4: 360, 5: 700 };

export function reducedMotion() {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

export function easeOut(t) { return 1 - Math.pow(1 - t, 3); }

/** Pause all continuous CSS animation while hidden or unfocused. */
export function watchVisibility(callback) {
  let visible = !document.hidden && document.hasFocus();
  const emit = () => {
    const next = !document.hidden && document.hasFocus();
    if (next === visible) return;
    visible = next;
    document.body.classList.toggle('motion-paused', !visible);
    callback(visible);
  };
  document.addEventListener('visibilitychange', emit);
  window.addEventListener('focus', emit);
  window.addEventListener('blur', emit);
  document.body.classList.toggle('motion-paused', !visible);
  callback(visible);
  return emit;
}

/** Number odometer. Falls back to the final value under reduced motion. */
export function tween(from, to, onUpdate, duration = 600) {
  const start = Number(from) || 0;
  const end = Number(to) || 0;
  if (reducedMotion() || start === end || document.hidden) {
    onUpdate(end);
    return () => {};
  }
  const t0 = performance.now();
  let raf = 0;
  const step = (now) => {
    const p = Math.min(1, (now - t0) / duration);
    onUpdate(start + (end - start) * easeOut(p));
    if (p < 1) raf = requestAnimationFrame(step);
  };
  raf = requestAnimationFrame(step);
  return () => cancelAnimationFrame(raf);
}

/** FLIP for list reordering/filtering. */
export function flip(container, mutate) {
  if (!container || reducedMotion()) { mutate(); return; }
  const first = new Map();
  for (const el of container.querySelectorAll('[data-flip]')) {
    first.set(el.dataset.flip, el.getBoundingClientRect());
  }
  mutate();
  for (const el of container.querySelectorAll('[data-flip]')) {
    const a = first.get(el.dataset.flip);
    if (!a) continue;
    const b = el.getBoundingClientRect();
    const dx = a.left - b.left;
    const dy = a.top - b.top;
    if (!dx && !dy) continue;
    el.animate(
      [{ transform: `translate(${dx}px, ${dy}px)` }, { transform: 'none' }],
      { duration: 260, easing: 'cubic-bezier(.22,1,.36,1)' },
    );
  }
}

/** Staggered entrance, capped so a long list never takes longer than 300 ms. */
export function stagger(nodes, step = 30, cap = 300) {
  if (reducedMotion()) return;
  const list = Array.from(nodes);
  list.forEach((el, i) => {
    el.style.animationDelay = `${Math.min(i * step, cap)}ms`;
  });
}

export function springIn(el, cls = 'is-springing') {
  if (!el || reducedMotion()) return;
  el.classList.remove(cls);
  void el.offsetWidth;
  el.classList.add(cls);
}

/** Cursor spotlight for cards. rAF-throttled, CSS reads --mx/--my. */
export function enableSpotlight(root = document) {
  let queued = false;
  let target = null;
  let x = 0;
  let y = 0;
  root.addEventListener('pointermove', (e) => {
    const card = e.target.closest && e.target.closest('.spotlight');
    if (!card) return;
    target = card;
    const r = card.getBoundingClientRect();
    x = e.clientX - r.left;
    y = e.clientY - r.top;
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      if (target) {
        target.style.setProperty('--mx', `${x}px`);
        target.style.setProperty('--my', `${y}px`);
      }
    });
  }, { passive: true });
}

/** View Transitions wrapper with a no-op fallback.
 *
 * The callback is told whether a view transition is running. It is: the
 * view's own entrance animation has to stand down then, or the new view
 * is drawn twice — once as the transition's snapshot and once live — and
 * the old view shows through the gap.
 */
export function viewTransition(fn, { direction = 'forward' } = {}) {
  if (reducedMotion() || !document.startViewTransition) { fn({ viewTransition: false }); return; }
  document.documentElement.dataset.navDirection = direction;
  document.startViewTransition(() => fn({ viewTransition: true }));
}

/** Circular theme reveal from the control that was clicked. */
export function themeReveal(x, y, fn) {
  if (reducedMotion() || !document.startViewTransition) { fn(); return; }
  const transition = document.startViewTransition(fn);
  transition.ready.then(() => {
    const r = Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y));
    document.documentElement.animate(
      { clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${r}px at ${x}px ${y}px)`] },
      { duration: 420, easing: 'cubic-bezier(.22,1,.36,1)', pseudoElement: '::view-transition-new(root)' },
    );
  }).catch(() => {});
}

/** Announce once per state transition, never on every poll (A-02). */
export function announce(text) {
  const host = document.getElementById('liveStatus');
  if (!host) return;
  host.textContent = '';
  requestAnimationFrame(() => { host.textContent = text; });
}
