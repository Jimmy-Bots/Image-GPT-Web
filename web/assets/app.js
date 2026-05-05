const storageKey = "gpt_image_web_token";

const state = {
  token: sessionStorage.getItem(storageKey) || "",
  identity: null,
  accounts: [],
  users: [],
  keys: [],
  tasks: [],
  images: [],
  settings: {},
  busy: new Set(),
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];
const encoder = new TextEncoder();

function authHeaders(extra = {}) {
  return state.token ? { Authorization: `Bearer ${state.token}`, ...extra } : { ...extra };
}

async function api(path, options = {}) {
  const headers = authHeaders(options.headers || {});
  const res = await fetch(path, { ...options, headers, credentials: "same-origin" });
  const text = await res.text();
  let data = {};
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!res.ok) throw new Error(data?.error?.message || data?.raw || res.statusText);
  return data;
}

function setBusy(id, busy) {
  if (busy) state.busy.add(id);
  else state.busy.delete(id);
  const el = document.getElementById(id);
  if (el instanceof HTMLButtonElement) el.disabled = busy;
}

async function guarded(id, fn) {
  if (state.busy.has(id)) return;
  setBusy(id, true);
  try {
    await fn();
  } catch (err) {
    alert(err.message);
  } finally {
    setBusy(id, false);
  }
}

function show(view) {
  $('[data-view="login"]').classList.toggle("hidden", view !== "login");
  $('[data-view="app"]').classList.toggle("hidden", view !== "app");
}

function setTab(name) {
  $$(".nav").forEach((btn) => btn.classList.toggle("active", btn.dataset.tab === name));
  $$(".tab").forEach((panel) => panel.classList.toggle("hidden", panel.dataset.panel !== name));
  $("#page-title").textContent = {
    dashboard: "总览",
    accounts: "账号池",
    users: "用户",
    keys: "API Keys",
    tasks: "图片任务",
    images: "图片",
    settings: "设置",
    playground: "调试",
    logs: "日志",
  }[name] || name;
}

function isAdmin() {
  return state.identity?.role === "admin";
}

function usesLegacyKeyAPI() {
  return state.identity?.auth_type === "legacy";
}

function applyRoleUI() {
  $$("[data-admin-only]").forEach((el) => el.classList.toggle("hidden", !isAdmin()));
  if (!isAdmin() && $(".nav.active")?.dataset.tab !== "keys" && $(".nav.active")?.dataset.tab !== "playground") {
    setTab("keys");
  }
}

function fmtDate(value) {
  if (!value) return "-";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function badge(text) {
  const value = String(text ?? "-");
  const cls = /正常|active|true|success/.test(value) ? "ok" : /限流|queued|running/.test(value) ? "warn" : /异常|禁用|disabled|false|error|deleted/.test(value) ? "err" : "";
  return `<span class="badge ${cls}">${escapeHTML(value)}</span>`;
}

function bytes(value) {
  const raw = typeof value === "string" ? encoder.encode(value).length : JSON.stringify(value ?? "").length;
  if (raw < 1024) return `${raw} B`;
  if (raw < 1024 * 1024) return `${(raw / 1024).toFixed(1)} KB`;
  return `${(raw / 1024 / 1024).toFixed(1)} MB`;
}

function shortJSON(value, limit = 220) {
  const raw = JSON.stringify(value || {});
  return raw.length > limit ? `${raw.slice(0, limit)}...` : raw;
}

function taskID() {
  const cryptoObj = globalThis.crypto;
  if (cryptoObj?.randomUUID) return cryptoObj.randomUUID();
  return `task-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

async function bootstrap() {
  if (!state.token) return show("login");
  try {
    const me = await api("/api/me");
    state.identity = me.identity;
    $("#identity").textContent = `${me.identity.name || "-"} · ${me.identity.role}`;
    $("#version").textContent = "connected";
    applyRoleUI();
    show("app");
    await refreshAll();
  } catch {
    sessionStorage.removeItem(storageKey);
    state.token = "";
    show("login");
  }
}

async function refreshAll() {
  const loaders = [loadKeys(), loadModels(), loadTasks()];
  if (isAdmin()) {
    loaders.push(loadAccounts(), loadUsers(), loadImages(), loadSettings(), loadLogs(), loadStorageInfo());
  }
  await Promise.allSettled(loaders);
}

async function loadAccounts() {
  const data = await api("/api/accounts");
  state.accounts = data.items || [];
  $("#accounts-body").innerHTML = state.accounts.map((a) => `
    <tr>
      <td><span class="mono">${escapeHTML(a.access_token_masked || a.token_ref)}</span></td>
      <td>${escapeHTML(a.email || "-")}</td>
      <td>${escapeHTML(a.type || "-")}</td>
      <td>${badge(a.status)}</td>
      <td>${a.image_quota_unknown ? "未知" : escapeHTML(a.quota)}</td>
      <td>${escapeHTML(a.success)}/${escapeHTML(a.fail)}</td>
      <td>${fmtDate(a.last_used_at)}</td>
      <td class="actions">
        <button class="secondary compact-btn" data-refresh-account="${escapeHTML(a.token_ref)}">刷新</button>
        <button class="danger compact-btn" data-delete-account="${escapeHTML(a.token_ref)}">删除</button>
      </td>
    </tr>`).join("");
  $("#metric-accounts").textContent = state.accounts.length;
  $("#metric-active").textContent = state.accounts.filter((a) => !["异常", "禁用"].includes(a.status)).length;
  $("#metric-success").textContent = state.accounts.reduce((n, a) => n + Number(a.success || 0), 0);
  $("#metric-fail").textContent = state.accounts.reduce((n, a) => n + Number(a.fail || 0), 0);
}

async function loadModels() {
  const data = await api("/v1/models");
  $("#models").innerHTML = (data.data || []).slice(0, 32).map((m) => `<span class="chip">${escapeHTML(m.id)}</span>`).join("");
}

async function loadUsers() {
  const data = await api("/api/users");
  state.users = data.items || [];
  $("#users-body").innerHTML = state.users.map((u) => `
    <tr>
      <td>${escapeHTML(u.email)}</td>
      <td>${escapeHTML(u.name)}</td>
      <td>${badge(u.role)}</td>
      <td>${badge(u.status)}</td>
      <td>${fmtDate(u.last_login_at)}</td>
      <td class="actions">
        <button class="secondary compact-btn" data-toggle-user="${escapeHTML(u.id)}" data-user-status="${escapeHTML(u.status)}">${u.status === "active" ? "禁用" : "启用"}</button>
      </td>
    </tr>`).join("");
  $("#metric-users").textContent = state.users.length;
}

async function loadKeys() {
  const data = await api(usesLegacyKeyAPI() ? "/api/auth/users" : "/api/me/api-keys");
  state.keys = data.items || [];
  $("#keys-body").innerHTML = state.keys.map((k) => `
    <tr>
      <td>${escapeHTML(k.name)}</td>
      <td>${escapeHTML(k.role)}</td>
      <td>${badge(k.enabled)}</td>
      <td>${fmtDate(k.created_at)}</td>
      <td>${fmtDate(k.last_used_at)}</td>
      <td class="actions">
        <button class="secondary compact-btn" data-toggle-key="${escapeHTML(k.id)}" data-key-enabled="${escapeHTML(k.enabled)}">${k.enabled ? "停用" : "启用"}</button>
        <button class="danger compact-btn" data-delete-key="${escapeHTML(k.id)}">删除</button>
      </td>
    </tr>`).join("");
}

async function loadTasks() {
  const data = await api("/api/image-tasks");
  state.tasks = data.items || [];
  $("#tasks-body").innerHTML = state.tasks.map((item) => `
    <tr>
      <td><span class="mono">${escapeHTML(item.id)}</span></td>
      <td>${escapeHTML(item.mode)}</td>
      <td>${badge(item.status)}</td>
      <td>${escapeHTML(item.model || "-")}</td>
      <td>${escapeHTML(item.size || "-")}</td>
      <td>${bytes(item.data)}</td>
      <td>${fmtDate(item.updated_at)}</td>
    </tr>`).join("");
  $("#metric-tasks").textContent = state.tasks.length;
}

async function loadImages() {
  const data = await api("/api/images");
  state.images = data.items || [];
  $("#images-grid").innerHTML = state.images.map((item) => `
    <article class="image-item">
      <label><input type="checkbox" data-image-path="${escapeHTML(item.path)}" /> <span class="mono">${escapeHTML(item.name)}</span></label>
      <a href="${escapeHTML(item.url)}" target="_blank" rel="noreferrer"><img src="${escapeHTML(item.url)}" alt="${escapeHTML(item.name)}" loading="lazy" /></a>
      <div class="muted">${fmtDate(item.created_at)} · ${bytes(Number(item.size || 0))}</div>
    </article>`).join("");
}

async function loadSettings() {
  const data = await api("/api/settings");
  state.settings = data.config || {};
  $("#settings-json").value = JSON.stringify(state.settings, null, 2);
}

async function loadStorageInfo() {
  const data = await api("/api/storage/info");
  $("#storage-status").textContent = `${data.backend?.type || "-"} · ${data.health?.status || "-"}`;
}

async function loadLogs() {
  const type = $("#log-type")?.value || "";
  const data = await api(`/api/logs${type ? `?type=${encodeURIComponent(type)}` : ""}`);
  $("#logs-body").innerHTML = (data.items || []).map((item) => `
    <tr>
      <td><input type="checkbox" data-log-id="${escapeHTML(item.id)}" /></td>
      <td>${fmtDate(item.time)}</td>
      <td>${escapeHTML(item.type)}</td>
      <td>${escapeHTML(item.summary)}</td>
      <td><span class="mono">${escapeHTML(shortJSON(item.detail, 260))}</span></td>
    </tr>`).join("");
}

async function login(event) {
  event.preventDefault();
  const email = $("#login-email").value.trim();
  const password = $("#login-password").value;
  if (!password.trim()) return;
  let data;
  if (email && password) {
    data = await api("/auth/login", { method: "POST", headers: { "Content-Type": "application/json", Authorization: "" }, body: JSON.stringify({ email, password }) });
    state.token = data.token;
  } else {
    state.token = password.trim();
    data = await api("/auth/login", { method: "POST" });
  }
  sessionStorage.setItem(storageKey, state.token);
  await bootstrap();
}

async function addAccounts() {
  const tokens = $("#account-tokens").value.split(/\s+/).map((x) => x.trim()).filter(Boolean);
  if (!tokens.length) return;
  await api("/api/accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ tokens }) });
  $("#account-tokens").value = "";
  await loadAccounts();
}

async function refreshAccounts(refs = []) {
  await api("/api/accounts/refresh", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token_refs: refs }) });
  await loadAccounts();
}

async function createUser() {
  const email = $("#user-email").value.trim();
  const password = $("#user-password").value;
  const role = $("#user-role").value;
  if (!email || !password) return;
  await api("/api/users", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email, password, role }) });
  $("#user-email").value = "";
  $("#user-password").value = "";
  await loadUsers();
}

async function createKey() {
  const name = $("#key-name").value.trim() || "API Key";
  const data = await api(usesLegacyKeyAPI() ? "/api/auth/users" : "/api/me/api-keys", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
  $("#new-key").classList.remove("hidden");
  $("#new-key").textContent = "";
  const label = document.createElement("span");
  label.textContent = "新 Key 只显示一次：";
  const value = document.createElement("span");
  value.className = "mono";
  value.textContent = data.key;
  $("#new-key").append(label, value);
  $("#key-name").value = "";
  await loadKeys();
}

async function saveSettings() {
  let payload;
  try {
    payload = JSON.parse($("#settings-json").value);
  } catch (err) {
    throw new Error(`设置 JSON 无效：${err.message}`);
  }
  const data = await api("/api/settings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
  state.settings = data.config || {};
  $("#settings-json").value = JSON.stringify(state.settings, null, 2);
}

async function createImageTask() {
  const prompt = $("#task-prompt").value.trim();
  if (!prompt) return;
  const body = {
    client_task_id: taskID(),
    prompt,
    model: $("#task-model").value.trim() || "gpt-image-2",
    size: $("#task-size").value.trim(),
  };
  await api("/api/image-tasks/generations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  $("#task-prompt").value = "";
  await loadTasks();
}

async function runPlayground() {
  const endpoint = $("#play-endpoint").value;
  const prompt = $("#play-prompt").value;
  const canStream = endpoint !== "/v1/images/generations";
  const stream = canStream && $("#play-stream").checked;
  const output = $("#play-output");
  output.textContent = "";
  let payload;
  if (endpoint === "/v1/responses") payload = { model: "auto", input: prompt, stream };
  else if (endpoint === "/v1/messages") payload = { model: "auto", messages: [{ role: "user", content: prompt }], stream };
  else if (endpoint === "/v1/images/generations") payload = { model: "gpt-image-2", prompt, response_format: "url", n: 1 };
  else if (endpoint === "/v1/complete") payload = { model: "auto", prompt, stream };
  else payload = { model: "auto", messages: [{ role: "user", content: prompt }], stream };

  if (!stream) {
    const data = await api(endpoint, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    output.textContent = JSON.stringify(data, null, 2);
    return;
  }
  const res = await fetch(endpoint, { method: "POST", headers: authHeaders({ "Content-Type": "application/json" }), body: JSON.stringify(payload), credentials: "same-origin" });
  if (!res.ok || !res.body) throw new Error(await res.text());
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    output.textContent += decoder.decode(value, { stream: true });
    output.scrollTop = output.scrollHeight;
  }
}

document.addEventListener("click", async (event) => {
  const target = event.target;
  if (!(target instanceof HTMLElement)) return;
  if (target.matches(".nav")) setTab(target.dataset.tab);
  if (target.id === "logout") {
    sessionStorage.removeItem(storageKey);
    state.token = "";
    show("login");
  }
  if (target.id === "refresh-all") guarded("refresh-all", refreshAll);
  if (target.id === "add-accounts") guarded("add-accounts", addAccounts);
  if (target.id === "refresh-accounts") guarded("refresh-accounts", () => refreshAccounts());
  if (target.id === "create-user") guarded("create-user", createUser);
  if (target.id === "create-key") guarded("create-key", createKey);
  if (target.id === "save-settings") guarded("save-settings", saveSettings);
  if (target.id === "load-logs") guarded("load-logs", loadLogs);
  if (target.id === "clear-logs") guarded("clear-logs", async () => {
    const ids = $$("[data-log-id]:checked").map((input) => input.dataset.logId).filter(Boolean);
    if (!ids.length) return;
    await api("/api/logs/delete", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids }) });
    await loadLogs();
  });
  if (target.id === "load-tasks") guarded("load-tasks", loadTasks);
  if (target.id === "create-task") guarded("create-task", createImageTask);
  if (target.id === "load-images") guarded("load-images", loadImages);
  if (target.id === "delete-images") guarded("delete-images", async () => {
    const paths = $$("[data-image-path]:checked").map((input) => input.dataset.imagePath).filter(Boolean);
    if (!paths.length || !confirm("删除选中的图片？")) return;
    await api("/api/images/delete", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ paths }) });
    await loadImages();
  });
  if (target.id === "run-play") guarded("run-play", runPlayground);
  if (target.dataset.refreshAccount) guarded("refresh-accounts", () => refreshAccounts([target.dataset.refreshAccount]));
  if (target.dataset.deleteAccount && confirm("删除这个账号？")) {
    await guarded("delete-account", async () => {
      await api("/api/accounts", { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token_refs: [target.dataset.deleteAccount] }) });
      await loadAccounts();
    });
  }
  if (target.dataset.toggleKey) {
    await guarded("toggle-key", async () => {
      const path = usesLegacyKeyAPI() ? `/api/auth/users/${target.dataset.toggleKey}` : `/api/me/api-keys/${target.dataset.toggleKey}`;
      await api(path, { method: usesLegacyKeyAPI() ? "POST" : "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: target.dataset.keyEnabled !== "true" }) });
      await loadKeys();
    });
  }
  if (target.dataset.deleteKey && confirm("删除这个 API Key？")) {
    await guarded("delete-key", async () => {
      const path = usesLegacyKeyAPI() ? `/api/auth/users/${target.dataset.deleteKey}` : `/api/me/api-keys/${target.dataset.deleteKey}`;
      await api(path, { method: "DELETE" });
      await loadKeys();
    });
  }
  if (target.dataset.toggleUser) {
    await guarded("toggle-user", async () => {
      const status = target.dataset.userStatus === "active" ? "disabled" : "active";
      await api(`/api/users/${target.dataset.toggleUser}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ status }) });
      await loadUsers();
    });
  }
});

$("#login-form").addEventListener("submit", (event) => guarded("login-submit", () => login(event)));
$("#play-endpoint").addEventListener("change", () => {
  $("#play-stream").disabled = $("#play-endpoint").value === "/v1/images/generations";
});
bootstrap();
