// Sign-in and the two-step verification.
//
// The "Create an account" and "Forgot password?" links leave the app: the
// interface is a local page with no outbound navigation of its own, and
// signing up for a Firefox account or resetting a password needs the real
// browser with the user's real profile. onOpenURL hands the URL to the
// control plane, which opens it in the system browser.

import { t, has } from '../i18n.js';
import { icon } from '../icons.js';
import { h, button, iconButton } from '../components/ui.js';
import { openModal } from '../components/overlay.js';

const SIGNUP_URL = 'https://accounts.firefox.com/signup';
// FxA serves the password-reset form at /reset_password. The old /reset
// URL is not a route and landed on Mozilla's "Page not found" screen.
const RESET_URL = 'https://accounts.firefox.com/reset_password';

// The failure codes the control server sends, and the key that says each
// one in the interface's own language.
const ERROR_KEYS = {
  'invalid-credentials': 'login.error.invalid-credentials',
  'account-locked': 'login.error.account-locked',
  'too-many-attempts': 'login.error.too-many-attempts',
  'needs-confirmation': 'login.error.needs-confirmation',
  'anti-bot': 'login.error.anti-bot',
  'invalid-email': 'login.error.invalid-email',
  'wrong-code': 'login.error.wrong-code',
  'generic': 'login.error.generic',
};

// The server names a failure with a code, so the message can be in the
// language the user reads. A code it does not know falls back to the
// server's own words. A failure with no code at all — a gateway page, a
// dropped connection, a body that is not JSON — has no words worth
// showing: it used to reach the sheet as "Request failed (502)".
function errorText(err) {
  const key = err && err.code ? ERROR_KEYS[err.code] : '';
  if (key && has(key)) return t(key);
  if (err && err.code) return (err && err.message) || t('login.error.generic');
  return t('login.error.generic');
}

// A shape a person can type. The browser's own check is off, because the
// form is novalidate, and the server would only answer with Mozilla's
// complaint about the request body.
function looksLikeEmail(value) {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value);
}

export function openLogin({ add = false, onOpenURL, endpoints }) {
  const emailInput = h('input', {
    class: 'input',
    type: 'email',
    id: 'loginEmail',
    autocomplete: 'username',
    placeholder: t('login.emailPlaceholder'),
    'aria-label': t('login.email'),
  });
  const pwInput = h('input', {
    class: 'input',
    type: 'password',
    id: 'loginPassword',
    autocomplete: 'current-password',
    placeholder: t('login.passwordPlaceholder'),
    'aria-label': t('login.password'),
  });
  const pwToggle = iconButton({
    name: 'eye',
    // The button says what it does. It was called "Password", the same
    // word as the field it belongs to, so a screen reader could not tell
    // what pressing it would do.
    label: t('login.showPassword'),
    size: 'sm',
    cls: 'pw-toggle',
  });
  // The state is there to be heard: the button is pressed while the
  // password is on the screen.
  pwToggle.setAttribute('aria-pressed', 'false');
  const setPwVisible = (visible) => {
    pwInput.type = visible ? 'text' : 'password';
    pwToggle.innerHTML = icon(visible ? 'eye-off' : 'eye', { size: 15 });
    const label = visible ? t('login.hidePassword') : t('login.showPassword');
    pwToggle.setAttribute('aria-label', label);
    pwToggle.setAttribute('aria-pressed', visible ? 'true' : 'false');
    pwToggle.title = label;
  };
  pwToggle.addEventListener('click', () => {
    setPwVisible(pwInput.type === 'password');
  });

  const errLine = h('div', { class: 'form-error', role: 'alert', hidden: true });
  const form = h('form', { class: 'login-form', novalidate: true }, [
    h('label', { class: 'field' }, [h('span', { class: 'field-label', text: t('login.email') }), emailInput]),
    h('label', { class: 'field' }, [
      h('span', { class: 'field-label', text: t('login.password') }),
      h('div', { class: 'pw-wrap' }, [pwInput, pwToggle]),
    ]),
    errLine,
  ]);

  const submit = button({ label: t('login.submit'), variant: 'primary', size: 'lg', type: 'submit', cls: 'is-fixed' });
  form.append(h('div', { class: 'login-row' }, [submit]));

  // The two links that used to be missing entirely. A user who forgot their
  // Firefox password had no way forward inside the app.
  const forgotLink = h('button', { class: 'link', type: 'button', text: t('login.forgot') });
  forgotLink.addEventListener('click', () => onOpenURL(RESET_URL));
  const createLink = h('button', { class: 'link', type: 'button', text: t('login.create') });
  createLink.addEventListener('click', () => onOpenURL(SIGNUP_URL));

  form.append(h('div', { class: 'login-links' }, [forgotLink, createLink]));

  const close = openModal({
    title: add ? t('login.addTitle') : t('login.title'),
    sub: add ? t('login.addSub') : '',
    iconName: 'fox',
    body: form,
    initialFocus: emailInput,
    actions: [],
  });

  // The submit button lives inside the form, so the modal action row stays empty.
  let submitting = false;
  // The error line goes away as soon as the user starts fixing the form. It
  // used to sit there saying both fields were empty while the email field
  // held a letter.
  const clearError = () => {
    if (!errLine.hidden) errLine.hidden = true;
  };
  emailInput.addEventListener('input', clearError);
  pwInput.addEventListener('input', clearError);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (submitting) return;
    const email = emailInput.value.trim();
    const password = pwInput.value;
    if (!email || !password) {
      errLine.hidden = false;
      errLine.textContent = t('login.missing');
      return;
    }
    if (!looksLikeEmail(email)) {
      // The address is checked here, so a mistyped one is caught before it
      // reaches Mozilla, which would answer with a complaint about the
      // request body rather than with anything a person can act on.
      errLine.hidden = false;
      errLine.textContent = t('login.error.invalid-email');
      return;
    }
    submitting = true;
    submit.classList.add('loading');
    submit.disabled = true;
    errLine.hidden = true;
    try {
      const res = await endpoints.login(email, password);
      // The server says an account needs a code with `needsVerification`.
      // The names that were checked here were none of them, so the prompt
      // never opened: the sheet closed as if the sign-in had worked and the
      // user was left signed out with nothing said.
      if (res && res.needsVerification) {
        close();
        openVerify({ email, add, onOpenURL, endpoints });
        return;
      }
      close();
      return res;
    } catch (err) {
      errLine.hidden = false;
      errLine.textContent = errorText(err);
      return null;
    } finally {
      submitting = false;
      submit.classList.remove('loading');
      submit.disabled = false;
    }
  });

  return close;
}

export function openVerify({ email, add, onOpenURL, endpoints }) {
  const boxes = [];
  const row = h('div', { class: 'code-boxes' });
  for (let i = 0; i < 6; i++) {
    const box = h('input', {
      type: 'text',
      inputmode: 'numeric',
      maxlength: '1',
      'aria-label': t('login.code'),
      autocomplete: i === 0 ? 'one-time-code' : 'off',
    });
    box.addEventListener('input', () => {
      box.value = box.value.replace(/\D/g, '').slice(0, 1);
      if (box.value && i < 5) boxes[i + 1].focus();
    });
    box.addEventListener('keydown', (e) => {
      if (e.key === 'Backspace' && !box.value && i > 0) boxes[i - 1].focus();
      if (e.key === 'ArrowLeft' && i > 0) boxes[i - 1].focus();
      if (e.key === 'ArrowRight' && i < 5) boxes[i + 1].focus();
    });
    box.addEventListener('paste', (e) => {
      const text = (e.clipboardData || window.clipboardData).getData('text').replace(/\D/g, '');
      if (!text) return;
      e.preventDefault();
      text.slice(0, 6).split('').forEach((ch, idx) => { if (boxes[idx]) boxes[idx].value = ch; });
      boxes[Math.min(text.length, 5)].focus();
    });
    boxes.push(box);
    row.append(box);
  }

  const errLine = h('div', { class: 'form-error', role: 'alert', hidden: true });
  const body = h('div', { class: 'login-form' }, [
    h('p', { class: 'form-hint', text: t('login.codeHint', { email }) }),
    row,
    errLine,
  ]);

  const submit = button({
    label: t('login.confirm'),
    variant: 'primary',
    size: 'lg',
    cls: 'is-fixed',
    onClick: async () => {
      const code = boxes.map((b) => b.value).join('');
      if (code.length !== 6) { boxes[0].focus(); return; }
      submit.classList.add('loading');
      submit.disabled = true;
      try {
        await endpoints.verify(code);
        close();
      } catch (err) {
        errLine.hidden = false;
        errLine.textContent = errorText(err);
        boxes.forEach((b) => { b.value = ''; });
        boxes[0].focus();
      } finally {
        submit.classList.remove('loading');
        submit.disabled = false;
      }
    },
  });

  const linkBtn = button({
    label: t('login.linkContinue'),
    variant: 'ghost',
    size: 'sm',
    onClick: async () => {
      linkBtn.classList.add('loading');
      try {
        await endpoints.verifyContinue();
        close();
      } catch (err) {
        errLine.hidden = false;
        errLine.textContent = errorText(err);
      } finally {
        linkBtn.classList.remove('loading');
      }
    },
  });

  const close = openModal({
    title: t('login.codeTitle'),
    sub: t('login.codeSub'),
    iconName: 'shield-check',
    body,
    initialFocus: boxes[0],
    actions: [
      { label: t('login.linkContinue'), variant: 'ghost', onClick: () => linkBtn.click() },
      { label: t('login.confirm'), variant: 'primary', primary: true, onClick: () => submit.click() },
    ],
  });
  requestAnimationFrame(() => boxes[0].focus());
  return close;
}
