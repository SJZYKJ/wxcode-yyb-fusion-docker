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

  const nav = [
    ["/", "台", "工作台", true, false],
    ["/scan", "+", "添加账号", true, false],
    ["/runs", "运", "运行管理", true, false],
    ["/users", "人", "用户管理", false, true],
    ["/settings", "设", "个人设置", true, true],
    ["/docs/index.html", "API", "接口文档", true, false]
  ];
  const shell = document.createElement("div");
  shell.className = "platform-shell";
  shell.innerHTML = `
    <aside class="platform-sidebar" aria-label="主导航">
      <a class="platform-brand" href="/"><span class="platform-brand-mark">Y</span><span class="platform-brand-copy"><strong>YYB Go</strong><span>微信协议管理平台</span></span></a>
      <nav class="platform-nav"><div class="platform-nav-group">平台功能</div>${nav.map(([href, icon, label, visible, authOnly]) => `<a href="${href}" data-admin-only="${!visible}" data-auth-only="${authOnly}" ${location.pathname === href ? 'aria-current="page"' : ""}><span class="platform-nav-icon">${icon}</span><span>${label}</span></a>`).join("")}</nav>
      <div class="platform-sidebar-foot"><button type="button" id="platformLogout"><span class="platform-nav-icon">退</span><span>退出登录</span></button></div>
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
    shell.querySelectorAll('[data-admin-only="true"]').forEach(link => { link.hidden = user.role !== "admin"; });
    shell.querySelectorAll('[data-auth-only="true"]').forEach(link => { link.hidden = !authEnabled; });
    document.querySelector(".platform-sidebar-foot").hidden = !authEnabled;
  }).catch(() => {
    document.getElementById("platformUserName").textContent = "状态未知";
    document.getElementById("platformUserRole").textContent = "请刷新页面";
  });
})();
