import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { createContext, runInContext } from 'node:vm';

const source = readFileSync(new URL('./theme.js', import.meta.url), 'utf8');
const storageKey = 'cpa-admin-theme';

function eventTarget(fields = {}) {
  const listeners = new Map();
  return {
    ...fields,
    addEventListener(type, listener) {
      if (!listeners.has(type)) listeners.set(type, []);
      listeners.get(type).push(listener);
    },
    emit(type, fields = {}) {
      const event = { type, target: this, currentTarget: this, ...fields };
      for (const listener of listeners.get(type) || []) listener.call(this, event);
    },
  };
}

function browser({ stored = null, dark = false, readError = false, writeError = false } = {}) {
  const storage = new Map(stored === null ? [] : [[storageKey, stored]]);
  const element = (tagName) => ({ tagName, children: [], append(child) { this.children.push(child); } });
  const selectors = [eventTarget({ ...element('select'), value: '' }), eventTarget({ ...element('select'), value: '' })];
  const media = eventTarget({ matches: dark, media: '(prefers-color-scheme: dark)' });
  const document = eventTarget({
    readyState: 'loading',
    documentElement: { dataset: {} },
    createElement: element,
    querySelectorAll(selector) {
      assert.equal(selector, '[data-theme-select]');
      return this.readyState === 'loading' ? [] : selectors;
    },
  });
  const localStorage = {
    getItem(key) {
      if (readError) throw new Error('Storage access denied');
      return storage.get(key) ?? null;
    },
    setItem(key, value) {
      if (writeError) throw new Error('Storage quota exceeded');
      storage.set(key, String(value));
    },
  };
  const window = eventTarget({
    document,
    localStorage,
    matchMedia(query) {
      assert.equal(query, '(prefers-color-scheme: dark)');
      return media;
    },
  });
  runInContext(source, createContext({ window, document, localStorage, matchMedia: window.matchMedia }), { filename: 'theme.js' });
  return {
    selectors,
    storage,
    get theme() { return document.documentElement.dataset.theme; },
    get layout() { return document.documentElement.dataset.themeLayout; },
    get mode() { return document.documentElement.dataset.themeMode; },
    ready() {
      document.readyState = 'interactive';
      document.emit('DOMContentLoaded');
    },
    choose(index, preference) {
      selectors[index].value = preference;
      selectors[index].emit('change');
    },
    systemDark(value) {
      media.matches = value;
      media.emit('change', { matches: value });
    },
    storageEvent(key, newValue) {
      if (key === null) storage.clear();
      else if (newValue === null) storage.delete(key);
      else storage.set(key, newValue);
      window.emit('storage', { key, newValue, storageArea: localStorage });
    },
  };
}

function assertPreference(app, preference, theme) {
  assert.equal(app.theme, theme);
  assert.deepEqual(app.selectors.map((selector) => selector.value), [preference, preference]);
}

test('saved skins and system appearance apply before DOMContentLoaded', () => {
  for (const dark of [false, true]) {
    for (const stored of [null, 'system', 'jade', 'graphite', 'paper', 'night']) {
      const app = browser({ stored, dark });
      const theme = stored && stored !== 'system' ? stored : dark ? 'graphite' : 'jade';
      assert.equal(app.theme, theme, `stored=${stored}, dark=${dark}`);
      app.ready();
      assertPreference(app, stored || 'system', theme);
    }
  }
});

test('system preference follows OS changes while a chosen skin stays fixed', () => {
  const app = browser();
  app.ready();
  app.systemDark(true);
  assertPreference(app, 'system', 'graphite');
  app.systemDark(false);
  assertPreference(app, 'system', 'jade');
  app.choose(0, 'paper');
  app.systemDark(true);
  assertPreference(app, 'paper', 'paper');
  app.choose(1, 'night');
  app.systemDark(false);
  assertPreference(app, 'night', 'night');
  app.choose(0, 'system');
  assertPreference(app, 'system', 'jade');
  app.systemDark(true);
  assertPreference(app, 'system', 'graphite');
});

test('both selectors save the preference, stay synchronized, and restore it on reload', () => {
  const app = browser({ dark: true });
  app.ready();
  for (const [index, preference] of ['paper', 'jade', 'night', 'graphite', 'system'].entries()) {
    app.choose(index % 2, preference);
    const theme = preference === 'system' ? 'graphite' : preference;
    assertPreference(app, preference, theme);
    assert.equal(app.storage.get(storageKey), preference);
    const reloaded = browser({ stored: app.storage.get(storageKey), dark: true });
    assert.equal(reloaded.theme, theme);
    reloaded.ready();
    assertPreference(reloaded, preference, theme);
  }
});

test('invalid saved preferences fall back to the current system appearance', () => {
  for (const stored of ['', 'unknown', 'DARK', '__proto__']) {
    const app = browser({ stored, dark: true });
    assert.equal(app.theme, 'graphite');
    app.ready();
    assertPreference(app, 'system', 'graphite');
    app.systemDark(false);
    assertPreference(app, 'system', 'jade');
  }
});

test('storage read and write failures do not block rendering or skin changes', () => {
  for (const [readError, writeError] of [[true, false], [false, true], [true, true]]) {
    const app = browser({ stored: 'paper', dark: true, readError, writeError });
    assert.equal(app.theme, readError ? 'graphite' : 'paper');
    app.ready();
    assertPreference(app, readError ? 'system' : 'paper', readError ? 'graphite' : 'paper');
    app.choose(0, 'night');
    assertPreference(app, 'night', 'night');
    app.choose(1, 'system');
    app.systemDark(false);
    assertPreference(app, 'system', 'jade');
  }
});

test('storage events synchronize preferences, ignore other keys, and reset on removal or clear', () => {
  const app = browser({ stored: 'paper', dark: true });
  app.ready();
  app.storageEvent(storageKey, 'night');
  assertPreference(app, 'night', 'night');
  app.storageEvent('unrelated-setting', 'jade');
  assertPreference(app, 'night', 'night');
  app.storageEvent(storageKey, 'invalid');
  assertPreference(app, 'system', 'graphite');
  app.systemDark(false);
  assertPreference(app, 'system', 'jade');
  app.storageEvent(storageKey, 'paper');
  app.storageEvent(storageKey, null);
  assertPreference(app, 'system', 'jade');
  app.storageEvent(storageKey, 'night');
  app.systemDark(true);
  app.storageEvent(null, null);
  assertPreference(app, 'system', 'graphite');
  app.systemDark(false);
  assertPreference(app, 'system', 'jade');
});

test('all Oil skins are selectable, restore before rendering, and clear their layout when switching back', () => {
  const app = browser();
  app.ready();
  const options = app.selectors[0].children[0].children;
  assert.equal(options.length, 46);
  assert.equal(new Set(options.map((option) => option.value)).size, 46);
  const css = readFileSync(new URL('./oil.css', import.meta.url), 'utf8');
  for (const option of options) {
    assert.ok(option.textContent);
    assert.ok(css.includes(`[data-theme="${option.value}"]`), `missing style for ${option.value}`);
    app.choose(0, option.value);
    assertPreference(app, option.value, option.value);
    assert.ok(['workspace', 'compact', 'cards', 'editorial', 'board', 'deck', 'split'].includes(app.layout));
    assert.ok(['light', 'dark'].includes(app.mode));
    const restored = browser({ stored: app.storage.get(storageKey) });
    assert.equal(restored.theme, option.value);
    assert.equal(restored.layout, app.layout);
    assert.equal(restored.mode, app.mode);
    app.storageEvent(storageKey, 'jade');
    assertPreference(app, 'jade', 'jade');
    assert.equal(app.layout, 'workspace');
    assert.equal(app.mode, '');
  }
});
