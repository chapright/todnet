// One sprite (index.html <defs>), one helper. Filled variants are only used
// for active navigation. Decorative SVGs are always aria-hidden.

export function icon(name, { size = 20, cls = '', title = '' } = {}) {
  const label = title ? ` role="img" aria-label="${escapeAttr(title)}"` : ' aria-hidden="true"';
  return `<svg class="icon ${cls}" width="${size}" height="${size}" viewBox="0 0 24 24"${label} focusable="false"><use href="#i-${name}"/></svg>`;
}

export function fox(size = 22, cls = '') {
  return `<svg class="icon ${cls}" width="${size}" height="${size}" viewBox="0 0 32 32" aria-hidden="true" focusable="false"><use href="#i-fox"/></svg>`;
}

export function escapeAttr(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
