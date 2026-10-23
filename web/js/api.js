// Thin client for the local control plane. Every route here already existed
// before the redesign; nothing is renamed or removed.

export class ApiError extends Error {
  constructor(message, { status = 0, code = '' } = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

async function parse(res) {
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      // A successful status with a body that is not JSON is a broken
      // service, not an empty answer: a proxy page, a half-written
      // response, a crash mid-send. Silently treating it as null made the
      // caller act as if the request had succeeded — a malformed sign-in
      // answer closed the sheet as if the account were signed in.
      throw new ApiError(`Request failed (${res.status})`, { status: res.status });
    }
  }
  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || `Request failed (${res.status})`;
    throw new ApiError(msg, { status: res.status, code: (data && data.code) || '' });
  }
  return data;
}

export async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: opts.body ? { 'Content-Type': 'application/json' } : undefined,
    cache: 'no-store',
    ...opts,
  });
  return parse(res);
}

export async function apiText(path, opts = {}) {
  const res = await fetch(path, { cache: 'no-store', ...opts });
  if (!res.ok) throw new ApiError(`Request failed (${res.status})`, { status: res.status });
  return res.text();
}

export const endpoints = {
  state: () => api('/api/state'),
  connect: (body = {}) => api('/api/connect', { method: 'POST', body: JSON.stringify(body) }),
  // Cancel abandons a connection attempt that is still running. It is a
  // separate request because the connect request is still in flight while
  // the attempt is in progress.
  cancel: () => api('/api/cancel', { method: 'POST' }),
  disconnect: () => api('/api/disconnect', { method: 'POST' }),
  openURL: (url) => api('/api/open-url', { method: 'POST', body: JSON.stringify({ url }) }),
  settings: (next) => api('/api/settings', { method: 'POST', body: JSON.stringify(next) }),
  locations: (force) => api('/api/locations' + (force ? '?refresh=1' : '')),
  logs: (since) => api('/api/logs?since=' + encodeURIComponent(since)),
  diagnostics: () => apiText('/api/diagnostics'),
  login: (email, password) => api('/api/login', { method: 'POST', body: JSON.stringify({ email, password }) }),
  verify: (code) => api('/api/verify', { method: 'POST', body: JSON.stringify({ code }) }),
  verifyContinue: () => api('/api/verify/continue', { method: 'POST' }),
  logout: () => api('/api/logout', { method: 'POST' }),
  switchAccount: (email) => api('/api/accounts/switch', { method: 'POST', body: JSON.stringify({ email }) }),
  removeAccount: (email) => api('/api/accounts/remove', { method: 'POST', body: JSON.stringify({ email }) }),
  checkAccounts: () => api('/api/accounts/check', { method: 'POST' }),
  elevate: () => api('/api/elevate', { method: 'POST' }),
  // ready tells the native host that the first frame is painted. The window
  // is created hidden and only shown after this, so launch never flashes an
  // empty frame. It is a no-op when the interface runs in a browser.
  ready: () => api('/api/ready', { method: 'POST' }),
  // windowAction drives the app's own title bar: minimise, maximise,
  // restore, toggle-maximize, drag, close, and resize from an edge.
  windowAction: (action, extra) => api('/api/window', {
    method: 'POST',
    body: JSON.stringify({ action, ...(extra || {}) }),
  }),
  // trayNotice carries the words of the balloon Windows draws when the
  // window is closed to the notification area. The balloon is not part of
  // the page, so the interface has to hand the text over.
  trayNotice: (title, text) => api('/api/tray-notice', { method: 'POST', body: JSON.stringify({ title, text }) }),
};
