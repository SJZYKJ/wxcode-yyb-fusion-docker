// 系统日志视图：可复用的挂载模块。
// 用法：<div data-system-logs="all"></div> 或 <div data-system-logs="api"></div>
// 需要页面同时引入 /static/css/system-logs.css。数据来自 GET /api/system-logs。
(() => {
  const CATEGORIES = { system: "服务运行", api: "接口请求", protocol: "协议调用", keepalive: "后台续期", account: "账号授权", management: "管理操作", auth: "面板登录" };
  const LEVELS = { debug: "调试", info: "信息", warn: "警告", error: "错误" };
  const STAGES = { refresh_token: "凭据续期", check_login: "授权检查", login_buffer: "登录票据", manual_auth: "会话登录", native_autoauth_transport: "独立恢复·传输", native_autoauth_application: "独立恢复·应用", native_autoauth_business: "独立恢复·业务", shortlink_transport: "短连接·传输", shortlink_application: "短连接·应用", pcmgr_login_buffer: "PCMgr 票据", pcmgr_key_renewal: "PCMgr 续期", circuit_target: "熔断·目标", circuit_session: "熔断·会话" };
  const OPERATIONS = { get_code: "取码", get_phone_number: "手机号", operate_wx_data: "operateWxData" };
  const SOURCES = { on_demand: "按需", manual: "手动" };

  // 渠道下拉与徽标都从服务端注册表（GET /api/scan-channels）派生，前端不再写死渠道。
  // 日志里的 channel 值是 internal/eventlog.ChannelFor 的产物（yyb/pcmgr/gameassist/
  // ldplayer/qbbrowser），而注册表里 YYB 的 id 是 appstore，所以把 appstore 折到 yyb。
  // 注册表读不到时只保留固定的 yyb 一项，绝不回退成「全部当 YYB」的旧行为。
  const YYB_CHANNEL = "yyb";
  const channelLabels = new Map([[YYB_CHANNEL, "YYB"]]);
  let registryPromise = null;

  function registryChannels() {
    if (registryPromise) return registryPromise;
    registryPromise = (async () => {
      try {
        const response = await fetch("/api/scan-channels", { cache: "no-store", headers: { Accept: "application/json" }, signal: AbortSignal.timeout(15000) });
        const payload = await response.json();
        const list = (payload && payload.data && payload.data.channels) || [];
        for (const item of list) {
          if (!item) continue;
          const id = String(item.id || "").trim();
          if (!id) continue;
          const value = id === "appstore" ? YYB_CHANNEL : id;
          const label = String(item.label || item.name || "").trim() || value;
          channelLabels.set(value, label);
        }
      } catch { /* 注册表不可用时保留 yyb */ }
      const values = Array.from(channelLabels.keys());
      const first = values.filter(value => value === YYB_CHANNEL);
      const rest = values.filter(value => value !== YYB_CHANNEL);
      return first.concat(rest).map(value => ({ value, label: channelLabels.get(value) }));
    })();
    return registryPromise;
  }

  const channelLabel = channel => {
    const id = String(channel || "");
    return channelLabels.get(id) || id;
  };

  const node = (tag, text, className) => {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = text;
    if (className) element.className = className;
    return element;
  };

  const TEMPLATE = [
    '<div class="log-toolbar">',
    '  <p class="log-status" role="status" aria-live="polite">正在读取日志…</p>',
    '  <div class="log-toolbar-actions">',
    '    <label class="live-toggle"><input type="checkbox" data-live> 自动刷新</label>',
    '    <button class="btn secondary" type="button" data-refresh>刷新日志</button>',
    '  </div>',
    '</div>',
    '<form class="log-filters" aria-label="筛选日志">',
    '  <label>等级<select class="input" name="level"><option value="">全部等级</option><option value="error">错误</option><option value="warn">警告</option><option value="info">信息</option><option value="debug">调试</option></select></label>',
    '  <label>分类<select class="input" name="category"><option value="">全部分类</option><option value="system">服务运行</option><option value="api">接口请求</option><option value="protocol">协议调用</option><option value="keepalive">后台续期</option><option value="account">账号授权</option><option value="management">管理操作</option><option value="auth">面板登录</option></select></label>',
    '  <label>渠道<select class="input" name="channel" data-channel-select><option value="">全部渠道</option></select></label>',
    '  <label>账号 ID<input class="input" name="account_id" type="number" min="1" step="1" placeholder="全部账号"></label>',
    '  <label>时间<select class="input" name="hours"><option value="168">近 7 天</option><option value="24">近 24 小时</option><option value="1">近 1 小时</option></select></label>',
    '  <div class="log-filter-actions"><button class="btn primary" type="submit">筛选日志</button><button class="btn secondary" type="reset">重置</button></div>',
    '</form>',
    '<div class="notice log-error" role="alert" hidden></div>',
    '<div class="table-wrap log-table-wrap" aria-busy="true">',
    '  <table class="log-table"><thead><tr><th scope="col">时间</th><th scope="col">等级</th><th scope="col">分类</th><th scope="col">账号 / 渠道</th><th scope="col">事件</th></tr></thead>',
    '  <tbody data-rows><tr><td colspan="5" class="log-empty">正在读取日志…</td></tr></tbody></table>',
    '</div>',
    '<div class="log-pagination"><span data-page>第 1 页</span><div><button class="btn secondary" type="button" data-previous disabled>上一页</button><button class="btn secondary" type="button" data-next disabled>下一页</button></div></div>',
    '<p class="log-footnote">最多保留 10,000 条。升级前的历史记录不回填；日志不包含密钥、扫码链接或请求正文。</p>'
  ].join("");

  function render(rowsNode, entries) {
    const fragment = document.createDocumentFragment();
    if (!entries.length) {
      const tr = node("tr"), td = node("td", "没有符合条件的记录。可调整筛选条件，或在后续操作后刷新。", "log-empty");
      td.colSpan = 5; tr.append(td); fragment.append(tr);
      rowsNode.replaceChildren(fragment);
      return;
    }
    for (const entry of entries) {
      const tr = node("tr", undefined, "log-row");
      const date = new Date(entry.created_at);
      const timeCell = node("td");
      const time = node("time", date.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }));
      time.dateTime = date.toISOString();
      timeCell.append(time);
      const severityCell = node("td");
      const badge = node("span", LEVELS[entry.level] || entry.level, "log-badge");
      badge.dataset.level = entry.level;
      severityCell.append(badge);
      const accountCell = node("td");
      accountCell.append(node("span", entry.account_id ? "ID " + entry.account_id : "全局", "log-account"));
      if (entry.channel) accountCell.append(node("span", channelLabel(entry.channel), "log-channel"));
      const messageCell = node("td"), details = entry.details || {};
      messageCell.append(node("span", entry.message));
      if (details.rejections && details.rejections.length) {
        messageCell.append(node("span", details.rejections.map(item => (STAGES[item.stage] || item.stage) + " " + item.code + (item.wechat_code ? " / WX " + item.wechat_code : "")).join(" · "), "log-rejections"));
      }
      const disclosure = node("details", undefined, "log-detail"), list = node("dl");
      disclosure.append(node("summary", "查看详情"));
      const operation = details.operation ? (OPERATIONS[details.operation] || details.operation) + (details.api ? " · " + details.api : "") : "";
      const values = [
        ["事件", entry.event], ["路径", details.route], ["方法", details.method], ["HTTP", details.http_status],
        ["AppID", details.app_id], ["操作", operation], ["来源", SOURCES[details.source] || details.source],
        ["耗时", details.duration_ms !== undefined ? details.duration_ms + " ms" : ""], ["丢弃条数", details.count]
      ];
      for (const [label, value] of values) {
        if (value === undefined || value === "" || value === 0) continue;
        list.append(node("dt", label), node("dd", String(value)));
      }
      disclosure.append(list);
      messageCell.append(disclosure);
      tr.append(timeCell, severityCell, node("td", CATEGORIES[entry.category] || entry.category), accountCell, messageCell);
      fragment.append(tr);
    }
    rowsNode.replaceChildren(fragment);
  }

  function mount(root) {
    root.classList.add("system-logs");
    root.innerHTML = TEMPLATE;
    const fixedCategory = root.dataset.systemLogs === "all" ? "" : (root.dataset.systemLogs || "");
    const form = root.querySelector(".log-filters");
    const rows = root.querySelector("[data-rows]");
    const statusNode = root.querySelector(".log-status");
    const errorNode = root.querySelector(".log-error");
    const pageNode = root.querySelector("[data-page]");
    const previous = root.querySelector("[data-previous]");
    const nextButton = root.querySelector("[data-next]");
    const refresh = root.querySelector("[data-refresh]");
    const live = root.querySelector("[data-live]");
    const tableWrap = root.querySelector(".log-table-wrap");
    if (fixedCategory) {
      const select = form.querySelector('select[name="category"]');
      select.value = fixedCategory;
      select.closest("label").hidden = true;
    }
    // 渠道选项来自注册表；先渲染占位，拿到注册表再替换，切换时保留已选值。
    const channelSelect = form.querySelector("select[data-channel-select]");
    void registryChannels().then(list => {
      const keep = channelSelect.value;
      const placeholder = new Option("全部渠道", "");
      channelSelect.replaceChildren(placeholder, ...list.map(item => new Option(item.label, item.value)));
      if (keep && list.some(item => item.value === keep)) channelSelect.value = keep;
    });
    const state = { filters: new URLSearchParams(), since: 0, page: 0, next: 0, cursors: [0], pending: false, sequence: 0, timer: null, loaded: false };
    const busy = value => {
      state.pending = value;
      tableWrap.setAttribute("aria-busy", String(value));
      refresh.disabled = value;
      previous.disabled = value || state.page === 0;
      nextButton.disabled = value || !state.next;
      form.querySelector('[type="submit"]').disabled = value;
    };
    async function load() {
      if (state.pending) return;
      const current = ++state.sequence;
      busy(true); errorNode.hidden = true;
      const query = new URLSearchParams(state.filters);
      if (state.cursors[state.page]) query.set("before", state.cursors[state.page]);
      if (state.since) query.set("since", state.since);
      try {
        const response = await fetch("/api/system-logs?" + query.toString(), { cache: "no-store", signal: AbortSignal.timeout(15000) });
        const body = await response.json();
        if (!response.ok || body.code !== 0) throw new Error(body.msg || "读取日志失败");
        if (current !== state.sequence) return;
        const data = body.data || {};
        const entries = data.entries || [];
        render(rows, entries);
        state.next = data.next_before || 0;
        state.loaded = true;
        statusNode.textContent = "本页 " + entries.length + " 条 · " + new Date().toLocaleTimeString("zh-CN", { hour12: false }) + " 更新";
        pageNode.textContent = "第 " + (state.page + 1) + " 页";
      } catch (error) {
        errorNode.textContent = error.name === "TimeoutError" ? "读取超时，请刷新重试。" : error.message;
        errorNode.hidden = false;
        statusNode.textContent = "日志读取失败";
        if (!rows.querySelector(".log-row")) render(rows, []);
      } finally { busy(false); }
    }
    function applyFilters() {
      if (state.pending) return;
      state.filters = new URLSearchParams();
      const data = new FormData(form);
      for (const name of ["category", "level", "channel", "account_id"]) {
        if (data.get(name)) state.filters.set(name, data.get(name));
      }
      if (fixedCategory) state.filters.set("category", fixedCategory);
      state.since = Date.now() - Number(data.get("hours") || 168) * 3600000;
      state.page = 0; state.cursors.splice(0, state.cursors.length, 0); state.next = 0;
      return load();
    }
    form.addEventListener("submit", event => { event.preventDefault(); void applyFilters(); });
    form.addEventListener("reset", () => { if (!state.pending) setTimeout(() => void applyFilters(), 0); });
    refresh.onclick = () => { if (state.pending) return; state.page = 0; state.cursors.splice(0, state.cursors.length, 0); void load(); };
    previous.onclick = () => { if (!state.pending && state.page > 0) { state.page--; void load(); } };
    nextButton.onclick = () => { if (!state.pending && state.next) { state.cursors[++state.page] = state.next; void load(); } };
    live.onchange = () => {
      clearInterval(state.timer);
      if (!live.checked) return;
      state.timer = setInterval(() => {
        if (document.hidden || state.pending || state.page !== 0 || rows.querySelector("details[open]")) return;
        void load();
      }, 15000);
    };
    root.__systemLogs = { ensure: () => { if (!state.loaded) return applyFilters(); return null; } };
    return root.__systemLogs;
  }

  const hosts = Array.from(document.querySelectorAll("[data-system-logs]"));
  for (const host of hosts) mount(host);

  window.SystemLogs = {
    refresh(kind) {
      for (const host of hosts) {
        if (host.dataset.systemLogs !== kind) continue;
        const instance = host.__systemLogs;
        if (!instance) continue;
        return instance.ensure();
      }
      return null;
    },
    get mounted() { return hosts.length; }
  };
  window.addEventListener("storage", event => { if (event.key === "yyb-skin") { /* 主题由 platform.js 处理 */ } });
})();
