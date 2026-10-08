const API = '/v8/management';
const $ = (id) => document.getElementById(id);
const state = { session: null, accounts: [], keys: [], models: [], preferredModel: '', customModel: false, client: 'codex', oauth: null };
const providers = [
  ['codex', 'ChatGPT Codex', 'ChatGPT 账号授权'],
  ['claude', 'Claude Code', 'Claude 账号授权'],
  ['antigravity', 'Antigravity', 'Google 账号授权'],
  ['xai', 'Grok Build', '设备码授权'],
  ['meta', 'Muse Code', 'Meta 设备码授权'],
  ['devin', 'Devin', 'Devin 账号授权'],
  ['kimi', 'Kimi', 'Kimi.com 设备码授权'],
  ['kimi-ai', 'Kimi.ai', 'Kimi.ai 设备码授权'],
];
const errors = {
  invalid_credentials: '账号或密码不正确。',
  login_throttled: '尝试次数过多，请在 30 分钟后重试。',
  session_expired: '网页登录已过期，请重新登录。设备 Key 不受影响。',
  invalid_origin: '访问地址与服务器的 public-url 配置不一致，请使用配置的地址。',
  invalid_csrf: '会话校验失败，请刷新页面后重试。',
  invalid_password: '密码需要 12 至 72 字节，且不能包含换行。',
  state_write_failed: '无法保存管理员状态，请检查服务器数据目录的写入权限。',
  administrator_unavailable: '管理员尚未初始化，请检查服务器启动日志。',
  too_many_sessions: '登录会话数量已达上限，请先退出其他浏览器。',
};
let noticeTimer;
let accountRead = 0;
let keyRead = 0;
let modelRead = 0;

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function notify(message, isError = false) {
  clearTimeout(noticeTimer);
  $('notice').textContent = message;
  $('notice').className = `notice${isError ? ' error' : ''}`;
  $('notice').hidden = false;
  noticeTimer = setTimeout(() => { $('notice').hidden = true; }, isError ? 10000 : 4500);
}

async function request(path, options = {}) {
  const { body, ...rest } = options;
  const headers = new Headers(rest.headers);
  if (state.session && path.startsWith(API)) headers.set('X-CSRF-Token', state.session.csrf_token);
  const multipart = body instanceof FormData;
  if (body !== undefined && !multipart) headers.set('Content-Type', 'application/json');
  const response = await fetch(path, {
    ...rest, headers, credentials: 'same-origin', cache: 'no-store',
    ...(body !== undefined ? { body: multipart ? body : JSON.stringify(body) } : {}),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (path.startsWith(API) && data.error === 'session_expired' && state.session) showLogin();
    const detail = typeof data.error === 'string' ? data.error : data.error?.message;
    throw new Error(errors[detail] || detail || `请求失败 (${response.status})`);
  }
  return data;
}

async function busy(button, action) {
  if (button.disabled) return;
  button.disabled = true;
  button.setAttribute('aria-busy', 'true');
  try { await action(); } catch (error) {
    if (error.name !== 'AbortError') notify(error.message || '请求失败，请重试。', true);
  } finally {
    button.disabled = false;
    button.removeAttribute('aria-busy');
  }
}

function actionButton(label, action, className = 'text-button') {
  const button = element('button', className, label);
  button.type = 'button';
  button.addEventListener('click', () => busy(button, action));
  return button;
}

async function copy(text) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    notify('已复制');
  } catch {
    notify('无法访问剪贴板，请显示或选中文本后手动复制。', true);
  }
}

function empty(title, detail) {
  const node = element('div', 'empty');
  node.append(element('strong', '', title), element('p', '', detail));
  return node;
}

function providerName(key) {
  return providers.find(([id]) => id === key)?.[1] || key || '其他服务';
}

function showLogin() {
  const flow = state.oauth;
  if (flow) { clearTimeout(flow.timer); flow.controller.abort(); }
  state.oauth = null;
  state.session = null;
  state.accounts = [];
  state.keys = [];
  state.models = [];
  state.preferredModel = '';
  state.customModel = false;
  accountRead++; keyRead++; modelRead++;
  for (const dialog of document.querySelectorAll('dialog[open]')) dialog.close();
  $('app-view').hidden = true;
  $('login-view').hidden = false;
  $('account-list').replaceChildren();
  $('key-list').replaceChildren();
  $('client-snippet').textContent = '';
  $('connect-key').replaceChildren();
  $('connect-model').value = '';
  $('model-options').replaceChildren();
  $('model-hint').textContent = '';
  $('callback-url').value = '';
  $('password-form').reset();
  $('password').value = '';
}

async function showApp(session) {
  state.session = session;
  $('login-view').hidden = true;
  $('app-view').hidden = false;
  $('admin-name').textContent = session.username;
  $('api-base').value = `${session.base_url}/v1`;
  $('password').value = '';
  await Promise.all([loadAccounts(), loadKeys()].map((task) => task.catch((error) => notify(error.message, true))));
}

function selectView(view) {
  for (const button of document.querySelectorAll('[data-view]')) {
    if (button.dataset.view === view) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  }
  for (const name of ['accounts', 'keys', 'connect']) $(`${name}-view`).hidden = name !== view;
}

async function loadAccounts() {
  const version = ++accountRead;
  const data = await request(`${API}/credentials`);
  if (version !== accountRead || !state.session) return;
  state.accounts = data.files || [];
  $('account-count').textContent = String(state.accounts.length);
  renderAccounts();
}

async function refreshAccounts() {
  await Promise.all([loadAccounts(), loadModels()]);
}

function renderAccounts() {
  const query = $('account-search').value.trim().toLowerCase();
  const accounts = state.accounts.filter((row) => [row.name, row.email, row.provider, providerName(row.provider)].join(' ').toLowerCase().includes(query));
  const list = $('account-list');
  list.replaceChildren();
  if (!accounts.length) {
    list.append(empty(query ? '没有匹配的账号' : '连接第一个服务账号', query ? '试试其他关键词。' : '点击“添加账号”完成授权，或导入已有的 JSON 凭据。'));
    return;
  }
  for (const row of accounts) {
    const container = element('article', 'account-row');
    const body = element('div', 'row-body');
    const name = row.provider || row.type;
    const paused = row.disabled || row.status === 'disabled';
    const unavailable = row.unavailable || row.status === 'error';
    const badge = element('span', `badge${paused ? ' paused' : unavailable ? ' error' : ''}`, paused ? '已暂停' : unavailable ? '需检查' : '已连接');
    body.append(element('div', 'row-title', row.email || row.label || row.name));
    const detail = element('div', 'row-detail', `${providerName(name)} · `);
    detail.append(badge);
    body.append(detail);
    if (row.status_message) body.append(element('div', 'row-detail', row.status_message));
    const actions = element('div', 'row-actions');
    actions.append(
      actionButton('模型', async () => {
        const result = await request(`${API}/credentials/models?${new URLSearchParams({ name: row.name, auth_index: row.auth_index || '' })}`);
        const models = $('account-models');
        models.replaceChildren();
        for (const model of result.models || []) models.append(element('p', '', model.id || model.name));
        if (!models.childElementCount) models.append(element('p', 'muted', '暂未发现可用模型。'));
        $('models-dialog').showModal();
      }),
      actionButton('刷新凭据', async () => {
        await request(`${API}/credentials/refresh`, { method: 'POST', body: { name: row.name, auth_index: row.auth_index || '' } });
        await refreshAccounts(); notify('凭据已刷新');
      }),
      actionButton(paused ? '启用' : '暂停', async () => {
        await request(`${API}/credentials/status`, { method: 'PATCH', body: { name: row.name, auth_index: row.auth_index || '', disabled: !paused } });
        await refreshAccounts();
      }),
      actionButton('删除', async () => {
        if (!window.confirm(`删除账号 ${row.email || row.name}？使用该账号的请求将无法继续路由到它。`)) return;
        await request(`${API}/credentials?${new URLSearchParams({ name: row.name, auth_index: row.auth_index || '' })}`, { method: 'DELETE' });
        await refreshAccounts(); notify('账号已删除');
      }, 'text-button danger'),
    );
    container.append(element('div', 'provider-mark', providerName(name).slice(0, 1)), body, actions);
    list.append(container);
  }
}

async function loadKeys() {
  const version = ++keyRead;
  const result = await request(`${API}/client-keys`);
  if (version !== keyRead || !state.session) return;
  state.keys = result.keys || [];
  $('key-count').textContent = String(state.keys.length);
  const list = $('key-list');
  list.replaceChildren();
  if (!state.keys.length) list.append(empty('还没有设备 Key', '添加上游账号后，生成 Key 并复制到设备。没有 Key 时模型接口会拒绝访问。'));
  for (const [index, key] of state.keys.entries()) {
    const row = element('article', 'key-row');
    const body = element('div', 'row-body');
    const value = element('div', 'key-value', maskKey(key.key));
    body.append(value, element('div', 'row-detail', `Key ${index + 1} · 长期有效`));
    const actions = element('div', 'row-actions');
    const reveal = actionButton('显示', () => {
      const visible = reveal.textContent === '显示';
      value.textContent = visible ? key.key : maskKey(key.key);
      reveal.textContent = visible ? '隐藏' : '显示';
    });
    actions.append(reveal, actionButton('复制', () => copy(key.key)), actionButton('撤销', async () => {
      if (!window.confirm('撤销这个 Key？使用它的设备将无法继续连接。其他 Key 不受影响。')) return;
      await request(`${API}/client-keys/${key.id}`, { method: 'DELETE' });
      await loadKeys(); notify('Key 已撤销');
    }, 'text-button danger'));
    row.append(body, actions);
    list.append(row);
  }
  const previous = $('connect-key').value;
  $('connect-key').replaceChildren();
  if (!state.keys.length) $('connect-key').append(new Option('请先生成设备 Key', ''));
  for (const [index, key] of state.keys.entries()) $('connect-key').append(new Option(`Key ${index + 1} · ${maskKey(key.key)}`, key.id));
  if (state.keys.some((key) => key.id === previous)) $('connect-key').value = previous;
  renderSnippet();
  await loadModels();
}

function maskKey(key) { return `${key.slice(0, 7)}••••••••${key.slice(-6)}`; }
function selectedKey() { return state.keys.find((key) => key.id === $('connect-key').value)?.key || ''; }

async function loadModels() {
  const version = ++modelRead;
  const key = selectedKey();
  state.models = [];
  if (!state.customModel) $('connect-model').value = '';
  $('model-options').replaceChildren();
  renderSnippet();
  if (!key) { $('model-hint').textContent = '生成设备 Key 后可以查询模型。'; return; }
  $('model-hint').textContent = '正在查询账号可用的模型…';
  try {
    const response = await request('/v1/models', { headers: { Authorization: `Bearer ${key}` } });
    if (version !== modelRead || !state.session) return;
    const models = response.data || [];
    state.models = models.map((model) => model.id);
    for (const model of models) $('model-options').append(new Option(model.id, model.id));
    if (!state.customModel) {
      state.preferredModel = state.models.includes(state.preferredModel) ? state.preferredModel : state.models.find((model) => model === 'gpt-6-astra') || state.models[0] || '';
      $('connect-model').value = state.preferredModel;
    }
    $('model-hint').textContent = models.length ? `已发现 ${models.length} 个模型，也可以手动输入模型 ID。` : '尚未发现模型，请先连接并启用上游账号。';
  } catch (error) {
    if (version !== modelRead) return;
    $('model-hint').textContent = `模型查询失败：${error.message}`;
  }
  renderSnippet();
}

const shellQuote = (value) => `'${String(value).replaceAll("'", "'\\''")}'`;

function renderSnippet() {
  if (!state.session) return;
  const key = selectedKey();
  const base = state.session.base_url;
  const model = $('connect-model').value.trim() || '<MODEL_ID>';
  $('codex-config').hidden = state.client !== 'codex' || !key;
  $('copy-snippet').disabled = !key;
  if (!key) { $('client-snippet').textContent = '先在“设备 Key”页面生成一个 Key。'; return; }
  let snippet;
  if (state.client === 'codex') {
    const settings = [
      'model_provider="cliproxy"', `model=${JSON.stringify(model)}`,
      'model_reasoning_effort="medium"',
      'model_providers.cliproxy.name="CLIProxyAPI"',
      `model_providers.cliproxy.base_url=${JSON.stringify(`${base}/v1`)}`,
      'model_providers.cliproxy.env_key="CPA_API_KEY"',
      'model_providers.cliproxy.wire_api="responses"',
      'model_providers.cliproxy.requires_openai_auth=false',
    ];
    snippet = `export CPA_API_KEY=${shellQuote(key)}\ncodex \\\n${settings.map((setting) => `  -c ${shellQuote(setting)}`).join(' \\\n')}`;
    $('codex-config-snippet').textContent = settings.join('\n');
    $('snippet-help').textContent = '安装 Codex CLI 后，在 macOS / Linux 的 Bash 或 Zsh 终端运行下方命令，即可使用所选设备 Key 和模型，推理强度为 medium。此命令仅对本次启动生效；长期使用可展开下方“保存为默认配置”。';
  } else if (state.client === 'claude') {
    snippet = `export ANTHROPIC_BASE_URL=${shellQuote(base)}\nexport ANTHROPIC_AUTH_TOKEN=${shellQuote(key)}\nexport ANTHROPIC_MODEL=${shellQuote(model)}\nclaude`;
    $('snippet-help').textContent = '在运行 Claude Code 的终端设置以下环境变量。';
  } else if (state.client === 'gemini') {
    snippet = `curl ${shellQuote(`${base}/v1beta/models/${encodeURIComponent(model)}:generateContent`)} \\\n  -H ${shellQuote(`x-goog-api-key: ${key}`)} \\\n  -H 'Content-Type: application/json' \\\n  -d ${shellQuote(JSON.stringify({ contents: [{ parts: [{ text: 'Hello' }] }] }))}`;
    $('snippet-help').textContent = 'Gemini 兼容接口示例。客户端配置相同的服务器地址和设备 Key。';
  } else {
    snippet = `curl ${shellQuote(`${base}/v1/chat/completions`)} \\\n  -H ${shellQuote(`Authorization: Bearer ${key}`)} \\\n  -H 'Content-Type: application/json' \\\n  -d ${shellQuote(JSON.stringify({ model, messages: [{ role: 'user', content: 'Hello' }], stream: true }))}`;
    $('snippet-help').textContent = '在 OpenAI 兼容客户端中填写上方 Base URL 和设备 Key。以下命令可验证流式调用。';
  }
  $('client-snippet').textContent = snippet;
}

async function cancelOAuth() {
  const flow = state.oauth;
  if (!flow) return;
  state.oauth = null;
  clearTimeout(flow.timer);
  flow.controller.abort();
  if (flow.state && state.session) {
    await request(`${API}/oauth/session?${new URLSearchParams({ state: flow.state })}`, { method: 'DELETE' }).catch(() => {});
  }
}

async function startOAuth(provider) {
  await cancelOAuth();
  $('provider-dialog').close();
  $('oauth-title').textContent = `连接 ${providerName(provider)}`;
  $('oauth-status').textContent = '正在创建授权请求…';
  $('oauth-link-area').hidden = true;
  $('device-code-area').hidden = true;
  $('callback-form').hidden = true;
  $('callback-url').value = '';
  $('oauth-dialog').showModal();
  const flow = { provider, state: '', controller: new AbortController(), timer: null };
  state.oauth = flow;
  try {
    const result = await request(`${API}/oauth/auth-url?${new URLSearchParams({ provider })}`);
    if (state.oauth !== flow) {
      if (result.state && state.session) await request(`${API}/oauth/session?${new URLSearchParams({ state: result.state })}`, { method: 'DELETE' }).catch(() => {});
      return;
    }
    flow.state = result.state;
    if (!flow.state) throw new Error('授权会话无效，请关闭窗口后重试。');
    const target = new URL(result.url);
    if (target.protocol !== 'https:') throw new Error('授权地址无效，请检查提供商配置。');
    $('oauth-link').href = target.href;
    $('oauth-link-area').hidden = false;
    $('oauth-status').textContent = '在授权页面完成登录后，这里会自动更新状态。';
    if (result.flow === 'device') {
      $('device-code').value = result.user_code || '';
      $('device-code-area').hidden = !result.user_code;
    } else {
      $('callback-form').hidden = false;
    }
    pollOAuth(flow);
  } catch (error) {
    if (state.oauth === flow) $('oauth-status').textContent = `授权启动失败：${error.message}`;
  }
}

async function pollOAuth(flow) {
  if (state.oauth !== flow || !flow.state) return;
  try {
    const result = await request(`${API}/oauth/status?${new URLSearchParams({ state: flow.state })}`, { signal: flow.controller.signal });
    if (state.oauth !== flow) return;
    if (result.status === 'ok') {
      state.oauth = null;
      $('oauth-status').textContent = '账号已连接，可以关闭此窗口。';
      $('callback-form').hidden = true;
      $('oauth-link-area').hidden = true;
      $('device-code-area').hidden = true;
      await refreshAccounts();
      notify('账号已连接');
    } else if (result.status === 'error') {
      $('oauth-status').textContent = `授权未完成：${result.error || '请关闭窗口后重试。'}`;
    } else {
      flow.timer = setTimeout(() => pollOAuth(flow), 2000);
    }
  } catch (error) {
    if (error.name !== 'AbortError' && state.oauth === flow) {
      $('oauth-status').textContent = `状态查询失败，正在重试：${error.message}`;
      flow.timer = setTimeout(() => pollOAuth(flow), 5000);
    }
  }
}

$('login-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const button = event.submitter;
  $('login-error').textContent = '';
  await busy(button, async () => {
    try {
      const session = await request(`${API}/auth/login`, { method: 'POST', body: { username: $('username').value.trim(), password: $('password').value } });
      await showApp(session);
    } catch (error) { $('login-error').textContent = error.message; }
  });
});

for (const button of document.querySelectorAll('[data-view]')) button.addEventListener('click', () => selectView(button.dataset.view));
for (const button of document.querySelectorAll('[data-close]')) button.addEventListener('click', () => $(button.dataset.close).close());
for (const button of document.querySelectorAll('[data-client]')) button.addEventListener('click', () => {
  state.client = button.dataset.client;
  for (const tab of document.querySelectorAll('[data-client]')) tab.setAttribute('aria-pressed', String(tab === button));
  renderSnippet();
});
for (const [id, name, detail] of providers) {
  const button = actionButton(name, () => startOAuth(id), 'provider-button');
  button.append(element('small', '', detail));
  $('provider-list').append(button);
}

$('add-account').addEventListener('click', () => $('provider-dialog').showModal());
$('account-search').addEventListener('input', renderAccounts);
$('reload-accounts').addEventListener('click', (event) => busy(event.currentTarget, refreshAccounts));
$('import-account').addEventListener('click', () => $('auth-file').click());
$('auth-file').addEventListener('change', async () => {
  const files = Array.from($('auth-file').files || []);
  await busy($('import-account'), async () => {
    try {
      for (const file of files) {
        const body = new FormData(); body.append('file', file);
        await request(`${API}/credentials`, { method: 'POST', body });
      }
      if (files.length) notify(`已导入 ${files.length} 个凭据文件`);
    } finally {
      await refreshAccounts();
    }
  });
  $('auth-file').value = '';
});
$('create-key').addEventListener('click', (event) => busy(event.currentTarget, async () => {
  await request(`${API}/client-keys`, { method: 'POST', body: {} });
  await loadKeys(); notify('已生成 Key，可复制到设备使用');
}));
$('connect-key').addEventListener('change', () => { renderSnippet(); loadModels(); });
$('connect-model').addEventListener('input', () => {
  const model = $('connect-model').value.trim();
  state.preferredModel = model;
  state.customModel = Boolean(model) && !state.models.includes(model);
  renderSnippet();
});
$('copy-base').addEventListener('click', () => copy($('api-base').value));
$('copy-snippet').addEventListener('click', () => copy($('client-snippet').textContent));
$('copy-oauth-url').addEventListener('click', () => copy($('oauth-link').href));
$('copy-device-code').addEventListener('click', () => copy($('device-code').value));
$('oauth-dialog').addEventListener('close', () => { cancelOAuth(); $('callback-url').value = ''; });
$('callback-form').addEventListener('submit', (event) => {
  event.preventDefault();
  const flow = state.oauth;
  if (!flow) return;
  busy(event.submitter, async () => {
    await request(`${API}/oauth/callback`, { method: 'POST', body: { provider: flow.provider, redirect_url: $('callback-url').value.trim() } });
    $('callback-url').value = '';
    notify('回调已提交，正在完成授权');
  });
});
$('change-password').addEventListener('click', () => { $('password-form').reset(); $('password-dialog').showModal(); });
$('password-form').addEventListener('submit', (event) => {
  event.preventDefault();
  busy(event.submitter, async () => {
    await request(`${API}/auth/password`, { method: 'PUT', body: { current_password: $('current-password').value, new_password: $('new-password').value } });
    showLogin(); notify('密码已更新，请重新登录。设备 Key 保持有效。');
  });
});
$('logout').addEventListener('click', (event) => busy(event.currentTarget, async () => {
  await cancelOAuth();
  await request(`${API}/auth/logout`, { method: 'POST', body: {} });
  showLogin();
}));

request(`${API}/auth/session`).then(showApp).catch(() => {});
