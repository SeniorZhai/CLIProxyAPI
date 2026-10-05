(() => {
  const storageKey = 'cpa-admin-theme';
  const oilThemes = [
    {"id": "oil-voice", "label": "声线 · 玫红", "layout": "editorial", "mode": "light"},
    {"id": "oil-reel", "label": "分镜 · 暖黑", "layout": "compact", "mode": "dark"},
    {"id": "oil-components", "label": "窑釉 · 棕陶", "layout": "workspace", "mode": "light"},
    {"id": "oil-muse", "label": "灵感 · 墨色画布", "layout": "board", "mode": "dark"},
    {"id": "oil-stay", "label": "湖栖 · 青瓷蓝", "layout": "editorial", "mode": "light"},
    {"id": "oil-diary", "label": "拾页 · 纸胶带", "layout": "board", "mode": "light"},
    {"id": "oil-home", "label": "间隙 · 橄榄铜", "layout": "cards", "mode": "dark"},
    {"id": "oil-anime", "label": "夏末 · 暮色票笺", "layout": "editorial", "mode": "dark"},
    {"id": "oil-lingo", "label": "Pip · 明黄厚边", "layout": "board", "mode": "light"},
    {"id": "oil-tracker", "label": "Relay · 紫灰精密", "layout": "split", "mode": "light"},
    {"id": "oil-freight", "label": "渡运 · 赤陶青墨", "layout": "editorial", "mode": "light"},
    {"id": "oil-ride", "label": "来接 · 蓝线胶囊", "layout": "deck", "mode": "light"},
    {"id": "oil-knot", "label": "编绳 · 雾紫细线", "layout": "split", "mode": "light"},
    {"id": "oil-recipe", "label": "灶 · 桃色专注", "layout": "deck", "mode": "dark"},
    {"id": "oil-cards", "label": "绎卡 · 鼠尾草纸", "layout": "cards", "mode": "light"},
    {"id": "oil-pulse", "label": "脉冲 · 酸柠黑", "layout": "compact", "mode": "dark"},
    {"id": "oil-keeb", "label": "键序 · 柔和工业", "layout": "cards", "mode": "light"},
    {"id": "oil-editor", "label": "Cut · 剪辑工作台", "layout": "compact", "mode": "dark"},
    {"id": "oil-weather", "label": "天色 · 晴暖组件", "layout": "board", "mode": "light"},
    {"id": "oil-contract", "label": "墨契 · 暗框纸页", "layout": "split", "mode": "light"},
    {"id": "oil-ledger", "label": "Penny · 奶油账本", "layout": "split", "mode": "light"},
    {"id": "oil-folio", "label": "小满 · 绘本画廊", "layout": "board", "mode": "light"},
    {"id": "oil-camera", "label": "隅 · 胶片仪器", "layout": "deck", "mode": "light"},
    {"id": "oil-silver", "label": "Morrow · 银灰编辑", "layout": "editorial", "mode": "light"},
    {"id": "oil-reader", "label": "页边 · 纸页批注", "layout": "editorial", "mode": "light"},
    {"id": "oil-lims", "label": "格存 · 精密数据台", "layout": "workspace", "mode": "light"},
    {"id": "oil-calendar", "label": "Cadence · 柔和计划簿", "layout": "deck", "mode": "light"},
    {"id": "oil-checkout", "label": "Pinch · 钴蓝双色", "layout": "workspace", "mode": "light"},
    {"id": "oil-maker", "label": "林屿 · 粉彩手作", "layout": "board", "mode": "light"},
    {"id": "oil-trail", "label": "山径 · 户外仪器", "layout": "compact", "mode": "light"},
    {"id": "oil-brew", "label": "杯沿尺 · 奶油器物", "layout": "deck", "mode": "light"},
    {"id": "oil-watch", "label": "刻度 · 单色仪表", "layout": "cards", "mode": "light"},
    {"id": "oil-roast", "label": "北纬 · 森绿柠黄", "layout": "editorial", "mode": "light"},
    {"id": "oil-letter", "label": "远信 · 雾蓝信纸", "layout": "deck", "mode": "light"},
    {"id": "oil-sneaker", "label": "步 · 运动陈列", "layout": "cards", "mode": "light"},
    {"id": "oil-copilot", "label": "Forge · 双明度编辑器", "layout": "split", "mode": "light"},
    {"id": "oil-scent", "label": "迹闻 · 冷绿陈列", "layout": "board", "mode": "light"},
    {"id": "oil-upload", "label": "Chute · 原生清单", "layout": "deck", "mode": "light"},
    {"id": "oil-club", "label": "圈里 · 奶油紫", "layout": "deck", "mode": "light"},
    {"id": "oil-vinyl", "label": "慢调 · 温润唱机", "layout": "deck", "mode": "light"},
    {"id": "oil-canvas", "label": "Loom · 粉彩便笺", "layout": "board", "mode": "light"},
    {"id": "oil-studio", "label": "入场 · 橙色操作台", "layout": "compact", "mode": "light"},
    {"id": "oil-podcast", "label": "Listen · 琥珀夜读", "layout": "editorial", "mode": "dark"},
    {"id": "oil-device", "label": "Ohm · 浅色硬件", "layout": "cards", "mode": "light"},
    {"id": "oil-wrapped", "label": "Tempo · 荧光海报", "layout": "board", "mode": "light"},
    {"id": "oil-mail", "label": "Post · 深浅工作台", "layout": "split", "mode": "light"},
  ];
  const themes = ['system', 'jade', 'graphite', 'paper', 'night', ...oilThemes.map((theme) => theme.id)];
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  let preference = 'system';
  try {
    const saved = window.localStorage.getItem(storageKey);
    if (themes.includes(saved)) preference = saved;
  } catch {}

  function apply() {
    document.documentElement.dataset.theme = preference === 'system'
      ? system.matches ? 'graphite' : 'jade'
      : preference;
    const selected = oilThemes.find((theme) => theme.id === preference);
    document.documentElement.dataset.themeLayout = selected?.layout || 'workspace';
    document.documentElement.dataset.themeMode = selected?.mode || '';
    for (const select of document.querySelectorAll('[data-theme-select]')) select.value = preference;
  }

  apply();
  document.addEventListener('DOMContentLoaded', () => {
    for (const select of document.querySelectorAll('[data-theme-select]')) {
      const group = document.createElement('optgroup');
      group.label = 'Oil UI';
      for (const theme of oilThemes) {
        const option = document.createElement('option');
        option.value = theme.id;
        option.textContent = theme.label;
        group.append(option);
      }
      select.append(group);
      select.addEventListener('change', () => {
        if (!themes.includes(select.value)) return;
        preference = select.value;
        try { window.localStorage.setItem(storageKey, preference); } catch {}
        apply();
      });
    }
    apply();
  });
  system.addEventListener('change', () => {
    if (preference === 'system') apply();
  });
  window.addEventListener('storage', (event) => {
    if (event.key !== storageKey && event.key !== null) return;
    preference = themes.includes(event.newValue) ? event.newValue : 'system';
    apply();
  });
})();
