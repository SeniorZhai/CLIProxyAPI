import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { createContext, runInContext } from 'node:vm';

const source = readFileSync(new URL('./admin.js', import.meta.url), 'utf8');
const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
const API = '/v8/management';
const session = { username: 'admin', csrf_token: 'browser-csrf', base_url: 'https://proxy.example' };
const deviceKey = { id: 'device-1', key: 'cpa_test_device_key' };
const flush = () => new Promise((resolve) => setImmediate(resolve));

class Element {
  constructor(tag = 'button') {
    this.tag = tag;
    this.children = [];
    this.dataset = {};
    this.attributes = new Map();
    this.listeners = new Map();
    this.value = '';
    this.hidden = false;
    this.disabled = false;
    this.open = false;
    this.text = '';
  }
  set textContent(text) { this.text = String(text); this.children = []; }
  get textContent() { return this.text + this.children.map((child) => child.textContent).join(''); }
  get childElementCount() { return this.children.length; }
  append(...children) {
    this.children.push(...children);
    if (this.tag === 'select' && !this.value) this.value = this.children[0]?.value || '';
  }
  replaceChildren(...children) {
    this.text = '';
    this.children = [];
    if (this.tag === 'select') this.value = '';
    this.append(...children);
  }
  setAttribute(name, value) { this.attributes.set(name, value); }
  removeAttribute(name) { this.attributes.delete(name); }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  async emit(type, fields = {}) {
    const event = { currentTarget: this, submitter: new Element(), preventDefault() {}, ...fields };
    await Promise.all((this.listeners.get(type) || []).map((listener) => listener(event)));
    await flush();
  }
  click() { return this.emit('click'); }
  showModal() { this.open = true; }
  close() {
    if (!this.open) return;
    this.open = false;
    for (const listener of this.listeners.get('close') || []) listener();
  }
  reset() { this.value = ''; }
}

async function browser(route = () => undefined, keys = [deviceKey]) {
  const elements = new Map();
  const buttons = [];
  for (const [, tag, attrs] of html.matchAll(/<(\w+)\b([^>]*)>/g)) {
    const id = attrs.match(/\bid="([^"]+)"/)?.[1];
    const data = [...attrs.matchAll(/\bdata-(\w+)="([^"]+)"/g)];
    if (!id && !data.length) continue;
    const node = new Element(tag);
    node.hidden = /\bhidden\b/.test(attrs);
    for (const [, name, value] of data) node.dataset[name] = value;
    if (id) elements.set(id, node);
    if (data.length) buttons.push(node);
  }
  const requests = [];
  const unexpected = [];
  const timers = new Map();
  let timerID = 0;
  const context = createContext({
    URL, URLSearchParams, Headers, FormData, AbortController,
    document: {
      getElementById(id) { assert.ok(elements.has(id), `Missing HTML element: ${id}`); return elements.get(id); },
      createElement: (tag) => new Element(tag),
      querySelectorAll(selector) {
        if (selector === 'dialog[open]') return [...elements.values()].filter((node) => node.tag === 'dialog' && node.open);
        const name = selector.match(/^\[data-(\w+)\]$/)?.[1];
        assert.ok(name, `Unsupported selector: ${selector}`);
        return buttons.filter((button) => name in button.dataset);
      },
    },
    Option: function (text, value) { const option = new Element('option'); option.textContent = text; option.value = value; return option; },
    window: { confirm: () => true },
    navigator: { clipboard: { writeText: async () => {} } },
    setTimeout(callback, delay) { timers.set(++timerID, { callback, delay }); return timerID; },
    clearTimeout(id) { timers.delete(id); },
    async fetch(path, options) {
      const request = { path, ...options };
      requests.push(request);
      let result = await route(request);
      if (result === undefined) {
        const defaults = {
          [`${API}/auth/session`]: session,
          [`${API}/credentials`]: { files: [] },
          [`${API}/client-keys`]: { keys },
          '/v1/models': { data: [{ id: 'example-model' }] },
        };
        if (!(path in defaults)) { unexpected.push(request); throw new Error(`Unexpected request: ${path}`); }
        result = { body: defaults[path] };
      }
      const status = result.status || 200;
      return { ok: status >= 200 && status < 300, status, json: async () => result.body || {} };
    },
  });
  runInContext(source, context, { filename: 'admin.js' });
  await flush();
  return {
    node: (id) => elements.get(id), requests, unexpected, timers,
    accountAction: (label) => elements.get('account-list').children[0].children.at(-1).children.find((button) => button.textContent === label),
    provider: (name) => elements.get('provider-list').children.find((button) => button.textContent.startsWith(name)),
    client: (name) => buttons.find((button) => button.dataset.client === name),
    async tick(delay) {
      const timer = [...timers].find(([, value]) => value.delay === delay);
      assert.ok(timer, `Missing ${delay}ms timer`);
      timers.delete(timer[0]);
      await timer[1].callback();
      await flush();
    },
  };
}

const providerCases = [
  ['antigravity', 'Antigravity', false],
  ['codex', 'ChatGPT Codex', false],
  ['claude', 'Claude Code', false],
  ['xai', 'Grok Build', true],
  ['meta', 'Muse Code', true],
  ['devin', 'Devin', false],
];

for (const [provider, label, device] of providerCases) {
  test(`${provider}: authorize, submit the appropriate flow, and refresh connected accounts`, async () => {
    let complete = false;
    const flowState = `${provider}-state`;
    const app = await browser(({ path }) => {
      if (path === `${API}/oauth/auth-url?provider=${provider}`) return { body: {
        state: flowState, url: `https://auth.example/${provider}`,
        ...(device ? { flow: 'device', user_code: 'ABCD-EFGH' } : {}),
      } };
      if (path === `${API}/oauth/status?state=${flowState}`) return { body: { status: complete ? 'ok' : 'wait' } };
      if (path === `${API}/oauth/callback`) return { body: { status: 'ok' } };
    });
    await app.node('add-account').click();
    assert.equal(app.node('provider-dialog').open, true);
    await app.provider(label).click();
    assert.equal(app.node('provider-dialog').open, false);
    assert.equal(app.node('oauth-dialog').open, true);
    assert.equal(app.node('oauth-link').href, `https://auth.example/${provider}`);
    assert.equal(app.node('callback-form').hidden, device);
    assert.equal(app.node('device-code-area').hidden, !device);
    if (device) {
      assert.equal(app.node('device-code').value, 'ABCD-EFGH');
    } else {
      const redirect = `http://localhost:8765/callback?code=test-code&state=${flowState}`;
      app.node('callback-url').value = `  ${redirect}  `;
      const submitter = new Element();
      await app.node('callback-form').emit('submit', { submitter });
      const callback = app.requests.find((request) => request.path === `${API}/oauth/callback`);
      assert.equal(callback.method, 'POST');
      assert.deepEqual(JSON.parse(callback.body), { provider, redirect_url: redirect });
      assert.equal(app.node('callback-url').value, '');
      assert.equal(submitter.disabled, false);
    }
    complete = true;
    await app.tick(2000);
    assert.match(app.node('oauth-status').textContent, /账号已连接/);
    assert.equal(app.node('callback-form').hidden, true);
    assert.equal(app.node('device-code-area').hidden, true);
    assert.equal(app.node('oauth-link-area').hidden, true);
    assert.equal(app.requests.filter(({ path }) => path === `${API}/credentials`).length, 2);
    assert.equal(app.requests.filter(({ path }) => path === '/v1/models').length, 2);
    assert.equal(app.requests.filter(({ path }) => path === `${API}/oauth/callback`).length, device ? 0 : 1);
    app.node('oauth-dialog').close();
    await flush();
    assert.equal(app.requests.some(({ method }) => method === 'DELETE'), false);
    assert.deepEqual(app.unexpected, []);
  });
}

test('closing an OAuth dialog aborts polling and deletes only its authorization session', async () => {
  const app = await browser(({ path }) => {
    if (path.includes('/oauth/auth-url?')) return { body: { state: 'state /+', url: 'https://auth.example/login' } };
    if (path.includes('/oauth/status?')) return { body: { status: 'wait' } };
    if (path.includes('/oauth/session?')) return { body: {} };
  });
  await app.provider('ChatGPT Codex').click();
  const poll = app.requests.find(({ path }) => path.includes('/oauth/status?'));
  app.node('callback-url').value = 'sensitive callback';
  app.node('oauth-dialog').close();
  await flush();
  assert.equal(poll.signal.aborted, true);
  assert.equal(app.timers.size, 0);
  assert.equal(app.node('callback-url').value, '');
  const deletion = app.requests.find(({ method }) => method === 'DELETE');
  assert.equal(deletion.path, `${API}/oauth/session?state=state+%2F%2B`);
  assert.equal(app.node('app-view').hidden, false);
  assert.deepEqual(app.unexpected, []);
});

test('cancelling before the auth URL arrives disposes of the late session without starting polling', async () => {
  let resolveStart;
  const app = await browser(({ path }) => {
    if (path.includes('/oauth/auth-url?')) return new Promise((resolve) => { resolveStart = resolve; });
    if (path.includes('/oauth/session?')) return { body: {} };
  });
  const starting = app.provider('Claude Code').click();
  await flush();
  app.node('oauth-dialog').close();
  resolveStart({ body: { state: 'late-state', url: 'https://auth.example/login' } });
  await starting;
  assert.equal(app.requests.some(({ path }) => path.includes('/oauth/status?')), false);
  assert.ok(app.requests.some(({ path, method }) => path === `${API}/oauth/session?state=late-state` && method === 'DELETE'));
  assert.equal(app.node('oauth-dialog').open, false);
  assert.equal(app.timers.size, 0);
  assert.deepEqual(app.unexpected, []);
});

test('a late successful poll cannot reconnect an authorization that was cancelled', async () => {
  let resolvePoll;
  const app = await browser(({ path }) => {
    if (path.includes('/oauth/auth-url?')) return { body: { state: 'cancelled-state', url: 'https://auth.example/login' } };
    if (path.includes('/oauth/status?')) return new Promise((resolve) => { resolvePoll = resolve; });
    if (path.includes('/oauth/session?')) return { body: {} };
  });
  await app.provider('ChatGPT Codex').click();
  app.node('oauth-dialog').close();
  resolvePoll({ body: { status: 'ok' } });
  await flush();
  assert.equal(app.node('oauth-dialog').open, false);
  assert.doesNotMatch(app.node('oauth-status').textContent, /账号已连接/);
  assert.equal(app.requests.filter(({ path }) => path === `${API}/credentials`).length, 1);
  assert.equal(app.timers.size, 0);
});

test('transient polling errors retry, while provider errors stop polling', async () => {
  let polls = 0;
  const app = await browser(({ path }) => {
    if (path.includes('/oauth/auth-url?')) return { body: { state: 'error-state', url: 'https://auth.example/login' } };
    if (path.includes('/oauth/status?')) {
      if (++polls === 1) throw new Error('network unavailable');
      return { body: { status: 'error', error: 'access_denied' } };
    }
  });
  await app.provider('ChatGPT Codex').click();
  assert.match(app.node('oauth-status').textContent, /network unavailable/);
  await app.tick(5000);
  assert.match(app.node('oauth-status').textContent, /access_denied/);
  assert.equal(app.timers.size, 0);
  assert.equal(polls, 2);
});

test('failed authorization and callback requests display errors and leave controls usable', async () => {
  let failStart = true;
  const app = await browser(({ path }) => {
    if (path.includes('/oauth/auth-url?')) return failStart
      ? { status: 502, body: { error: 'provider unavailable' } }
      : { body: { state: 'callback-state', url: 'https://auth.example/login' } };
    if (path.includes('/oauth/status?')) return { body: { status: 'wait' } };
    if (path === `${API}/oauth/callback`) return { status: 400, body: { error: 'invalid callback state' } };
  });
  const button = app.provider('ChatGPT Codex');
  await button.click();
  assert.match(app.node('oauth-status').textContent, /provider unavailable/);
  assert.equal(button.disabled, false);
  assert.equal(app.node('oauth-link-area').hidden, true);
  assert.equal(app.timers.size, 0);
  failStart = false;
  await button.click();
  const submitter = new Element();
  app.node('callback-url').value = 'http://localhost/callback?state=wrong';
  await app.node('callback-form').emit('submit', { submitter });
  assert.match(app.node('notice').textContent, /invalid callback state/);
  assert.equal(submitter.disabled, false);
  assert.equal(app.node('callback-url').value, 'http://localhost/callback?state=wrong');
});

test('non-HTTPS authorization URLs are rejected before polling', async () => {
  const app = await browser(({ path }) => {
    if (path.includes('/oauth/auth-url?')) return { body: { state: 'bad-url', url: 'javascript:alert(1)' } };
  });
  await app.provider('ChatGPT Codex').click();
  assert.match(app.node('oauth-status').textContent, /授权地址无效/);
  assert.equal(app.node('oauth-link-area').hidden, true);
  assert.equal(app.requests.some(({ path }) => path.includes('/oauth/status?')), false);
});

test('an expired web session closes dialogs, clears device keys, and stops OAuth polling', async () => {
  const app = await browser(({ path }) => {
    if (path.includes('/oauth/auth-url?')) return { body: { state: 'expired-state', url: 'https://auth.example/login' } };
    if (path.includes('/oauth/status?')) return { status: 401, body: { error: 'session_expired' } };
  });
  assert.match(app.node('client-snippet').textContent, /cpa_test_device_key/);
  await app.provider('ChatGPT Codex').click();
  assert.equal(app.node('login-view').hidden, false);
  assert.equal(app.node('app-view').hidden, true);
  assert.equal(app.node('oauth-dialog').open, false);
  assert.equal(app.node('client-snippet').textContent, '');
  assert.equal(app.node('key-list').childElementCount, 0);
  assert.equal(app.node('connect-key').childElementCount, 0);
  assert.equal(app.node('connect-model').value, '');
  assert.equal(app.node('model-options').childElementCount, 0);
  assert.equal(app.node('model-hint').textContent, '');
  assert.equal(app.requests.find(({ path }) => path.includes('/oauth/status?')).signal.aborted, true);
  assert.equal(app.timers.size, 0);
  assert.equal(app.requests.some(({ method }) => method === 'DELETE'), false);
});

test('management CSRF headers and device authorization headers remain isolated', async () => {
  const app = await browser();
  await app.node('create-key').click();
  for (const request of app.requests) {
    assert.equal(request.credentials, 'same-origin');
    assert.equal(request.cache, 'no-store');
    if (request.path === '/v1/models') {
      assert.equal(request.headers.get('Authorization'), `Bearer ${deviceKey.key}`);
      assert.equal(request.headers.has('X-CSRF-Token'), false);
    } else {
      assert.equal(request.headers.has('Authorization'), false);
      assert.equal(request.headers.get('X-CSRF-Token'), request.path.endsWith('/auth/session') ? null : session.csrf_token);
    }
  }
  const created = app.requests.find(({ method }) => method === 'POST');
  assert.equal(created.path, `${API}/client-keys`);
  assert.equal(created.headers.get('Content-Type'), 'application/json');
  assert.equal(created.body, '{}');
});

test('a device API rejection does not expire the administrator web session', async () => {
  const app = await browser(({ path }) => path === '/v1/models'
    ? { status: 401, body: { error: 'session_expired' } } : undefined);
  assert.equal(app.node('app-view').hidden, false);
  assert.equal(app.node('login-view').hidden, true);
  assert.match(app.node('model-hint').textContent, /模型查询失败/);
  assert.match(app.node('client-snippet').textContent, /cpa_test_device_key/);
});

test('client snippets use the selected device key, correct API conventions, and shell quoting', async () => {
  const app = await browser();
  assert.equal(app.node('api-base').value, 'https://proxy.example/v1');
  const snippet = () => app.node('client-snippet').textContent;
  assert.match(snippet(), /export CPA_API_KEY='cpa_test_device_key'/);
  assert.match(snippet(), /model_providers\.cliproxy\.base_url="https:\/\/proxy\.example\/v1"/);
  assert.match(snippet(), /model_providers\.cliproxy\.wire_api="responses"/);
  assert.match(snippet(), /model_providers\.cliproxy\.requires_openai_auth=false/);
  assert.match(snippet(), /model="example-model"/);
  await app.client('claude').click();
  assert.match(snippet(), /ANTHROPIC_BASE_URL='https:\/\/proxy\.example'/);
  assert.match(snippet(), /ANTHROPIC_AUTH_TOKEN='cpa_test_device_key'/);
  assert.match(snippet(), /ANTHROPIC_MODEL='example-model'/);
  await app.client('gemini').click();
  assert.match(snippet(), /\/v1beta\/models\/example-model:generateContent/);
  assert.match(snippet(), /x-goog-api-key: cpa_test_device_key/);
  await app.client('openai').click();
  assert.match(snippet(), /\/v1\/chat\/completions/);
  assert.match(snippet(), /Authorization: Bearer cpa_test_device_key/);
  assert.match(snippet(), /"stream":true/);
  assert.doesNotMatch(snippet(), /browser-csrf|X-CSRF-Token/);
  app.node('connect-model').value = "vendor/model'$(printf injected)";
  await app.node('connect-model').emit('input');
  assert.ok(snippet().includes("vendor/model'\\''$(printf injected)"));
  await app.client('gemini').click();
  assert.ok(snippet().includes("vendor%2Fmodel'\\''%24(printf%20injected):generateContent"));
});

test('client configuration stays unavailable until a device key exists', async () => {
  const app = await browser(() => undefined, []);
  assert.equal(app.node('copy-snippet').disabled, true);
  assert.match(app.node('client-snippet').textContent, /生成一个 Key/);
  assert.equal(app.requests.some(({ path }) => path === '/v1/models'), false);
});

test('selecting another device key updates authorization and preserves the selection after a reload', async () => {
  const otherKey = { id: 'device-2', key: "cpa_other'$(printf injected)" };
  const app = await browser(() => undefined, [deviceKey, otherKey]);
  app.node('connect-key').value = otherKey.id;
  await app.node('connect-key').emit('change');
  await app.node('create-key').click();
  assert.equal(app.node('connect-key').value, otherKey.id);
  assert.ok(app.node('client-snippet').textContent.includes("export CPA_API_KEY='cpa_other'\\''$(printf injected)'"));
  assert.doesNotMatch(app.node('client-snippet').textContent, /cpa_test_device_key/);
  const modelRequests = app.requests.filter(({ path }) => path === '/v1/models');
  assert.equal(modelRequests.length, 3);
  for (const request of modelRequests.slice(1)) {
    assert.equal(request.headers.get('Authorization'), `Bearer ${otherKey.key}`);
    assert.equal(request.headers.has('X-CSRF-Token'), false);
  }
});

for (const [label, method, path, before, after] of [
  ['删除', 'DELETE', `${API}/credentials?name=account.json&auth_index=account-1`, ['old-model'], []],
  ['暂停', 'PATCH', `${API}/credentials/status`, ['old-model'], []],
  ['启用', 'PATCH', `${API}/credentials/status`, [], ['enabled-model']],
  ['刷新凭据', 'POST', `${API}/credentials/refresh`, ['old-model'], ['refreshed-model']],
]) {
  test(`${label} refreshes available models and reconciles the generated client configuration`, async () => {
    let changed = false;
    const app = await browser((request) => {
      if (request.method === method && request.path === path) {
        changed = true;
        return { body: {} };
      }
      if (request.path === `${API}/credentials`) return { body: { files: changed && label === '删除' ? [] : [{
        name: 'account.json', auth_index: 'account-1', provider: 'codex',
        disabled: label === '启用' ? !changed : label === '暂停' && changed,
      }] } };
      if (request.path === '/v1/models') return { body: { data: (changed ? after : before).map((id) => ({ id })) } };
    });
    assert.equal(app.node('connect-model').value, before[0] || '');
    await app.accountAction(label).click();
    assert.equal(changed, true);
    assert.equal(app.requests.filter(({ path }) => path === '/v1/models').length, 2);
    assert.deepEqual(app.node('model-options').children.map((option) => option.value), after);
    assert.equal(app.node('connect-model').value, after[0] || '');
    assert.ok(app.node('client-snippet').textContent.includes(`model="${after[0] || '<MODEL_ID>'}"`));
    assert.doesNotMatch(app.node('client-snippet').textContent, /old-model/);
    assert.match(app.node('model-hint').textContent, after.length ? /已发现 1 个模型/ : /尚未发现模型/);
    assert.deepEqual(app.unexpected, []);
  });
}

test('manual account reload preserves a listed selection only while that model remains available', async () => {
  let models = ['first-model', 'selected-model'];
  const app = await browser(({ path }) => path === '/v1/models'
    ? { body: { data: models.map((id) => ({ id })) } } : undefined);
  app.node('connect-model').value = 'selected-model';
  await app.node('connect-model').emit('input');
  models = ['replacement-model', 'selected-model'];
  await app.node('reload-accounts').click();
  assert.equal(app.node('connect-model').value, 'selected-model');
  models = ['replacement-model'];
  await app.node('reload-accounts').click();
  assert.equal(app.node('connect-model').value, 'replacement-model');
  assert.doesNotMatch(app.node('client-snippet').textContent, /selected-model/);
  assert.equal(app.requests.filter(({ path }) => path === '/v1/models').length, 3);
});

test('overlapping model reloads preserve the selected model and ignore an older response', async () => {
  const otherKey = { id: 'device-2', key: 'cpa_other_device_key' };
  const pending = [];
  let loading = false;
  const app = await browser(({ path }) => {
    if (path !== '/v1/models') return;
    if (loading) return new Promise((resolve) => pending.push(resolve));
    return { body: { data: [{ id: 'first-model' }, { id: 'chosen-model' }] } };
  }, [deviceKey, otherKey]);
  app.node('connect-model').value = 'chosen-model';
  await app.node('connect-model').emit('input');
  loading = true;
  const reloading = app.node('reload-accounts').click();
  await flush();
  assert.equal(app.node('connect-model').value, '');
  assert.doesNotMatch(app.node('client-snippet').textContent, /chosen-model/);
  app.node('connect-key').value = otherKey.id;
  await app.node('connect-key').emit('change');
  assert.equal(pending.length, 2);
  pending[1]({ body: { data: [{ id: 'new-first-model' }, { id: 'chosen-model' }] } });
  await flush();
  assert.equal(app.node('connect-model').value, 'chosen-model');
  pending[0]({ body: { data: [{ id: 'stale-model' }] } });
  await reloading;
  assert.equal(app.node('connect-model').value, 'chosen-model');
  assert.deepEqual(app.node('model-options').children.map((option) => option.value), ['new-first-model', 'chosen-model']);
  assert.match(app.node('client-snippet').textContent, /model="chosen-model"/);
  assert.match(app.node('client-snippet').textContent, /cpa_other_device_key/);
  assert.doesNotMatch(app.node('client-snippet').textContent, /stale-model|cpa_test_device_key/);
});

test('model reload failures clear automatic suggestions and recover on the next account reload', async () => {
  let fail = false;
  const app = await browser(({ path }) => path === '/v1/models' && fail
    ? { status: 503, body: { error: 'model registry unavailable' } } : undefined);
  fail = true;
  await app.node('reload-accounts').click();
  assert.equal(app.node('model-options').childElementCount, 0);
  assert.equal(app.node('connect-model').value, '');
  assert.match(app.node('model-hint').textContent, /模型查询失败：model registry unavailable/);
  assert.match(app.node('client-snippet').textContent, /model="<MODEL_ID>"/);
  assert.equal(app.node('app-view').hidden, false);
  fail = false;
  await app.node('reload-accounts').click();
  assert.equal(app.node('connect-model').value, 'example-model');
  assert.match(app.node('model-hint').textContent, /已发现 1 个模型/);
});

test('an intentional custom model survives empty model lists and query failures', async () => {
  let fail = false;
  const app = await browser(({ path }) => path === '/v1/models'
    ? fail ? { status: 503, body: { error: 'unavailable' } } : { body: { data: [] } } : undefined);
  app.node('connect-model').value = 'vendor/custom-model';
  await app.node('connect-model').emit('input');
  await app.node('reload-accounts').click();
  assert.equal(app.node('connect-model').value, 'vendor/custom-model');
  fail = true;
  await app.node('reload-accounts').click();
  assert.equal(app.node('connect-model').value, 'vendor/custom-model');
  assert.match(app.node('client-snippet').textContent, /model="vendor\/custom-model"/);
  assert.match(app.node('model-hint').textContent, /模型查询失败/);
});

test('logout discards custom model selection before a new administrator session', async () => {
  const app = await browser(({ path }) => {
    if (path === `${API}/auth/logout`) return { body: {} };
    if (path === `${API}/auth/login`) return { body: session };
  });
  app.node('connect-model').value = 'private/custom-model';
  await app.node('connect-model').emit('input');
  await app.node('logout').click();
  assert.equal(app.node('connect-model').value, '');
  assert.equal(app.node('model-options').childElementCount, 0);
  assert.equal(app.node('model-hint').textContent, '');
  await app.node('login-form').emit('submit');
  assert.equal(app.node('connect-model').value, 'example-model');
  assert.doesNotMatch(app.node('client-snippet').textContent, /private\/custom-model/);
});
