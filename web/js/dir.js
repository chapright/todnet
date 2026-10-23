// Writing-direction helpers.
//
// offsetLeft and offsetTop are always measured from the left and the top,
// whatever the direction. Anything that positions a node from the inline
// start — the navigation pill, the thumb of a segmented control — has to
// measure from the other side in a right-to-left layout, or it lands in
// the wrong place.
//
// The direction that matters is the *element's*, not the document's: a
// right-to-left page can hold a left-to-right view. The log view is one,
// and its level filter was placing its thumb as if it were right-to-left
// because the page was.

import { isRTL } from './i18n.js';

export function rtl() { return isRTL(); }

// startOffset returns the distance between the inline start edge of the
// container and the inline start edge of the element, in CSS pixels.
//
// The offset is measured from the container's padding box, which is what
// an absolutely positioned child is placed against: the border is not
// part of it.
export function startOffset(el, container) {
  if (!el || !container) return 0;
  const c = container.getBoundingClientRect();
  const r = el.getBoundingClientRect();
  const cs = getComputedStyle(container);
  if (cs.direction === 'rtl') {
    const border = parseFloat(cs.borderRightWidth) || 0;
    return (c.right - border) - r.right;
  }
  const border = parseFloat(cs.borderLeftWidth) || 0;
  return (r.left - c.left) - border;
}
