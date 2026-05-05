import React, { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  Ban,
  CheckCircle2,
  Copy,
  ImageIcon,
  KeyRound,
  LoaderCircle,
  LogOut,
  Play,
  RefreshCw,
  Search,
  Settings,
  Trash2,
  Upload,
  Users,
  WandSparkles,
  X
} from "lucide-react";
import { api, authHeaders, getStoredToken, request, setStoredToken } from "./api";
import type { Account, ApiKey, Identity, ImageResult, ImageTask, ModelItem, ReferenceImage, Settings as SettingsType, StoredImage, SystemLog, Toast, User } from "./types";
import { classNames, compact, copyText, createID, fileToDataURL, fmtBytes, fmtDate, formatQuota, imageSrc, parseJSON, parseTaskData, quotaSummary, safeJSON, statusClass } from "./utils";
import "./styles.css";

type Tab = "dashboard" | "accounts" | "workbench" | "tasks" | "images" | "playground" | "keys" | "users" | "settings" | "logs";
type WorkbenchItem = {
  id: string;
  status: "queued" | "running" | "success" | "error";
  prompt: string;
  model: string;
  size?: string;
  taskId?: string;
  image?: ImageResult;
  error?: string;
};

const navItems: Array<{ id: Tab; label: string; admin?: boolean; icon: React.ElementType }> = [
  { id: "dashboard", label: "总览", admin: true, icon: Activity },
  { id: "accounts", label: "账号池", admin: true, icon: Users },
  { id: "workbench", label: "图片工作台", icon: WandSparkles },
  { id: "tasks", label: "任务", icon: LoaderCircle },
  { id: "images", label: "图片库", admin: true, icon: ImageIcon },
  { id: "playground", label: "Playground", icon: Play },
  { id: "keys", label: "API Keys", icon: KeyRound },
  { id: "users", label: "用户", admin: true, icon: Users },
  { id: "settings", label: "设置", admin: true, icon: Settings },
  { id: "logs", label: "日志", admin: true, icon: Activity }
];

const pageTitle: Record<Tab, string> = {
  dashboard: "总览",
  accounts: "账号池",
  workbench: "图片工作台",
  tasks: "任务",
  images: "图片库",
  playground: "Playground",
  keys: "API Keys",
  users: "用户",
  settings: "设置",
  logs: "日志"
};

function Badge({ value }: { value: string | boolean | number | undefined }) {
  return <span className={classNames("badge", statusClass(String(value ?? "")))}>{String(value ?? "-")}</span>;
}

function IconButton({ children, className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button className={classNames("icon-button", className)} {...props}>{children}</button>;
}

function App() {
  const [token, setToken] = useState(getStoredToken());
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [activeTab, setActiveTab] = useState<Tab>("dashboard");
  const [version, setVersion] = useState("-");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [keys, setKeys] = useState<ApiKey[]>([]);
  const [models, setModels] = useState<ModelItem[]>([]);
  const [tasks, setTasks] = useState<ImageTask[]>([]);
  const [images, setImages] = useState<StoredImage[]>([]);
  const [settings, setSettings] = useState<SettingsType>({});
  const [logs, setLogs] = useState<SystemLog[]>([]);
  const [storageStatus, setStorageStatus] = useState("-");
  const [busy, setBusy] = useState<string | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [lightbox, setLightbox] = useState<{ src: string; title?: string } | null>(null);

  const isAdmin = identity?.role === "admin";
  const legacyKeys = identity?.auth_type === "legacy";

  function toast(type: Toast["type"], message: string) {
    const id = createID("toast");
    setToasts((items) => [...items, { id, type, message }]);
    window.setTimeout(() => setToasts((items) => items.filter((item) => item.id !== id)), 3200);
  }

  async function runBusy(id: string, fn: () => Promise<void>) {
    if (busy) return;
    setBusy(id);
    try {
      await fn();
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "操作失败");
    } finally {
      setBusy(null);
    }
  }

  async function bootstrap(currentToken = token) {
    if (!currentToken) return;
    const me = await api.me(currentToken);
    setIdentity(me.identity);
    setVersion("connected");
    if (me.identity.role !== "admin" && (activeTab === "dashboard" || activeTab === "accounts" || activeTab === "users" || activeTab === "settings" || activeTab === "logs" || activeTab === "images")) {
      setActiveTab("workbench");
    }
    await refreshAll(currentToken, me.identity.role === "admin", me.identity.auth_type === "legacy");
  }

  async function refreshAll(currentToken = token, admin = isAdmin, legacy = legacyKeys) {
    const common = [
      api.models(currentToken).then((data) => setModels(data.data || [])),
      api.tasks(currentToken).then((data) => setTasks(data.items || [])),
      api.keys(currentToken, Boolean(legacy)).then((data) => setKeys(data.items || []))
    ];
    const adminLoads = admin
      ? [
          api.accounts(currentToken).then((data) => setAccounts(data.items || [])),
          api.users(currentToken).then((data) => setUsers(data.items || [])),
          api.images(currentToken).then((data) => setImages(data.items || [])),
          api.settings(currentToken).then((data) => setSettings(data.config || {})),
          api.logs(currentToken).then((data) => setLogs(data.items || [])),
          api.storage(currentToken).then((data) => setStorageStatus(`${data.backend.type} · ${data.health.status}`))
        ]
      : [];
    await Promise.allSettled([...common, ...adminLoads]);
  }

  useEffect(() => {
    if (!token) return;
    bootstrap(token).catch(() => {
      setStoredToken("");
      setToken("");
      setIdentity(null);
    });
  }, []);

  async function handleLogin(email: string, password: string) {
    if (!password.trim()) return;
    if (email.trim()) {
      const data = await api.loginWithPassword(email.trim(), password);
      setStoredToken(data.token);
      setToken(data.token);
      await bootstrap(data.token);
      return;
    }
    const raw = password.trim();
    await api.loginWithToken(raw);
    setStoredToken(raw);
    setToken(raw);
    await bootstrap(raw);
  }

  function logout() {
    setStoredToken("");
    setToken("");
    setIdentity(null);
    setActiveTab("dashboard");
  }

  if (!token || !identity) {
    return <LoginView busy={busy === "login"} onLogin={(email, password) => runBusy("login", () => handleLogin(email, password))} />;
  }

  const visibleNav = navItems.filter((item) => !item.admin || isAdmin);

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">GI</div>
          <div>
            <strong>GPT Image Web</strong>
            <span>{version}</span>
          </div>
        </div>
        <nav className="nav-list">
          {visibleNav.map((item) => {
            const Icon = item.icon;
            return (
              <button key={item.id} className={classNames("nav-item", activeTab === item.id && "active")} onClick={() => setActiveTab(item.id)}>
                <Icon size={17} />
                <span>{item.label}</span>
              </button>
            );
          })}
        </nav>
        <div className="identity-card">
          <strong>{identity.name || "User"}</strong>
          <span>{identity.role} · {identity.auth_type}</span>
        </div>
        <button className="ghost full" onClick={logout}><LogOut size={16} />退出</button>
      </aside>

      <main className="workspace">
        <header className="topbar">
          <div>
            <p>{activeTab}</p>
            <h1>{pageTitle[activeTab]}</h1>
          </div>
          <div className="top-actions">
            <span className="status-pill">online</span>
            <button className="secondary" disabled={busy === "refresh"} onClick={() => runBusy("refresh", () => refreshAll())}>
              {busy === "refresh" ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}
              刷新
            </button>
          </div>
        </header>

        {activeTab === "dashboard" && isAdmin && <Dashboard accounts={accounts} models={models} tasks={tasks} storageStatus={storageStatus} onReloadModels={() => runBusy("models", async () => setModels((await api.models(token)).data || []))} />}
        {activeTab === "accounts" && isAdmin && <AccountsPanel token={token} accounts={accounts} setAccounts={setAccounts} toast={toast} busy={busy} runBusy={runBusy} />}
        {activeTab === "workbench" && <ImageWorkbench token={token} accounts={accounts} tasks={tasks} setTasks={setTasks} setImages={setImages} toast={toast} openLightbox={(src, title) => setLightbox({ src, title })} />}
        {activeTab === "tasks" && <TasksPanel token={token} tasks={tasks} setTasks={setTasks} openLightbox={(src, title) => setLightbox({ src, title })} toast={toast} />}
        {activeTab === "images" && isAdmin && <ImagesPanel token={token} images={images} setImages={setImages} toast={toast} openLightbox={(src, title) => setLightbox({ src, title })} />}
        {activeTab === "playground" && <Playground token={token} models={models} toast={toast} openLightbox={(src, title) => setLightbox({ src, title })} />}
        {activeTab === "keys" && <KeysPanel token={token} legacy={Boolean(legacyKeys)} keys={keys} setKeys={setKeys} toast={toast} />}
        {activeTab === "users" && isAdmin && <UsersPanel token={token} users={users} setUsers={setUsers} toast={toast} />}
        {activeTab === "settings" && isAdmin && <SettingsPanel token={token} settings={settings} setSettings={setSettings} toast={toast} />}
        {activeTab === "logs" && isAdmin && <LogsPanel token={token} logs={logs} setLogs={setLogs} toast={toast} />}
      </main>

      <div className="toast-stack">
        {toasts.map((item) => <div key={item.id} className={classNames("toast", item.type)}>{item.message}</div>)}
      </div>
      {lightbox && (
        <div className="lightbox" onClick={() => setLightbox(null)}>
          <button className="lightbox-close" onClick={() => setLightbox(null)}><X size={20} /></button>
          <img src={lightbox.src} alt={lightbox.title || "preview"} />
        </div>
      )}
    </div>
  );
}

function LoginView({ busy, onLogin }: { busy: boolean; onLogin: (email: string, password: string) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  function submit(event: FormEvent) {
    event.preventDefault();
    onLogin(email, password);
  }
  return (
    <main className="login-view">
      <form className="login-panel" onSubmit={submit}>
        <div className="brand login-brand">
          <div className="brand-mark">GI</div>
          <div>
            <strong>GPT Image Web</strong>
            <span>账号池、图片任务与兼容 API 管理台</span>
          </div>
        </div>
        <label><span>Email</span><input value={email} onChange={(event) => setEmail(event.target.value)} autoComplete="username" placeholder="admin@example.com" /></label>
        <label><span>Password / Admin Key</span><input value={password} onChange={(event) => setPassword(event.target.value)} type="password" autoComplete="current-password" placeholder="密码或 legacy admin key" /></label>
        <button disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : null}登录</button>
        <p className="hint">不填写 Email 时，密码框内容会作为 Bearer Key 校验。</p>
      </form>
    </main>
  );
}

function Dashboard({ accounts, models, tasks, storageStatus, onReloadModels }: { accounts: Account[]; models: ModelItem[]; tasks: ImageTask[]; storageStatus: string; onReloadModels: () => void }) {
  const normal = accounts.filter((item) => item.status === "正常").length;
  const success = accounts.reduce((sum, item) => sum + Number(item.success || 0), 0);
  const fail = accounts.reduce((sum, item) => sum + Number(item.fail || 0), 0);
  const recent = tasks[0];
  return (
    <div className="stack">
      <div className="metrics">
        <Metric label="账号总数" value={accounts.length} />
        <Metric label="正常账号" value={normal} tone="ok" />
        <Metric label="可用额度" value={quotaSummary(accounts)} />
        <Metric label="任务总数" value={tasks.length} />
        <Metric label="图片成功" value={success} tone="ok" />
        <Metric label="失败" value={fail} tone="err" />
      </div>
      <div className="dashboard-grid">
        <section className="panel">
          <PanelHead title="模型" subtitle="当前兼容接口暴露的模型" action={<button className="secondary small" onClick={onReloadModels}>刷新</button>} />
          <div className="chips">{models.slice(0, 40).map((model) => <span className="chip" key={model.id}>{model.id}</span>)}</div>
        </section>
        <section className="panel">
          <PanelHead title="系统状态" subtitle="关键运行指标" />
          <div className="status-list">
            <div><span>存储</span><strong>{storageStatus}</strong></div>
            <div><span>账号池</span><strong>{normal}/{accounts.length} 正常</strong></div>
            <div><span>最近任务</span><strong>{recent ? `${recent.status} · ${fmtDate(recent.updated_at)}` : "-"}</strong></div>
          </div>
        </section>
      </div>
    </div>
  );
}

function Metric({ label, value, tone }: { label: string; value: string | number; tone?: string }) {
  return <article className="metric"><span>{label}</span><strong className={tone}>{value}</strong></article>;
}

function PanelHead({ title, subtitle, action }: { title: string; subtitle?: string; action?: React.ReactNode }) {
  return (
    <div className="panel-head">
      <div><h2>{title}</h2>{subtitle ? <p>{subtitle}</p> : null}</div>
      {action ? <div className="actions">{action}</div> : null}
    </div>
  );
}

function AccountsPanel({ token, accounts, setAccounts, toast, busy, runBusy }: { token: string; accounts: Account[]; setAccounts: (items: Account[]) => void; toast: (type: Toast["type"], message: string) => void; busy: string | null; runBusy: (id: string, fn: () => Promise<void>) => Promise<void> }) {
  const [tokens, setTokens] = useState("");
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [type, setType] = useState("");
  const [pageSize, setPageSize] = useState(25);
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<string[]>([]);

  const types = useMemo(() => Array.from(new Set(accounts.map((item) => item.type || "Free"))), [accounts]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return accounts.filter((item) => {
      const text = `${item.email || ""} ${item.token_ref} ${item.access_token_masked} ${item.type}`.toLowerCase();
      return (!q || text.includes(q)) && (!status || item.status === status) && (!type || (item.type || "Free") === type);
    });
  }, [accounts, query, status, type]);
  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const current = filtered.slice((page - 1) * pageSize, page * pageSize);
  const selectedSet = new Set(selected);

  useEffect(() => setPage(1), [query, status, type, pageSize]);

  async function importTokens() {
    const values = tokens.split(/\s+/).map((item) => item.trim()).filter(Boolean);
    if (!values.length) return;
    const data = await api.addAccounts(token, values);
    setAccounts(data.items);
    setTokens("");
    toast("success", `新增 ${data.added} 个，跳过 ${data.skipped} 个`);
  }
  async function refresh(refs = selected) {
    const data = await api.refreshAccounts(token, refs);
    setAccounts(data.items);
    toast(data.errors.length ? "error" : "success", `刷新成功 ${data.refreshed} 个${data.errors.length ? `，失败 ${data.errors.length} 个` : ""}`);
  }
  async function remove(refs: string[]) {
    if (!refs.length || !confirm(`删除 ${refs.length} 个账号？`)) return;
    const data = await api.deleteAccounts(token, refs);
    setAccounts(data.items);
    setSelected([]);
    toast("success", `已删除 ${data.removed} 个账号`);
  }
  async function update(ref: string, body: { status?: string }) {
    const data = await api.updateAccount(token, ref, body);
    setAccounts(data.items);
  }

  return (
    <section className="panel">
      <PanelHead title="账号池" subtitle="导入、筛选、刷新和维护 ChatGPT access_token" action={<><button className="secondary" onClick={() => runBusy("refresh-all-accounts", () => refresh([]))}>刷新全部</button><button className="secondary-danger" onClick={() => runBusy("remove-bad", () => remove(accounts.filter((item) => item.status === "异常").map((item) => item.token_ref)))}>移除异常</button></>} />
      <div className="account-import"><textarea value={tokens} onChange={(event) => setTokens(event.target.value)} placeholder="每行一个 access_token，支持批量粘贴" /><button disabled={busy === "import"} onClick={() => runBusy("import", importTokens)}><Upload size={16} />导入账号</button></div>
      <div className="filters">
        <div className="searchbox"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索邮箱、token ref、类型" /></div>
        <select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">全部状态</option><option>正常</option><option>限流</option><option>异常</option><option>禁用</option></select>
        <select value={type} onChange={(event) => setType(event.target.value)}><option value="">全部类型</option>{types.map((item) => <option key={item}>{item}</option>)}</select>
        <select value={pageSize} onChange={(event) => setPageSize(Number(event.target.value))}><option>10</option><option>25</option><option>50</option><option>100</option></select>
      </div>
      <div className="bulkbar">
        <label className="inline"><input type="checkbox" checked={current.length > 0 && current.every((item) => selectedSet.has(item.token_ref))} onChange={(event) => {
          setSelected((prev) => event.target.checked ? Array.from(new Set([...prev, ...current.map((item) => item.token_ref)])) : prev.filter((ref) => !current.some((item) => item.token_ref === ref)));
        }} /><span>选择当前页</span></label>
        <span>已选择 {selected.length} 项</span>
        <button className="ghost small" disabled={!selected.length} onClick={() => runBusy("refresh-selected", () => refresh(selected))}>刷新选中</button>
        <button className="ghost small" disabled={!selected.length} onClick={() => runBusy("disable-selected", async () => { for (const ref of selected) await update(ref, { status: "禁用" }); toast("success", "已停用选中账号"); })}>停用选中</button>
        <button className="danger small" disabled={!selected.length} onClick={() => runBusy("delete-selected", () => remove(selected))}>删除选中</button>
      </div>
      <div className="table-wrap">
        <table className="accounts-table">
          <thead><tr><th></th><th>Token</th><th>Email</th><th>类型</th><th>状态</th><th>额度</th><th>恢复</th><th>成功/失败</th><th>最近使用</th><th></th></tr></thead>
          <tbody>{current.map((item) => (
            <tr key={item.token_ref}>
              <td><input type="checkbox" checked={selectedSet.has(item.token_ref)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, item.token_ref] : prev.filter((ref) => ref !== item.token_ref))} /></td>
              <td><code>{item.access_token_masked || item.token_ref}</code><small>{item.token_ref}</small></td>
              <td>{item.email || "-"}</td>
              <td>{item.type || "Free"}</td>
              <td><Badge value={item.status} /></td>
              <td>{formatQuota(item)}</td>
              <td>{item.restore_at || "-"}</td>
              <td>{item.success}/{item.fail}</td>
              <td>{fmtDate(item.last_used_at)}</td>
              <td className="row-actions">
                <IconButton title="刷新" onClick={() => runBusy("refresh-one", () => refresh([item.token_ref]))}><RefreshCw size={15} /></IconButton>
                <IconButton title={item.status === "禁用" ? "启用" : "禁用"} onClick={() => runBusy("toggle-one", async () => update(item.token_ref, { status: item.status === "禁用" ? "正常" : "禁用" }))}><Ban size={15} /></IconButton>
                <IconButton title="删除" className="danger-icon" onClick={() => runBusy("delete-one", () => remove([item.token_ref]))}><Trash2 size={15} /></IconButton>
              </td>
            </tr>
          ))}</tbody>
        </table>
      </div>
      <div className="pager"><button className="ghost small" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>上一页</button><span>{page} / {pageCount} · {filtered.length} 项</span><button className="ghost small" disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))}>下一页</button></div>
    </section>
  );
}

function ImageWorkbench({ token, accounts, tasks, setTasks, setImages, toast, openLightbox }: { token: string; accounts: Account[]; tasks: ImageTask[]; setTasks: (items: ImageTask[]) => void; setImages: React.Dispatch<React.SetStateAction<StoredImage[]>>; toast: (type: Toast["type"], message: string) => void; openLightbox: (src: string, title?: string) => void }) {
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState("gpt-image-2");
  const [size, setSize] = useState("");
  const [count, setCount] = useState(1);
  const [asyncMode, setAsyncMode] = useState(true);
  const [refs, setRefs] = useState<ReferenceImage[]>([]);
  const [items, setItems] = useState<WorkbenchItem[]>([]);
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  async function addFiles(files: File[]) {
    const next = await Promise.all(files.filter((file) => file.type.startsWith("image/")).map(async (file) => ({ id: createID("ref"), name: file.name, file, dataUrl: await fileToDataURL(file) })));
    setRefs((current) => [...current, ...next].slice(0, 8));
  }

  useEffect(() => {
    const activeIds = items.flatMap((item) => item.taskId && (item.status === "queued" || item.status === "running") ? [item.taskId] : []);
    if (!activeIds.length) return;
    const timer = window.setInterval(async () => {
      try {
        const data = await api.tasks(token, activeIds);
        setTasks(data.items);
        setItems((current) => current.map((item) => {
          if (!item.taskId) return item;
          const task = data.items.find((candidate) => candidate.id === item.taskId);
          if (!task) return item;
          const result = parseTaskData(task.data)[0];
          return {
            ...item,
            status: task.status === "success" ? "success" : task.status === "error" ? "error" : task.status,
            image: result || item.image,
            error: task.error || item.error
          };
        }));
        if (data.items.some((task) => task.status === "success")) {
          api.images(token).then((data) => setImages(data.items || [])).catch(() => {});
        }
      } catch {
        // Keep polling quiet; task rows show the last known state.
      }
    }, 2500);
    return () => window.clearInterval(timer);
  }, [items, token]);

  async function submit() {
    const text = prompt.trim();
    if (!text) return;
    setBusy(true);
    try {
      const baseItems: WorkbenchItem[] = Array.from({ length: Math.max(1, Math.min(4, count)) }, () => ({ id: createID("img"), status: "queued", prompt: text, model, size }));
      setItems((current) => [...baseItems, ...current]);
      if (asyncMode) {
        if (refs.length) {
          for (const item of baseItems) {
            const form = new FormData();
            form.set("client_task_id", item.id);
            form.set("prompt", text);
            form.set("model", model);
            if (size) form.set("size", size);
            refs.forEach((ref) => form.append("image", ref.file, ref.name));
            const task = await api.createEditTask(token, form);
            item.taskId = task.id;
          }
        } else {
          for (const item of baseItems) {
            const task = await api.createGenerationTask(token, { client_task_id: item.id, prompt: text, model, size, n: 1 });
            item.taskId = task.id;
          }
        }
        setItems((current) => current.map((row) => baseItems.find((item) => item.id === row.id) || row));
        toast("success", `已提交 ${baseItems.length} 个任务`);
      } else {
        if (refs.length) {
          const form = new FormData();
          form.set("prompt", text);
          form.set("model", model);
          form.set("response_format", "url");
          form.set("n", String(count));
          if (size) form.set("size", size);
          refs.forEach((ref) => form.append("image", ref.file, ref.name));
          const data = await request<{ data: ImageResult[] }>(token, "/v1/images/edits", { method: "POST", body: form });
          const nextItems: WorkbenchItem[] = data.data.map((image, index) => ({ ...(baseItems[index] || baseItems[0]), id: createID("img"), status: "success", image }));
          setItems((current) => nextItems.concat(current.filter((row) => !baseItems.some((item) => item.id === row.id))));
        } else {
          const data = await request<{ data: ImageResult[] }>(token, "/v1/images/generations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ prompt: text, model, size, n: count, response_format: "url" }) });
          const nextItems: WorkbenchItem[] = data.data.map((image) => ({ id: createID("img"), status: "success", prompt: text, model, size, image }));
          setItems((current) => nextItems.concat(current.filter((row) => !baseItems.some((item) => item.id === row.id))));
        }
        api.images(token).then((data) => setImages(data.items || [])).catch(() => {});
      }
    } catch (error) {
      setItems((current) => current.map((item) => item.status === "queued" ? { ...item, status: "error", error: error instanceof Error ? error.message : "生成失败" } : item));
      throw error;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="image-workbench">
      <section className="panel composer">
        <PanelHead title="图片工作台" subtitle="文生图、参考图编辑、异步任务和本地结果预览" action={<span className="pill">额度 {quotaSummary(accounts)}</span>} />
        <input ref={fileRef} className="hidden" type="file" accept="image/*" multiple onChange={(event) => addFiles(Array.from(event.target.files || []))} />
        <div className="reference-strip">
          {refs.map((ref) => <button key={ref.id} className="reference-thumb" onClick={() => openLightbox(ref.dataUrl, ref.name)}><img src={ref.dataUrl} alt={ref.name} /><span onClick={(event) => { event.stopPropagation(); setRefs((items) => items.filter((item) => item.id !== ref.id)); }}><X size={12} /></span></button>)}
        </div>
        <textarea className="prompt-box" value={prompt} onChange={(event) => setPrompt(event.target.value)} onPaste={(event) => {
          const files = Array.from(event.clipboardData.files).filter((file) => file.type.startsWith("image/"));
          if (files.length) {
            event.preventDefault();
            addFiles(files);
          }
        }} placeholder={refs.length ? "描述你希望如何修改参考图" : "输入你想要生成的画面，也可直接粘贴图片"} />
        <div className="composer-controls">
          <button className="secondary" onClick={() => fileRef.current?.click()}><Upload size={16} />上传参考图</button>
          <label><span>模型</span><input value={model} onChange={(event) => setModel(event.target.value)} /></label>
          <label><span>比例</span><select value={size} onChange={(event) => setSize(event.target.value)}><option value="">自动</option><option>1:1</option><option>16:9</option><option>9:16</option><option>4:3</option><option>3:4</option></select></label>
          <label><span>张数</span><input type="number" min={1} max={4} value={count} onChange={(event) => setCount(Number(event.target.value))} /></label>
          <label className="inline"><input type="checkbox" checked={asyncMode} onChange={(event) => setAsyncMode(event.target.checked)} /><span>异步任务</span></label>
          <button disabled={busy || !prompt.trim()} onClick={submit}>{busy ? <LoaderCircle className="spin" size={16} /> : <WandSparkles size={16} />}{refs.length ? "编辑" : "生成"}</button>
        </div>
      </section>
      <section className="panel results-panel">
        <PanelHead title="结果" subtitle={`${items.length} 个结果/任务`} action={<><button className="secondary small" onClick={() => setItems([])}>清空</button><button className="secondary small" onClick={() => api.tasks(token).then((data) => setTasks(data.items || []))}>同步任务</button></>} />
        <div className={classNames("result-grid", items.length === 0 && "empty")}>
          {items.length === 0 ? "暂无结果" : items.map((item) => <ResultCard key={item.id} item={item} openLightbox={openLightbox} />)}
        </div>
      </section>
    </div>
  );
}

function ResultCard({ item, openLightbox }: { item: WorkbenchItem; openLightbox: (src: string, title?: string) => void }) {
  const src = item.image ? imageSrc(item.image) : "";
  return (
    <article className="result-card">
      {src ? <button onClick={() => openLightbox(src, item.prompt)}><img src={src} alt={item.prompt} /></button> : <div className="result-placeholder">{item.status === "error" ? <X size={22} /> : <LoaderCircle className="spin" size={22} />}</div>}
      <div><Badge value={item.status} /><span>{item.model}</span>{item.size ? <span>{item.size}</span> : null}</div>
      {item.error ? <p className="error-text">{item.error}</p> : null}
    </article>
  );
}

function TasksPanel({ token, tasks, setTasks, openLightbox, toast }: { token: string; tasks: ImageTask[]; setTasks: (items: ImageTask[]) => void; openLightbox: (src: string, title?: string) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const rows = tasks.filter((item) => {
    const text = `${item.id} ${item.mode} ${item.status} ${item.model} ${item.error}`.toLowerCase();
    return (!query || text.includes(query.toLowerCase())) && (!status || item.status === status);
  });
  return (
    <section className="panel">
      <PanelHead title="图片任务" subtitle="查看异步任务状态和返回数据" action={<button className="secondary" onClick={() => api.tasks(token).then((data) => { setTasks(data.items || []); toast("success", "任务已刷新"); })}>刷新任务</button>} />
      <div className="filters"><div className="searchbox"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索任务 ID、模型、状态" /></div><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">全部状态</option><option>queued</option><option>running</option><option>success</option><option>error</option></select></div>
      <div className="table-wrap"><table><thead><tr><th>ID</th><th>Mode</th><th>Status</th><th>Model</th><th>Size</th><th>Result</th><th>Error</th><th>Updated</th></tr></thead><tbody>{rows.map((task) => {
        const first = parseTaskData(task.data)[0];
        const src = first ? imageSrc(first) : "";
        return <tr key={task.id}><td><code>{task.id}</code></td><td>{task.mode}</td><td><Badge value={task.status} /></td><td>{task.model || "-"}</td><td>{task.size || "-"}</td><td>{src ? <button className="link-button" onClick={() => openLightbox(src, task.id)}>预览</button> : "-"}</td><td className="error-cell">{task.error || "-"}</td><td>{fmtDate(task.updated_at)}</td></tr>;
      })}</tbody></table></div>
    </section>
  );
}

function ImagesPanel({ token, images, setImages, toast, openLightbox }: { token: string; images: StoredImage[]; setImages: (items: StoredImage[]) => void; toast: (type: Toast["type"], message: string) => void; openLightbox: (src: string, title?: string) => void }) {
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState("new");
  const [selected, setSelected] = useState<string[]>([]);
  const items = [...images].filter((item) => `${item.name} ${item.path}`.toLowerCase().includes(query.toLowerCase())).sort((a, b) => sort === "old" ? new Date(a.created_at).getTime() - new Date(b.created_at).getTime() : sort === "large" ? b.size - a.size : new Date(b.created_at).getTime() - new Date(a.created_at).getTime());
  async function remove() {
    if (!selected.length || !confirm(`删除 ${selected.length} 张图片？`)) return;
    const data = await api.deleteImages(token, selected);
    setSelected([]);
    setImages((await api.images(token)).items || []);
    toast("success", `已删除 ${data.removed} 张图片`);
  }
  return (
    <section className="panel">
      <PanelHead title="图片库" subtitle="本地归档图片，支持预览、复制链接和批量删除" action={<><button className="secondary" onClick={() => api.images(token).then((data) => setImages(data.items || []))}>刷新图片</button><button className="danger" disabled={!selected.length} onClick={remove}>删除选中</button></>} />
      <div className="filters"><div className="searchbox"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索文件名或路径" /></div><select value={sort} onChange={(event) => setSort(event.target.value)}><option value="new">最新优先</option><option value="old">最早优先</option><option value="large">文件最大</option></select></div>
      <div className="image-grid">{items.map((item) => {
        const copyURL = item.url.startsWith("http") ? item.url : `${location.origin}${item.url}`;
        return <article key={item.path} className="image-item"><label><input type="checkbox" checked={selected.includes(item.path)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, item.path] : prev.filter((path) => path !== item.path))} /><span>{item.name}</span></label><button onClick={() => openLightbox(item.url, item.name)}><img src={item.url} alt={item.name} loading="lazy" /></button><div><span>{fmtDate(item.created_at)} · {fmtBytes(item.size)}</span><IconButton onClick={() => copyText(copyURL).then(() => toast("success", "已复制链接"))}><Copy size={14} /></IconButton></div></article>;
      })}</div>
    </section>
  );
}

function Playground({ token, models, toast, openLightbox }: { token: string; models: ModelItem[]; toast: (type: Toast["type"], message: string) => void; openLightbox: (src: string, title?: string) => void }) {
  const [endpoint, setEndpoint] = useState("/v1/chat/completions");
  const [stream, setStream] = useState("true");
  const [model, setModel] = useState("auto");
  const [payload, setPayload] = useState("");
  const [output, setOutput] = useState("");
  const [meta, setMeta] = useState("未运行");
  const [images, setImages] = useState<ImageResult[]>([]);
  const [busy, setBusy] = useState(false);

  function buildPayload(nextEndpoint = endpoint, nextModel = model, nextStream = stream) {
    const streaming = nextStream === "true" && nextEndpoint !== "/v1/images/generations";
    if (nextEndpoint === "/v1/responses") return safeJSON({ model: nextModel, input: "只回复 OK", stream: streaming });
    if (nextEndpoint === "/v1/messages") return safeJSON({ model: nextModel, messages: [{ role: "user", content: "只回复 OK" }], stream: streaming });
    if (nextEndpoint === "/v1/images/generations") return safeJSON({ model: "gpt-image-2", prompt: "一只透明玻璃杯里的蓝色星光", response_format: "url", n: 1, size: "1:1" });
    if (nextEndpoint === "/v1/complete") return safeJSON({ model: nextModel, prompt: "只回复 OK", stream: streaming });
    return safeJSON({ model: nextModel, messages: [{ role: "user", content: "只回复 OK" }], stream: streaming });
  }

  useEffect(() => setPayload(buildPayload()), []);

  async function run() {
    setBusy(true);
    setOutput("");
    setImages([]);
    const start = performance.now();
    try {
      const body = parseJSON(payload) as Record<string, unknown>;
      if (endpoint !== "/v1/images/generations" && stream === "true") {
        body.stream = true;
        const res = await fetch(endpoint, { method: "POST", headers: authHeaders(token, { "Content-Type": "application/json" }), body: JSON.stringify(body), credentials: "same-origin" });
        if (!res.ok || !res.body) throw new Error(await res.text());
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          setOutput((current) => current + decoder.decode(value, { stream: true }));
        }
      } else {
        const data = await request<{ data?: ImageResult[] } | unknown>(token, endpoint, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
        setOutput(safeJSON(data));
        if (typeof data === "object" && data && "data" in data && Array.isArray((data as { data?: unknown }).data)) setImages((data as { data: ImageResult[] }).data);
      }
      setMeta(`${Math.round(performance.now() - start)} ms`);
    } catch (error) {
      setMeta("失败");
      throw error;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="playground-grid">
      <section className="panel">
        <PanelHead title="Playground" subtitle="直接调试兼容 API，支持流式文本和图片预览" action={<button className="secondary small" onClick={() => setPayload(safeJSON(parseJSON(payload)))}>格式化 JSON</button>} />
        <div className="play-controls"><label><span>Endpoint</span><select value={endpoint} onChange={(event) => { setEndpoint(event.target.value); setPayload(buildPayload(event.target.value, model, stream)); }}><option>/v1/chat/completions</option><option>/v1/complete</option><option>/v1/responses</option><option>/v1/messages</option><option>/v1/images/generations</option></select></label><label><span>Model</span><input list="model-list" value={model} onChange={(event) => setModel(event.target.value)} /><datalist id="model-list">{models.map((item) => <option key={item.id}>{item.id}</option>)}</datalist></label><label><span>Stream</span><select value={stream} disabled={endpoint === "/v1/images/generations"} onChange={(event) => { setStream(event.target.value); setPayload(buildPayload(endpoint, model, event.target.value)); }}><option value="true">stream</option><option value="false">json</option></select></label><button className="secondary" onClick={() => setPayload(buildPayload())}>生成请求</button><button disabled={busy} onClick={() => run().catch((error) => toast("error", error instanceof Error ? error.message : "运行失败"))}>{busy ? <LoaderCircle className="spin" size={16} /> : <Play size={16} />}运行</button></div>
        <textarea className="json-editor" value={payload} onChange={(event) => setPayload(event.target.value)} spellCheck={false} />
      </section>
      <section className="panel">
        <PanelHead title="响应" subtitle={meta} action={<button className="secondary small" onClick={() => copyText(output).then(() => toast("success", "已复制响应"))}>复制</button>} />
        {images.length ? <div className="play-image-preview">{images.map((image, index) => { const src = imageSrc(image); return src ? <button key={index} onClick={() => openLightbox(src)}><img src={src} alt={`result ${index + 1}`} /></button> : null; })}</div> : null}
        <pre className="output">{output || "等待运行"}</pre>
      </section>
    </div>
  );
}

function KeysPanel({ token, legacy, keys, setKeys, toast }: { token: string; legacy: boolean; keys: ApiKey[]; setKeys: (items: ApiKey[]) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [name, setName] = useState("");
  const [newKey, setNewKey] = useState("");
  async function reload() { setKeys((await api.keys(token, legacy)).items || []); }
  async function create() { const data = await api.createKey(token, legacy, name.trim() || "API Key"); setNewKey(data.key); setName(""); await reload(); }
  return <section className="panel"><PanelHead title="API Keys" subtitle="为当前用户创建和管理访问密钥" /><div className="toolbar compact"><input value={name} onChange={(event) => setName(event.target.value)} placeholder="Key name" /><button onClick={() => create().then(() => toast("success", "Key 已创建")).catch((error) => toast("error", error.message))}>创建 API Key</button></div>{newKey ? <div className="notice"><span>新 Key 只显示一次：</span><code>{newKey}</code><IconButton onClick={() => copyText(newKey).then(() => toast("success", "已复制"))}><Copy size={14} /></IconButton></div> : null}<div className="table-wrap"><table><thead><tr><th>Name</th><th>Role</th><th>Enabled</th><th>Created</th><th>Last used</th><th></th></tr></thead><tbody>{keys.map((item) => <tr key={item.id}><td>{item.name}</td><td>{item.role}</td><td><Badge value={item.enabled} /></td><td>{fmtDate(item.created_at)}</td><td>{fmtDate(item.last_used_at)}</td><td className="row-actions"><IconButton onClick={() => api.updateKey(token, legacy, item.id, { enabled: !item.enabled }).then(reload)}>{item.enabled ? <Ban size={15} /> : <CheckCircle2 size={15} />}</IconButton><IconButton className="danger-icon" onClick={() => confirm("删除这个 API Key？") && api.deleteKey(token, legacy, item.id).then(reload)}><Trash2 size={15} /></IconButton></td></tr>)}</tbody></table></div></section>;
}

function UsersPanel({ token, users, setUsers, toast }: { token: string; users: User[]; setUsers: (items: User[]) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [form, setForm] = useState({ email: "", name: "", password: "", role: "user" });
  async function reload() { setUsers((await api.users(token)).items || []); }
  async function create() { await api.createUser(token, form); setForm({ email: "", name: "", password: "", role: "user" }); await reload(); toast("success", "用户已创建"); }
  return <section className="panel"><PanelHead title="用户" subtitle="创建用户、调整角色和启停账号" /><div className="toolbar user-toolbar"><input value={form.email} onChange={(event) => setForm({ ...form, email: event.target.value })} placeholder="email" /><input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="name" /><input value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} type="password" placeholder="password" /><select value={form.role} onChange={(event) => setForm({ ...form, role: event.target.value })}><option>user</option><option>admin</option></select><button onClick={() => create().catch((error) => toast("error", error.message))}>创建用户</button></div><div className="table-wrap"><table><thead><tr><th>Email</th><th>Name</th><th>Role</th><th>Status</th><th>Created</th><th>Last login</th><th></th></tr></thead><tbody>{users.map((user) => <tr key={user.id}><td>{user.email}</td><td>{user.name || "-"}</td><td><Badge value={user.role} /></td><td><Badge value={user.status} /></td><td>{fmtDate(user.created_at)}</td><td>{fmtDate(user.last_login_at)}</td><td><button className="secondary small" onClick={() => api.updateUser(token, user.id, { status: user.status === "active" ? "disabled" : "active" }).then(reload)}>{user.status === "active" ? "停用" : "启用"}</button></td></tr>)}</tbody></table></div></section>;
}

function SettingsPanel({ token, settings, setSettings, toast }: { token: string; settings: SettingsType; setSettings: (settings: SettingsType) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [json, setJson] = useState(safeJSON(settings));
  useEffect(() => setJson(safeJSON(settings)), [settings]);
  const aiReview = settings.ai_review && typeof settings.ai_review === "object" ? settings.ai_review as Record<string, unknown> : {};
  function updateField(key: string, value: unknown) { setSettings({ ...settings, [key]: value }); }
  async function save(next = settings) { const data = await api.saveSettings(token, next); setSettings(data.config || {}); toast("success", "设置已保存"); }
  return <div className="settings-layout"><section className="panel"><PanelHead title="常用设置" subtitle="保存后会同步写入配置表" action={<button onClick={() => save().catch((error) => toast("error", error.message))}>保存</button>} /><div className="settings-form"><label><span>Proxy</span><input value={String(settings.proxy || "")} onChange={(event) => updateField("proxy", event.target.value)} /></label><label><span>Base URL</span><input value={String(settings.base_url || "")} onChange={(event) => updateField("base_url", event.target.value)} /></label><label><span>图片保留天数</span><input type="number" value={Number(settings.image_retention_days || 30)} onChange={(event) => updateField("image_retention_days", Number(event.target.value))} /></label><label><span>图片轮询超时</span><input type="number" value={Number(settings.image_poll_timeout_secs || 120)} onChange={(event) => updateField("image_poll_timeout_secs", Number(event.target.value))} /></label><label className="inline"><input type="checkbox" checked={Boolean(settings.auto_remove_invalid_accounts)} onChange={(event) => updateField("auto_remove_invalid_accounts", event.target.checked)} /><span>自动移除异常账号</span></label><label className="inline"><input type="checkbox" checked={Boolean(aiReview.enabled)} onChange={(event) => updateField("ai_review", { ...aiReview, enabled: event.target.checked })} /><span>启用 AI 内容审核</span></label><label className="wide"><span>敏感词，每行一个</span><textarea value={Array.isArray(settings.sensitive_words) ? settings.sensitive_words.join("\n") : ""} onChange={(event) => updateField("sensitive_words", event.target.value.split("\n").map((line) => line.trim()).filter(Boolean))} /></label></div></section><section className="panel"><PanelHead title="原始 JSON" subtitle="高级设置可以直接编辑" action={<button className="secondary" onClick={() => { const parsed = parseJSON(json) as SettingsType; save(parsed).catch((error) => toast("error", error.message)); }}>保存 JSON</button>} /><textarea className="json-editor settings-json" value={json} onChange={(event) => setJson(event.target.value)} spellCheck={false} /></section></div>;
}

function LogsPanel({ token, logs, setLogs, toast }: { token: string; logs: SystemLog[]; setLogs: (items: SystemLog[]) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [type, setType] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  async function load() { setLogs((await api.logs(token, type)).items || []); }
  async function clear() { const data = await api.deleteLogs(token, selected); setSelected([]); await load(); toast("success", `已清理 ${data.removed} 条`); }
  return <section className="panel"><PanelHead title="日志" subtitle="数据库中的调用和账号操作记录" action={<><select value={type} onChange={(event) => setType(event.target.value)}><option value="">全部</option><option value="call">调用</option><option value="account">账号</option></select><button className="secondary" onClick={() => load().catch((error) => toast("error", error.message))}>刷新日志</button><button className="danger" disabled={!selected.length} onClick={() => clear().catch((error) => toast("error", error.message))}>清理选中</button></>} /><div className="table-wrap"><table><thead><tr><th></th><th>Time</th><th>Type</th><th>Summary</th><th>Detail</th></tr></thead><tbody>{logs.map((log) => <tr key={log.id}><td><input type="checkbox" checked={selected.includes(log.id)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, log.id] : prev.filter((id) => id !== log.id))} /></td><td>{fmtDate(log.time)}</td><td>{log.type}</td><td>{log.summary}</td><td><code>{JSON.stringify(log.detail || {}).slice(0, 260)}</code></td></tr>)}</tbody></table></div></section>;
}

createRoot(document.getElementById("root")!).render(<App />);
