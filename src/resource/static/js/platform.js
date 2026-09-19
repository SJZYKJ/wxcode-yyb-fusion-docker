(() => {
  const pages = {
    "/": ["工作台", "账号与能力调用"],
    "/scan": ["添加账号", "微信授权"],
    "/runs": ["运行管理", "脚本任务与账号日志"],
    "/users": ["用户管理", "成员与访问权限"],
    "/settings": ["个人设置", "资料与安全"]
  };
  const current = pages[location.pathname] || ["YYB Go", "管理控制台"];
  const main = document.querySelector("main");
  if (!main) return;

  const icons = {
    home: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 10.5 12 3l9 7.5"/><path d="M5 9.5V21h14V9.5"/><path d="M9 21v-6h6v6"/></svg>',
    scan: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14"/><path d="M5 12h14"/></svg>',
    runs: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3"/></svg>',
    users: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>',
    settings: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.01a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51h.01a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>',
    docs: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M16 13H8M16 17H8M10 9H8"/></svg>',
    logout: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="M16 17l5-5-5-5"/><path d="M21 12H9"/></svg>'
  };
  const nav = [
    { href: "/", icon: "home", label: "工作台", adminOnly: false, authOnly: false },
    { href: "/scan", icon: "scan", label: "添加账号", adminOnly: false, authOnly: false },
    { href: "/runs", icon: "runs", label: "运行管理", adminOnly: false, authOnly: false },
    { href: "/users", icon: "users", label: "用户管理", adminOnly: true, authOnly: true },
    { href: "/settings", icon: "settings", label: "个人设置", adminOnly: false, authOnly: true },
    { href: "/docs/index.html", icon: "docs", label: "接口文档", adminOnly: false, authOnly: false }
  ];
  const navLinks = nav.map(item => {
    const isCurrent = location.pathname === item.href || (item.href !== "/" && location.pathname.startsWith(item.href));
    return `<a href="${item.href}" data-admin-only="${item.adminOnly}" data-auth-only="${item.authOnly}" ${item.adminOnly ? "hidden" : ""} ${isCurrent ? 'aria-current="page"' : ""}>${icons[item.icon]}<span>${item.label}</span></a>`;
  }).join("");
  const shell = document.createElement("div");
  shell.className = "platform-shell";
  shell.innerHTML = `
    <aside class="platform-sidebar" aria-label="主导航">
      <a class="platform-brand" href="/"><span class="platform-brand-mark">Y</span><span class="platform-brand-copy"><strong>YYB Go</strong><span>微信协议管理平台</span></span></a>
      <nav class="platform-nav"><div class="platform-nav-group">工作区</div>${navLinks}</nav>
      <div class="platform-sidebar-foot"><button type="button" id="platformLogout">${icons.logout}<span>退出登录</span></button></div>
    </aside>
    <button class="platform-overlay" id="platformOverlay" type="button" aria-label="关闭导航"></button>
    <section class="platform-stage">
      <header class="platform-topbar">
        <div style="display:flex;align-items:center;gap:12px;min-width:0"><button class="platform-menu" id="platformMenu" type="button" aria-label="打开导航">☰</button><div class="platform-page-context"><div class="platform-breadcrumb">YYB Go / ${current[1]}</div><div class="platform-page-title">${current[0]}</div></div></div>
        <div class="platform-user"><div class="platform-user-copy"><strong id="platformUserName">正在读取</strong><span id="platformUserRole">当前用户</span></div><span class="platform-avatar" id="platformAvatar">Y</span></div>
      </header>
      <div class="platform-main"></div>
    </section>`;
  document.body.insertBefore(shell, document.body.firstChild);
  shell.querySelector(".platform-main").appendChild(main);
  document.body.classList.add("platform-ready");

  const closeNav = () => document.body.classList.remove("platform-nav-open");
  document.getElementById("platformMenu").onclick = () => document.body.classList.toggle("platform-nav-open");
  document.getElementById("platformOverlay").onclick = closeNav;
  shell.querySelectorAll(".platform-nav a").forEach(link => link.addEventListener("click", closeNav));
  document.getElementById("platformLogout").onclick = async () => { await fetch("/logout", { method: "POST" }); location.href = "/login"; };

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
    document.getElementById("platformAvatar").textContent = Array.from(name)[0]?.toUpperCase() || "Y";
    // 管理项仅管理员可见；登录项仅在启用鉴权时可见。两条件取「或」（此前两行各自赋值互相覆盖，
    // 导致普通用户也能看到「用户管理」入口）。
    shell.querySelectorAll(".platform-nav a[data-admin-only], .platform-nav a[data-auth-only]").forEach(link => {
      const adminHidden = link.dataset.adminOnly === "true" && user.role !== "admin";
      const authHidden = link.dataset.authOnly === "true" && !authEnabled;
      link.hidden = adminHidden || authHidden;
    });
    document.querySelector(".platform-sidebar-foot").hidden = !authEnabled;
  }).catch(() => {
    document.getElementById("platformUserName").textContent = "状态未知";
    document.getElementById("platformUserRole").textContent = "请刷新页面";
  });
})();
