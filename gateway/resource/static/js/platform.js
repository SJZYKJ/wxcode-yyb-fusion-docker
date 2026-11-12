(() => {
  // 页面标题与面包屑：/runs 的四个子视图各自有标题。
  const pages = {
    "/": ["工作台", "账号与能力调用"],
    "/scan": ["添加账号", "微信授权"],
    "/proxies": ["代理设置", "账号网络出口"],
    "/runs": ["运行与日志", "任务、调用记录与系统日志"],
    "/users": ["用户管理", "成员与访问权限"],
    "/account-links": ["已激活短链接", "一次性授权链接"],
    "/settings": ["个人设置", "资料与安全"]
  };
  // 控制台内部切视图走 history.pushState，不会重新执行脚本；每次都读当前地址栏，
  // 否则导航高亮会停在上一次的 focus/view 上。
  const params = () => new URLSearchParams(location.search);
  const view = params().get("view");
  const focus = params().get("focus");
  const runsViews = {
    tasks: ["运行与日志", "任务列表与批量操作"],
    logs: ["调用记录", "脚本调用与运行日志"],
    system: ["系统日志", "授权、续期与接口诊断"],
    push: ["独立推送", "账号通知设置"]
  };
  const contextual = location.pathname === "/runs" ? (runsViews[view] || runsViews.tasks)
    : location.pathname === "/" ? { accounts: ["我的微信账号", "账号与有效期"], test: ["接口测试", "协议能力调用"] }[focus]
    : null;
  const current = contextual || pages[location.pathname] || ["wxcode-YYB", "管理控制台"];
  const main = document.querySelector("main");
  if (!main) return;

  // ---------------------------------------------------------------------------
  // 图标体系 v4：统一 24×24 视觉盒、stroke 2px、圆角端点与圆角连接。
  // 全部重画，形状与 v3 那套（矩形点阵 / 加号圆 / 三横线）不同，但线宽、光心、
  // 端点风格一致，所以混排时不会一大一小。
  // ---------------------------------------------------------------------------
  const glyph = body => '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"'
    + ' stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">' + body + "</svg>";

  const icons = {
    // 工作台：层叠面板
    dashboard: glyph('<path d="M12 3 21 7.5 12 12 3 7.5z"/><path d="m3 12 9 4.5L21 12"/><path d="m3 16.5 9 4.5 9-4.5"/>'),
    // 我的微信账号：对话气泡 + 三个点
    account: glyph('<path d="M21 11.5a8.4 8.4 0 0 1-8.5 8.4 9 9 0 0 1-3.8-.9L3 20.5l1.5-4.6a8.4 8.4 0 0 1-1-4.4A8.4 8.4 0 0 1 12 3.1a8.4 8.4 0 0 1 9 8.4z"/>'
      + '<path d="M8.6 11.6h.01M12 11.6h.01M15.4 11.6h.01"/>'),
    // 添加账号：扫描取景框
    scan: glyph('<path d="M4 9V6.5A2.5 2.5 0 0 1 6.5 4H9"/><path d="M15 4h2.5A2.5 2.5 0 0 1 20 6.5V9"/>'
      + '<path d="M20 15v2.5a2.5 2.5 0 0 1-2.5 2.5H15"/><path d="M9 20H6.5A2.5 2.5 0 0 1 4 17.5V15"/><path d="M4.5 12h15"/>'),
    // 运行与日志：圆角面板里的播放键
    runs: glyph('<rect x="3" y="4.5" width="18" height="15" rx="4.5"/><path d="M10.5 9.3l4.5 2.7-4.5 2.7z"/>'),
    // 独立推送：纸飞机
    push: glyph('<path d="M20.5 3.5 3.8 10.4a.7.7 0 0 0 .1 1.3l6.2 2 2 6.2a.7.7 0 0 0 1.3.1z"/><path d="M20.5 3.5 10.1 13.7"/>'),
    // 调用记录：圆点列表
    logs: glyph('<path d="M9 6h11M9 12h11M9 18h7"/><path d="M4.5 6h.01M4.5 12h.01M4.5 18h.01"/>'),
    // 系统日志：脉冲心电图
    system: glyph('<path d="M2.5 12h3.7l2.1-5.5 3.4 11 2.1-5.5h7.7"/>'),
    // 接口测试：锥形瓶
    test: glyph('<path d="M9.5 3.2h5"/><path d="M10 3.2v6L5.4 18a2.2 2.2 0 0 0 1.9 3.3h9.4a2.2 2.2 0 0 0 1.9-3.3L14 9.2v-6"/>'
      + '<path d="M7.6 15.6h8.8"/>'),
    // 用户管理：群组
    users: glyph('<circle cx="8" cy="8" r="2.8"/><circle cx="16" cy="8" r="2.8"/>'
      + '<path d="M2.8 19.5c0-2.9 2.3-5.2 5.2-5.2s5.2 2.3 5.2 5.2"/>'
      + '<path d="M11 19.5c0-2.9 2.3-5.2 5.2-5.2s5 2.3 5 5.2"/>'),
    // 个人设置：滑杆
    settings: glyph('<path d="M3.5 8h9M17.5 8h3"/><path d="M3.5 16h3M11.5 16h9"/>'
      + '<circle cx="14.8" cy="8" r="2.6"/><circle cx="8.2" cy="16" r="2.6"/>'),
    // 代理设置：地球 + 经线
    proxy: glyph('<circle cx="12" cy="12" r="8.6"/><path d="M3.4 12h17.2"/>'
      + '<path d="M12 3.4c2.4 2.3 3.8 5.4 3.8 8.6s-1.4 6.3-3.8 8.6c-2.4-2.3-3.8-5.4-3.8-8.6s1.4-6.3 3.8-8.6z"/>'),
    // 已激活短链接：链环
    link: glyph('<path d="m9.6 14.4 4.8-4.8"/><path d="M7.9 11.1 5.6 13.4a3.9 3.9 0 0 0 5.5 5.5l2.3-2.3"/>'
      + '<path d="m16.1 12.9 2.3-2.3a3.9 3.9 0 0 0-5.5-5.5l-2.3 2.3"/>'),
    // 接口文档：代码括号
    docs: glyph('<path d="M9.5 8.5 6 12l3.5 3.5"/><path d="M14.5 8.5 18 12l-3.5 3.5"/><path d="m13 5.5-2 13"/>'),
    // 退出登录：门 + 出向箭头
    logout: glyph('<path d="M9.5 20.5H6A2.5 2.5 0 0 1 3.5 18V6A2.5 2.5 0 0 1 6 3.5h3.5"/>'
      + '<path d="M15.5 8 20 12l-4.5 4"/><path d="M20 12H9"/>'),
    // 切换导航宽度：左侧栏
    pin: glyph('<rect x="3" y="4.5" width="18" height="15" rx="3.5"/><path d="M9.5 4.5v15"/>'),
    // 搜索：方圆形镜头
    search: glyph('<rect x="3.2" y="3.2" width="12.6" height="12.6" rx="4.2"/><path d="m14.6 14.6 6.2 6.2"/>'),
    // 亮色：日出
    sun: glyph('<path d="M5.2 16a6.8 6.8 0 0 1 13.6 0"/><path d="M2.5 16h19"/><path d="M12 3.6V6"/>'
      + '<path d="M5.2 7.4 6.9 9M18.8 7.4 17.1 9"/>'),
    // 深色：弯月 + 星
    moon: glyph('<path d="M17.8 20.2A8.6 8.6 0 1 1 9.3 5.6a6.9 6.9 0 0 0 8.5 14.6z"/>'
      + '<path d="m18.6 3.2.8 1.9 1.9.8-1.9.8-.8 1.9-.8-1.9-1.9-.8 1.9-.8z"/>'),
    // 移动端抽屉开关：汉堡
    menu: glyph('<path d="M3.5 7h17M3.5 12h17M3.5 17h11"/>'),
    // 管理分组标记
    shield: glyph('<path d="M12 3.2 19.5 6v5.2c0 4.4-3.1 8.3-7.5 9.6-4.4-1.3-7.5-5.2-7.5-9.6V6z"/>'
      + '<path d="m9.2 11.8 2 2 3.6-3.6"/>')
  };

  // 侧栏只保留「运行与日志」一个入口，它的四个子视图挂在条目下方。
  // 第三个分组是管理员区（data-admin-only 由第 4 个字段的取反决定，不要改顺序）。
  const navGroups = [
    { label: "工作区", items: [
      ["/", "dashboard", "工作台", true, false],
      ["/?focus=accounts", "account", "我的微信账号", true, false],
      ["/scan", "scan", "添加账号", true, false],
      ["/runs", "runs", "运行与日志", true, false, [
        ["/runs", "runs", "任务"],
        ["/runs?view=logs", "logs", "调用记录"],
        ["/runs?view=system", "system", "系统日志"],
        ["/runs?view=push", "push", "独立推送"]
      ]],
      ["/?focus=test", "test", "接口测试", true, false],
      ["/docs/index.html", "docs", "接口文档", true, false]
    ]},
    { label: "账号与网络", items: [
      ["/proxies", "proxy", "代理设置", true, false],
      ["/account-links", "link", "已激活短链接", true, true],
      ["/settings", "settings", "个人设置", true, false]
    ]},
    { label: "管理后台", icon: "shield", tone: "admin", items: [
      ["/users", "users", "用户管理", false, true],
      ["/logs", "logs", "运行日志", false, true]
    ]}
  ];

  const isActive = href => {
    const [path, queryString] = href.split("?");
    if (location.pathname !== path) return false;
    if (!queryString) return !params().has("focus") && !params().has("view");
    const targetQuery = new URLSearchParams(queryString);
    return [...targetQuery.keys()].every(key => targetQuery.get(key) === params().get(key));
  };

  // 页面里 index.html 会按角色切换 #usersNav 的可见性（已做空值保护），这里保留同一个 id。
  const navIds = { "/users": "usersNav" };
  const navLinkHTML = item => {
    const href = item[0], icon = item[1], label = item[2], visible = item[3], authOnly = item[4], children = item[5] || [];
    // 同一时刻只有一个 aria-current="page"：子视图激活时，父条目只做「展开」不做「当前」。
    const childActive = children.some(child => isActive(child[0]));
    const active = isActive(href) || (children.length > 0 && location.pathname === href.split("?")[0] && !childActive);
    let html = '<a href="' + href + '"' + (navIds[href] ? ' id="' + navIds[href] + '"' : "") + ' data-label="' + label + '" data-admin-only="' + (!visible) + '" data-auth-only="' + authOnly + '"'
      + ((!visible || authOnly) ? " hidden" : "")
      + (active ? ' aria-current="page"' : "")
      + '><span class="platform-nav-icon">' + icons[icon] + '</span><span class="platform-nav-label">' + label + "</span></a>";
    if (children.length) {
      html += '<div class="platform-nav-sub" data-open="' + (location.pathname === href.split("?")[0] ? "true" : "false") + '">'
        + children.map(child => {
            // 与父条目同 href 的子项（/runs 的「任务」）只做视觉高亮，不重复声明当前页。
            const subActive = isActive(child[0]) && child[0] !== href;
            return '<a href="' + child[0] + '" data-label="' + child[2] + '" class="platform-nav-sublink' + (isActive(child[0]) ? " is-active" : "") + '"' + (subActive ? ' aria-current="page"' : "") + ">" + child[2] + "</a>";
          }).join("")
        + "</div>";
    }
    return html;
  };

  // 顶部主横幅的一级导航：工作区那组条目（出现频率最高的一层）。
  const primaryItems = navGroups[0].items;
  const primaryLinkHTML = item => {
    const href = item[0], icon = item[1], label = item[2];
    // 只按链接自身的路径 + 查询串判定，避免 /?focus=... 与 / 互相串选。
    const active = isActive(href);
    return '<a class="platform-primary-link" href="' + href + '" data-label="' + label + '"'
      + (active ? ' aria-current="page"' : "")
      + '><span class="platform-primary-icon">' + icons[icon] + "</span><span>" + label + "</span></a>";
  };

  // 移动端底部 dock：五个高频入口，桌面端不渲染可见内容（CSS 隐藏）。
  const dockItems = ["/", "/?focus=accounts", "/scan", "/runs", "/proxies"]
    .map(href => primaryItems.find(item => item[0] === href) || navGroups[1].items.find(item => item[0] === href))
    .filter(Boolean);
  const dockLinkHTML = item => {
    const href = item[0], icon = item[1], label = item[2];
    const active = isActive(href);
    return '<a class="platform-dock-link" href="' + href + '" data-label="' + label + '"'
      + (active ? ' aria-current="page"' : "")
      + '><span class="platform-dock-icon">' + icons[icon] + "</span><span>" + label + "</span></a>";
  };

  const shell = document.createElement("div");
  shell.className = "platform-shell";
  shell.innerHTML = [
    '<header class="platform-banner" aria-label="主导航与工具">',
    '  <div class="platform-banner-brand">',
    '    <button class="platform-menu" id="platformMenu" type="button" aria-label="打开导航" aria-controls="platformNavigation" aria-expanded="false">' + icons.menu + '</button>',
    '    <a class="platform-brand" href="/"><span class="platform-brand-mark">Y</span><span class="platform-brand-copy"><strong>/wxcode-YYB</strong><span>微信协议管理平台</span></span></a>',
    '  </div>',
    '  <nav class="platform-primary" aria-label="一级导航">' + primaryItems.map(primaryLinkHTML).join("") + '</nav>',
    '  <div class="platform-banner-tools">',
    '    <div class="platform-search">' + icons.search + '<input id="platformSearch" type="search" autocomplete="off" placeholder="搜索页面与功能（Ctrl / ⌘ + K）" aria-label="搜索页面与功能"><div class="platform-search-results" id="platformSearchResults" hidden></div></div>',
    '    <span class="platform-pill is-optional" id="platformAccountsPill" hidden title="账号在线情况"><span class="dot"></span><span id="platformAccountsText">账号读取中</span></span>',
    '    <button class="platform-iconbtn" id="platformTheme" type="button" aria-label="切换明暗主题" title="切换明暗主题">' + icons.moon + '</button>',
    '    <details class="platform-user" id="platformUserMenu"><summary><span class="platform-avatar" id="platformAvatar">Y</span><span class="platform-user-copy"><strong id="platformUserName">正在读取</strong><span class="platform-user-role" id="platformUserRole">当前用户</span></span></summary>',
    '      <div class="platform-user-menu"><a href="/settings">个人设置<span>/settings</span></a><a href="/account-links">已激活短链接<span>/account-links</span></a><button type="button" id="platformLogoutMenu">退出登录<span>exit</span></button></div>',
    '    </details>',
    '  </div>',
    '</header>',
    '<div class="platform-body">',
    '  <aside class="platform-sidebar" aria-label="主导航">',
    '    <div class="platform-sidebar-head"><span class="platform-sidebar-kicker">导航</span><span class="platform-sidebar-hint">⌘/Ctrl + K 搜索</span></div>',
    '    <nav class="platform-nav">' + navGroups.map(group => '<div class="platform-nav-section" data-tone="' + (group.tone || "normal") + '"><div class="platform-nav-group">'
        + (group.icon ? '<span class="platform-nav-group-icon">' + icons[group.icon] + "</span>" : "")
        + "<span>" + group.label + "</span></div>" + group.items.map(navLinkHTML).join("") + "</div>").join("") + "</nav>",
    '    <div class="platform-sidebar-foot">',
    '      <button type="button" id="platformRailPin" data-label="切换导航宽度" aria-label="切换导航宽度" aria-pressed="false"><span class="platform-nav-icon">' + icons.pin + '</span><span class="platform-nav-label">收起导航</span></button>',
    '      <button type="button" id="platformLogout" data-label="退出登录" aria-label="退出登录"><span class="platform-nav-icon">' + icons.logout + '</span><span class="platform-nav-label">退出登录</span></button>',
    '    </div>',
    '  </aside>',
    '  <button class="platform-overlay" id="platformOverlay" type="button" aria-label="关闭导航"></button>',
    '  <section class="platform-stage">',
    '    <div class="platform-page-context"><span class="platform-page-title">' + current[0] + '</span><span class="platform-breadcrumb">wxcode-YYB / ' + current[1] + '</span></div>',
    '    <div class="platform-main"></div>',
    '  </section>',
    '</div>',
    '<nav class="platform-dock" aria-label="快捷导航">' + dockItems.map(dockLinkHTML).join("") + '</nav>'
  ].join("");
  document.body.insertBefore(shell, document.body.firstChild);
  shell.querySelector(".platform-main").appendChild(main);
  document.body.classList.add("platform-ready");
  // 主题：深色优先；显式选择写入 localStorage("yyb-skin")，未选择时跟随系统。
  // 图标显示“当前状态”：暗色 = 月亮，亮色 = 太阳。
  const themeButton = document.getElementById("platformTheme");
  const systemLight = matchMedia("(prefers-color-scheme: light)");
  const effectiveTheme = () => document.documentElement.dataset.theme
    || (systemLight.matches ? "light" : "dark");
  const showTheme = () => {
    const theme = effectiveTheme();
    const light = theme === "light";
    themeButton.innerHTML = light ? icons.sun : icons.moon;
    themeButton.setAttribute("aria-pressed", light ? "true" : "false");
    themeButton.setAttribute("aria-label", light ? "切换到深色主题" : "切换到浅色主题");
    themeButton.title = light ? "切换到深色主题" : "切换到浅色主题";
  };
  themeButton.onclick = () => {
    const next = effectiveTheme() === "light" ? "dark" : "light";
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem("yyb-skin", next); } catch (error) {}
    showTheme();
  };
  window.addEventListener("storage", event => {
    if (event.key !== "yyb-skin") return;
    if (event.newValue === "light" || event.newValue === "dark") document.documentElement.dataset.theme = event.newValue;
    else delete document.documentElement.dataset.theme;
    showTheme();
  });
  systemLight.addEventListener("change", showTheme);
  showTheme();

  // 侧栏宽度：data-rail="open" 表示收起成图标条（宽屏下仍然可见）。
  const railPin = document.getElementById("platformRailPin");
  const railOpen = () => shell.dataset.rail === "open";
  railPin.onclick = () => {
    if (railOpen()) delete shell.dataset.rail; else shell.dataset.rail = "open";
    railPin.setAttribute("aria-pressed", railOpen() ? "true" : "false");
  };

  // 全局搜索：前端过滤导航项（含 /runs 的四个子视图）。
  const searchEntries = [];
  navGroups.forEach(group => group.items.forEach(item => {
    searchEntries.push({ href: item[0], label: item[2], group: group.label });
    (item[5] || []).forEach(child => searchEntries.push({ href: child[0], label: child[2], group: item[2] }));
  }));
  const searchInput = document.getElementById("platformSearch");
  const searchResults = document.getElementById("platformSearchResults");
  const closeSearch = () => { searchResults.hidden = true; searchResults.innerHTML = ""; };
  const runSearch = () => {
    const query = searchInput.value.trim().toLowerCase();
    if (!query) return closeSearch();
    // 按当前权限过滤：被角色/登录态隐藏的入口不出现在搜索结果里。
    const blocked = new Set([...shell.querySelectorAll(".platform-nav a[hidden]")].map(link => link.getAttribute("href")));
    const matched = searchEntries
      .filter(entry => !blocked.has(entry.href) && entry.label.toLowerCase().includes(query))
      .slice(0, 8);
    if (!matched.length) {
      searchResults.innerHTML = '<div class="platform-search-results-empty">没有匹配的页面</div>';
      searchResults.hidden = false;
      return;
    }
    searchResults.innerHTML = matched.map(entry => '<a href="' + entry.href + '"><span>' + entry.label + "</span><span>" + entry.group + "</span></a>").join("");
    searchResults.hidden = false;
  };
  searchInput.addEventListener("input", runSearch);
  searchInput.addEventListener("focus", runSearch);
  searchInput.addEventListener("keydown", event => {
    if (event.key === "Escape") { searchInput.value = ""; closeSearch(); searchInput.blur(); return; }
    if (event.key === "Enter") { event.preventDefault(); searchResults.querySelector("a")?.click(); }
  });
  document.addEventListener("click", event => { if (!event.target.closest(".platform-search")) closeSearch(); });
  document.addEventListener("keydown", event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      searchInput.focus();
      searchInput.select();
    }
  });

  // 移动端抽屉：同一个 platformMenu 按钮开 / 关；遮罩、Esc 与 Tab 焦点环
  // 沿用原有逻辑（关闭后侧栏 inert，焦点回到菜单按钮）。
  const menu = document.getElementById("platformMenu");
  const sidebar = shell.querySelector(".platform-sidebar");
  const overlay = document.getElementById("platformOverlay");
  const mobileNav = matchMedia("(max-width: 899px)");
  sidebar.id = "platformNavigation";
  menu.setAttribute("aria-controls", sidebar.id);
  menu.setAttribute("aria-expanded", "false");
  if (overlay) { overlay.tabIndex = -1; overlay.hidden = true; }
  const closeNav = () => {
    const restore = document.body.classList.contains("platform-nav-open");
    document.body.classList.remove("platform-nav-open");
    menu.setAttribute("aria-expanded", "false");
    sidebar.inert = mobileNav.matches;
    if (overlay) overlay.hidden = true;
    if (restore && mobileNav.matches) menu.focus();
  };
  // These navigation entries are views of the same workspace. Reloading the
  // document silently resets the account, target AppID and pending request.
  const focusWorkspace = () => {
    if (location.pathname !== "/") return;
    const focus = new URLSearchParams(location.search).get("focus");
    const context = {accounts:["我的微信账号", "账号与有效期"], test:["接口测试", "协议能力调用"]}[focus] || pages["/"];
    shell.querySelector(".platform-page-title").textContent = context[0];
    shell.querySelector(".platform-breadcrumb").textContent = `wxcode-YYB / ${context[1]}`;
    shell.querySelectorAll(".platform-nav a").forEach(link => {
      if (isActive(link.getAttribute("href"))) link.setAttribute("aria-current", "page");
      else link.removeAttribute("aria-current");
    });
    const target = focus === "accounts" ? document.getElementById("accountWorkspace")
      : focus === "test" ? document.getElementById("callWorkspace") : main;
    target?.scrollIntoView({block:"start"});
  };
  sidebar.addEventListener("click", event => {
    const link = event.target.closest("a");
    if (!link || event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey
      || link.target || link.hasAttribute("download") || location.pathname !== "/") return;
    const target = new URL(link.href);
    if (target.origin !== location.origin || target.pathname !== "/") return;
    const ref = new URLSearchParams(location.search).get("ref");
    if (ref) target.searchParams.set("ref", ref);
    event.preventDefault();
    if (target.href !== location.href) history.pushState(null, "", target.pathname + target.search + target.hash);
    closeNav();
    focusWorkspace();
  });
  window.addEventListener("popstate", focusWorkspace);
  menu.onclick = () => {
    if (document.body.classList.contains("platform-nav-open")) return closeNav();
    document.body.classList.add("platform-nav-open");
    sidebar.inert = false;
    menu.setAttribute("aria-expanded", "true");
    if (overlay) overlay.hidden = false;
    sidebar.querySelector('a[aria-current="page"]')?.focus();
  };
  mobileNav.addEventListener("change", closeNav);
  closeNav();
  document.addEventListener("keydown", event => {
    if (!document.body.classList.contains("platform-nav-open")) return;
    if (event.key === "Escape") { event.preventDefault(); closeNav(); }
    if (event.key === "Tab") {
      const links = [...sidebar.querySelectorAll("a, button")].filter(el => el.getClientRects().length);
      const first = links[0], last = links[links.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
    }
  });
  overlay?.addEventListener("click", closeNav);
  shell.querySelectorAll(".platform-nav a").forEach(link => link.addEventListener("click", closeNav));
  shell.querySelectorAll(".platform-dock-link, .platform-primary-link").forEach(link => link.addEventListener("click", closeNav));
  const logout = async () => { await fetch("/logout", { method: "POST" }); location.href = "/login"; };
  document.getElementById("platformLogout").onclick = logout;
  document.getElementById("platformLogoutMenu").onclick = logout;

  // 横幅状态胶囊：只读汇总在线 / 掉线账号数，失败时保持隐藏，不影响页面。
  fetch("/accounts", { headers: { Accept: "application/json" } }).then(async response => {
    if (!response.ok) return null;
    const body = await response.json();
    const data = body && body.code === 0 ? body.data : null;
    const list = Array.isArray(data) ? data : (data && Array.isArray(data.accounts) ? data.accounts : null);
    if (!list) return null;
    const alive = list.filter(item => item.status === "alive").length;
    const pill = document.getElementById("platformAccountsPill");
    const copy = document.getElementById("platformAccountsText");
    const offline = list.length - alive;
    copy.innerHTML = "在线 <b>" + alive + "</b> · 掉线 <b>" + offline + "</b>";
    pill.classList.toggle("is-warn", alive === 0 && list.length > 0);
    pill.classList.toggle("is-bad", false);
    pill.hidden = false;
    return null;
  }).catch(() => null);

  fetch("/api/auth/me").then(async response => {
    if (response.status === 401) {
      location.replace("/login");
      return null;
    }
    const body = await response.json();
    if (!response.ok || body.code !== 0) throw new Error(body.msg || "读取用户失败");
    const authEnabled = body.data.auth_enabled !== false;
    const user = body.data.user;
    const name = user.display_name || user.username;
    document.getElementById("platformUserName").textContent = name;
    document.getElementById("platformUserRole").textContent = authEnabled ? (user.role === "admin" ? "管理员" : "普通用户") : "本机模式";
    const roleLabel = authEnabled ? (user.role === "admin" ? "管理员" : "普通用户") : "本机模式";
    const roleStat = document.getElementById("currentRoleText");
    const quotaStat = document.getElementById("currentQuotaText");
    if (roleStat) roleStat.textContent = roleLabel;
    if (quotaStat) quotaStat.textContent = "无限制";
    document.getElementById("platformAvatar").textContent = Array.from(name)[0]?.toUpperCase() || "Y";
    shell.querySelectorAll('.platform-nav a').forEach(link => {
      link.hidden = (link.dataset.adminOnly === "true" && user.role !== "admin") || (link.dataset.authOnly === "true" && !authEnabled);
    });
    // 分组里没有任何可见条目时，整组（含分组标题）一起隐藏，避免留下空标题。
    shell.querySelectorAll(".platform-nav-section").forEach(section => {
      const visible = [...section.querySelectorAll("a")].filter(link => !link.hidden).length;
      section.hidden = visible === 0;
    });
    const repairAccountsButton = document.getElementById("repairAccountsBtn");
    if (repairAccountsButton) repairAccountsButton.hidden = authEnabled && user.role !== "admin";
    document.querySelector(".platform-sidebar-foot").hidden = !authEnabled;
  }).catch(() => {
    document.getElementById("platformUserName").textContent = "状态未知";
    document.getElementById("platformUserRole").textContent = "请刷新页面";
  });
})();
