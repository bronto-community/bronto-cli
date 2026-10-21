// Drives the test browser's login form over the Chrome DevTools Protocol.
// Started by scripts/test-browser-login, which reads the password from the
// OS keychain and passes it in BRONTO_TEST_LOGIN_PASSWORD. No dependencies:
// Node 22+ has fetch and WebSocket built in.
const port = process.env.BRONTO_TEST_CDP_PORT || '9222';
const region = process.env.BRONTO_IT_REGION || 'eu';
const email = process.env.BRONTO_TEST_LOGIN_EMAIL;
const password = process.env.BRONTO_TEST_LOGIN_PASSWORD;
delete process.env.BRONTO_TEST_LOGIN_PASSWORD;

const appURL = `https://app.${region}.bronto.io/`;
const fail = (msg, code = 1) => {
  console.error(`test-browser-login: ${msg}`);
  process.exit(code);
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
let page = targets.find((t) => t.type === 'page' && t.url.includes('.bronto.io'));
if (!page) {
  page = await (await fetch(`http://127.0.0.1:${port}/json/new?${appURL}`, { method: 'PUT' })).json();
}

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  ws.onopen = resolve;
  ws.onerror = () => reject(new Error('cannot connect to the test browser'));
});
let nextID = 0;
const pending = new Map();
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data);
  if (pending.has(msg.id)) {
    pending.get(msg.id)(msg);
    pending.delete(msg.id);
  }
};
const send = (method, params = {}) =>
  new Promise((resolve) => {
    const id = ++nextID;
    pending.set(id, resolve);
    ws.send(JSON.stringify({ id, method, params }));
  });
const evaluate = async (expression) =>
  (await send('Runtime.evaluate', { expression, returnByValue: true })).result?.result?.value;
const currentURL = () => evaluate('location.href');
const onLogin = async () => new URL(await currentURL()).pathname.startsWith('/login');

await send('Page.navigate', { url: appURL });
for (let i = 0; i < 20 && (await evaluate('document.readyState')) !== 'complete'; i++) await sleep(250);
await sleep(1500); // the app redirects to /login client-side

if (!(await onLogin())) {
  console.log('test-browser-login: already logged in.');
  ws.close();
  process.exit(0);
}

for (let i = 0; i < 40 && !(await evaluate('!!document.querySelector("#email")')); i++) await sleep(250);

// Fill the form the way a React app expects (native setter + input event),
// passing the credentials as call arguments rather than inside source text.
const doc = await send('Runtime.evaluate', { expression: 'document' });
const res = await send('Runtime.callFunctionOn', {
  objectId: doc.result.result.objectId,
  functionDeclaration: `function (email, password) {
    const set = (el, v) => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, v);
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    };
    const e = this.querySelector('#email'), p = this.querySelector('#password');
    if (!e || !p) return 'form not found';
    set(e, email);
    set(p, password);
    const form = p.closest('form');
    if (!form) return 'form not found';
    form.requestSubmit();
    return 'submitted';
  }`,
  arguments: [{ value: email }, { value: password }],
  returnByValue: true,
});
const outcome = res.result?.result?.value;
if (outcome !== 'submitted') fail(`couldn't submit the login form (${outcome}); has the login page changed?`);

for (let i = 0; i < 60; i++) {
  await sleep(500);
  if (!(await onLogin())) {
    console.log(`test-browser-login: logged in as ${email}.`);
    ws.close();
    process.exit(0);
  }
}
const message = await evaluate(
  `[...document.querySelectorAll('[role=alert], .error, [class*=error]')].map(e => e.innerText.trim()).filter(Boolean).join(' | ')`,
);
fail(`still on the login page after 30s${message ? `: ${message}` : ''}. Wrong password, or a 2FA step?`);
