import React, { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowUp,
  Ban,
  Clock3,
  Copy,
  Eye,
  EyeOff,
  ImageIcon,
  ImagePlus,
  LayoutDashboard,
  LoaderCircle,
  LogOut,
  MailPlus,
  MessageSquarePlus,
  Play,
  RefreshCw,
  RotateCcw,
  Search,
  Settings,
  Sparkles,
  Trash2,
  Upload,
  Users,
  WandSparkles,
  X
} from "lucide-react";
import { api, authHeaders, getStoredToken, request, setStoredToken } from "./api";
import type { Account, Identity, ImageResult, ImageTask, ModelItem, ReferenceImage, RegisterRuntime, Settings as SettingsType, StoredImage, SystemLog, Toast, User } from "./types";
import { classNames, copyText, createID, fileToDataURL, fmtBytes, fmtDate, formatQuota, imageSrc, parseJSON, parseTaskData, quotaSummary, safeJSON, statusClass } from "./utils";
import "./styles.css";

type Tab = "dashboard" | "accounts" | "register" | "activity" | "images" | "playground" | "users" | "settings";
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

type WorkbenchTurn = {
  id: string;
  prompt: string;
  model: string;
  size?: string;
  count: number;
  mode: "generate" | "edit";
  refs: ReferenceImage[];
  images: WorkbenchItem[];
  status: "queued" | "running" | "success" | "error";
  createdAt: string;
  error?: string;
};

type StoredWorkbenchRef = Omit<ReferenceImage, "file">;
type StoredWorkbenchTurn = Omit<WorkbenchTurn, "refs"> & { refs: StoredWorkbenchRef[] };
type StoredWorkbenchState = {
  version: 1;
  activeTurnId: string | null;
  turns: StoredWorkbenchTurn[];
};

const workbenchStoragePrefix = "gpt_image_web_workbench:";

const navItems: Array<{ id: Tab; label: string; icon: React.ElementType }> = [
  { id: "dashboard", label: "总览", icon: Activity },
  { id: "accounts", label: "账号池", icon: Users },
  { id: "register", label: "注册", icon: MailPlus },
  { id: "activity", label: "任务日志", icon: LoaderCircle },
  { id: "images", label: "图片库", icon: ImageIcon },
  { id: "playground", label: "Playground", icon: Play },
  { id: "users", label: "用户", icon: Users },
  { id: "settings", label: "设置", icon: Settings }
];

const pageTitle: Record<Tab, string> = {
  dashboard: "总览",
  accounts: "账号池",
  register: "注册",
  activity: "任务日志",
  images: "图片库",
  playground: "Playground",
  users: "用户",
  settings: "设置"
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
  const [adminMode, setAdminMode] = useState(false);
  const [version, setVersion] = useState("-");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [models, setModels] = useState<ModelItem[]>([]);
  const [tasks, setTasks] = useState<ImageTask[]>([]);
  const [images, setImages] = useState<StoredImage[]>([]);
  const [settings, setSettings] = useState<SettingsType>({});
  const [registerRuntime, setRegisterRuntime] = useState<RegisterRuntime | null>(null);
  const [logs, setLogs] = useState<SystemLog[]>([]);
  const [storageStatus, setStorageStatus] = useState("-");
  const [busy, setBusy] = useState<string | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [lightbox, setLightbox] = useState<{ src: string; title?: string } | null>(null);
  const [loginError, setLoginError] = useState("");

  const isAdmin = identity?.role === "admin";
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
    if (me.identity.role !== "admin") setAdminMode(false);
    await refreshAll(currentToken, me.identity.role === "admin");
  }

  async function refreshAll(currentToken = token, admin = isAdmin) {
    const common = admin
      ? [
          api.models(currentToken).then((data) => setModels(data.data || [])),
          api.tasks(currentToken).then((data) => setTasks(data.items || []))
        ]
      : [];
    const adminLoads = admin
      ? [
          api.accounts(currentToken).then((data) => setAccounts(data.items || [])),
          api.users(currentToken).then((data) => setUsers(data.items || [])),
          api.images(currentToken).then((data) => setImages(data.items || [])),
          api.settings(currentToken).then((data) => setSettings(data.config || {})),
          api.registerState(currentToken).then((data) => setRegisterRuntime(data)),
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

  useEffect(() => {
    if (!token || !isAdmin || !registerRuntime?.running) return;
    const timer = window.setInterval(() => {
      api.registerState(token).then(setRegisterRuntime).catch(() => {});
    }, 3000);
    return () => window.clearInterval(timer);
  }, [token, isAdmin, registerRuntime?.running]);

  async function handleLogin(email: string, password: string) {
    if (!email.trim() || !password.trim()) {
      throw new Error("请输入邮箱和密码");
    }
    const data = await api.loginWithPassword(email.trim(), password);
    setStoredToken(data.token);
    setToken(data.token);
    await bootstrap(data.token);
  }

  async function submitLogin(email: string, password: string) {
    if (busy) return;
    setBusy("login");
    setLoginError("");
    try {
      await handleLogin(email, password);
    } catch (error) {
      setLoginError(error instanceof Error ? error.message : "登录失败");
    } finally {
      setBusy(null);
    }
  }

  function logout() {
    setStoredToken("");
    setToken("");
    setIdentity(null);
    setActiveTab("dashboard");
    setAdminMode(false);
  }

  if (!token || !identity) {
    return <LoginView busy={busy === "login"} error={loginError} onLogin={submitLogin} />;
  }

  if (!adminMode || !isAdmin) {
    return (
      <ImageHome
        token={token}
        identity={identity}
        isAdmin={Boolean(isAdmin)}
        accounts={accounts}
        setTasks={setTasks}
        setImages={setImages}
        toast={toast}
        logout={logout}
        openAdmin={() => {
          setActiveTab("dashboard");
          setAdminMode(true);
        }}
        openLightbox={(src, title) => setLightbox({ src, title })}
        toasts={toasts}
        lightbox={lightbox}
        closeLightbox={() => setLightbox(null)}
      />
    );
  }

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
          {navItems.map((item) => {
            const Icon = item.icon;
            return (
              <button key={item.id} className={classNames("nav-item", activeTab === item.id && "active")} onClick={() => setActiveTab(item.id)}>
                <Icon size={17} />
                <span>{item.label}</span>
              </button>
            );
          })}
        </nav>
        <button className="ghost full" onClick={() => setAdminMode(false)}><WandSparkles size={16} />图片工作台</button>
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
        {activeTab === "register" && isAdmin && <RegisterPanel token={token} registerRuntime={registerRuntime} setRegisterRuntime={setRegisterRuntime} toast={toast} />}
        {activeTab === "activity" && isAdmin && <ActivityPanel token={token} tasks={tasks} setTasks={setTasks} logs={logs} setLogs={setLogs} openLightbox={(src, title) => setLightbox({ src, title })} toast={toast} />}
        {activeTab === "images" && isAdmin && <ImagesPanel token={token} images={images} setImages={setImages} toast={toast} openLightbox={(src, title) => setLightbox({ src, title })} />}
        {activeTab === "playground" && <Playground token={token} models={models} toast={toast} openLightbox={(src, title) => setLightbox({ src, title })} />}
        {activeTab === "users" && isAdmin && <UsersPanel token={token} users={users} setUsers={setUsers} toast={toast} />}
        {activeTab === "settings" && isAdmin && <SettingsPanel token={token} settings={settings} setSettings={setSettings} toast={toast} />}
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

function LoginView({ busy, error, onLogin }: { busy: boolean; error: string; onLogin: (email: string, password: string) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [localError, setLocalError] = useState("");
  function submit(event: FormEvent) {
    event.preventDefault();
    setLocalError("");
    if (!email.trim() || !password.trim()) {
      setLocalError("请输入邮箱和密码");
      return;
    }
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
        <label><span>Password</span><input value={password} onChange={(event) => setPassword(event.target.value)} type="password" autoComplete="current-password" placeholder="账户密码" /></label>
        {localError || error ? <p className="form-error">{localError || error}</p> : null}
        <button disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : null}登录</button>
        <p className="hint">管理后台创建用户后，会自动生成该用户唯一 API Key；网页登录只使用邮箱密码。</p>
      </form>
    </main>
  );
}

function ImageHome({
  token,
  identity,
  isAdmin,
  accounts,
  setTasks,
  setImages,
  toast,
  logout,
  openAdmin,
  openLightbox,
  toasts,
  lightbox,
  closeLightbox
}: {
  token: string;
  identity: Identity;
  isAdmin: boolean;
  accounts: Account[];
  setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>;
  setImages: React.Dispatch<React.SetStateAction<StoredImage[]>>;
  toast: (type: Toast["type"], message: string) => void;
  logout: () => void;
  openAdmin: () => void;
  openLightbox: (src: string, title?: string) => void;
  toasts: Toast[];
  lightbox: { src: string; title?: string } | null;
  closeLightbox: () => void;
}) {
  return (
    <div className="home-shell">
      <header className="home-header">
        <div className="brand home-brand">
          <div className="brand-mark">GI</div>
          <div>
            <strong>GPT Image Web</strong>
            <span>{identity.name || "User"} · {identity.role}</span>
          </div>
        </div>
        <div className="home-actions">
          <span className="status-pill">online</span>
          {isAdmin ? <button className="secondary" onClick={openAdmin}><LayoutDashboard size={16} />管理后台</button> : null}
          <button className="ghost" onClick={logout}><LogOut size={16} />退出</button>
        </div>
      </header>

      <ImageWorkbench token={token} identity={identity} accounts={accounts} canRefreshArchive={isAdmin} setTasks={setTasks} setImages={setImages} toast={toast} openLightbox={openLightbox} />

      <div className="toast-stack">
        {toasts.map((item) => <div key={item.id} className={classNames("toast", item.type)}>{item.message}</div>)}
      </div>
      {lightbox && (
        <div className="lightbox" onClick={closeLightbox}>
          <button className="lightbox-close" onClick={closeLightbox}><X size={20} /></button>
          <img src={lightbox.src} alt={lightbox.title || "preview"} />
        </div>
      )}
    </div>
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
      const text = `${item.email || ""} ${item.token_ref} ${item.access_token_masked} ${item.password || ""} ${item.type}`.toLowerCase();
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
  async function update(ref: string, body: { status?: string; password?: string }) {
    const data = await api.updateAccount(token, ref, body);
    setAccounts(data.items);
  }

  return (
    <section className="panel">
      <PanelHead title="账号池" subtitle="导入、筛选、刷新和维护 ChatGPT access_token" action={<><button className="secondary" onClick={() => runBusy("refresh-all-accounts", () => refresh([]))}>刷新全部</button><button className="secondary-danger" onClick={() => runBusy("remove-bad", () => remove(accounts.filter((item) => item.status === "异常").map((item) => item.token_ref)))}>移除异常</button></>} />
      <div className="account-import"><textarea value={tokens} onChange={(event) => setTokens(event.target.value)} placeholder="每行一个 access_token，支持批量粘贴" /><button disabled={busy === "import"} onClick={() => runBusy("import", importTokens)}><Upload size={16} />导入账号</button></div>
      <div className="filters">
        <div className="searchbox"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索邮箱、token ref、密码、类型" /></div>
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
      <datalist id="account-type-options">{types.map((value) => <option key={value}>{value}</option>)}</datalist>
      <div className="table-wrap">
        <table className="accounts-table">
          <thead><tr><th></th><th>Token</th><th>Email</th><th>密码</th><th>类型</th><th>状态</th><th>额度</th><th>恢复</th><th>成功/失败</th><th>最近使用</th><th></th></tr></thead>
          <tbody>{current.map((item) => (
            <AccountRow
              key={item.token_ref}
              item={item}
              selected={selectedSet.has(item.token_ref)}
              onSelect={(checked) => setSelected((prev) => checked ? [...prev, item.token_ref] : prev.filter((ref) => ref !== item.token_ref))}
              onRefresh={() => runBusy("refresh-one", () => refresh([item.token_ref]))}
              onToggle={() => runBusy("toggle-one", async () => update(item.token_ref, { status: item.status === "禁用" ? "正常" : "禁用" }))}
              onDelete={() => runBusy("delete-one", () => remove([item.token_ref]))}
              onSave={async (body) => {
                const data = await api.updateAccount(token, item.token_ref, body);
                setAccounts(data.items);
                toast("success", "账号已更新");
              }}
              toast={toast}
            />
          ))}</tbody>
        </table>
      </div>
      <div className="pager"><button className="ghost small" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>上一页</button><span>{page} / {pageCount} · {filtered.length} 项</span><button className="ghost small" disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))}>下一页</button></div>
    </section>
  );
}

function AccountRow({ item, selected, onSelect, onRefresh, onToggle, onDelete, onSave, toast }: { item: Account; selected: boolean; onSelect: (checked: boolean) => void; onRefresh: () => void; onToggle: () => void; onDelete: () => void; onSave: (body: { type?: string; status?: string; quota?: number; password?: string }) => Promise<void>; toast: (type: Toast["type"], message: string) => void }) {
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [revealPassword, setRevealPassword] = useState(false);
  const [draft, setDraft] = useState({ type: item.type || "Free", status: item.status || "正常", quota: String(item.quota ?? 0), password: item.password || "" });
  useEffect(() => {
    if (!editing) setDraft({ type: item.type || "Free", status: item.status || "正常", quota: String(item.quota ?? 0), password: item.password || "" });
  }, [editing, item.type, item.status, item.quota, item.password]);
  async function save() {
    setSaving(true);
    try {
      await onSave({ type: draft.type.trim() || "Free", status: draft.status, quota: Number(draft.quota) || 0, password: draft.password.trim() });
      setEditing(false);
    } finally {
      setSaving(false);
    }
  }
  return (
    <tr>
      <td><input type="checkbox" checked={selected} onChange={(event) => onSelect(event.target.checked)} /></td>
      <td><code>{item.access_token_masked || item.token_ref}</code><small>{item.token_ref}</small></td>
      <td>{item.email || "-"}</td>
      <td>
        {editing ? (
          <div className="account-password-cell">
            <input className="cell-input password-cell" type={revealPassword ? "text" : "password"} value={draft.password} onChange={(event) => setDraft({ ...draft, password: event.target.value })} placeholder="password" />
            <IconButton title={revealPassword ? "隐藏密码" : "显示密码"} onClick={() => setRevealPassword((value) => !value)}>{revealPassword ? <EyeOff size={15} /> : <Eye size={15} />}</IconButton>
          </div>
        ) : (
          <div className="account-password-cell">
            <code>{item.password ? (revealPassword ? item.password : "••••••••") : "-"}</code>
            {item.password ? (
              <>
                <IconButton title={revealPassword ? "隐藏密码" : "显示密码"} onClick={() => setRevealPassword((value) => !value)}>{revealPassword ? <EyeOff size={15} /> : <Eye size={15} />}</IconButton>
                <IconButton title="复制密码" onClick={() => copyText(item.password || "").then(() => toast("success", "已复制密码"))}><Copy size={15} /></IconButton>
              </>
            ) : null}
          </div>
        )}
      </td>
      <td>{editing ? <input className="cell-input" list="account-type-options" value={draft.type} onChange={(event) => setDraft({ ...draft, type: event.target.value })} /> : (item.type || "Free")}</td>
      <td>{editing ? <select className="cell-input" value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}><option>正常</option><option>限流</option><option>异常</option><option>禁用</option></select> : <Badge value={item.status} />}</td>
      <td>{editing ? <input className="cell-input" type="number" min={0} value={draft.quota} onChange={(event) => setDraft({ ...draft, quota: event.target.value })} /> : formatQuota(item)}</td>
      <td>{item.restore_at || "-"}</td>
      <td>{item.success}/{item.fail}</td>
      <td>{fmtDate(item.last_used_at)}</td>
      <td className="row-actions">
        {editing ? (
          <>
            <button className="secondary small" disabled={saving} onClick={save}>{saving ? "保存中" : "保存"}</button>
            <button className="ghost small" disabled={saving} onClick={() => setEditing(false)}>取消</button>
          </>
        ) : (
          <>
            <button className="ghost small" onClick={() => setEditing(true)}>编辑</button>
            <IconButton title="刷新" onClick={onRefresh}><RefreshCw size={15} /></IconButton>
            <IconButton title={item.status === "禁用" ? "启用" : "禁用"} onClick={onToggle}><Ban size={15} /></IconButton>
            <IconButton title="删除" className="danger-icon" onClick={onDelete}><Trash2 size={15} /></IconButton>
          </>
        )}
      </td>
    </tr>
  );
}

function ImageWorkbench({ token, identity, accounts, canRefreshArchive, setTasks, setImages, toast, openLightbox }: { token: string; identity: Identity; accounts: Account[]; canRefreshArchive: boolean; setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>; setImages: React.Dispatch<React.SetStateAction<StoredImage[]>>; toast: (type: Toast["type"], message: string) => void; openLightbox: (src: string, title?: string) => void }) {
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState("gpt-image-2");
  const [size, setSize] = useState("");
  const [count, setCount] = useState(1);
  const [asyncMode, setAsyncMode] = useState(true);
  const [refs, setRefs] = useState<ReferenceImage[]>([]);
  const [turns, setTurns] = useState<WorkbenchTurn[]>([]);
  const [activeTurnId, setActiveTurnId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [historyReady, setHistoryReady] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const storageKey = useMemo(() => `${workbenchStoragePrefix}${identity.id || identity.key_id || "legacy"}`, [identity.id, identity.key_id]);
  const quota = accounts.length ? quotaSummary(accounts) : "可用";
  const activeTaskCount = useMemo(() => turns.reduce((sum, turn) => sum + turn.images.filter((item) => item.status === "queued" || item.status === "running").length, 0), [turns]);
  const activeTaskIds = useMemo(() => Array.from(new Set(turns.flatMap((turn) => turn.images.flatMap((item) => item.taskId && (item.status === "queued" || item.status === "running") ? [item.taskId] : [])))), [turns]);
  const activeTaskKey = activeTaskIds.join(",");
  const hasHistory = turns.length > 0;

  useEffect(() => {
    let cancelled = false;
    setHistoryReady(false);
    setTurns([]);
    setActiveTurnId(null);
    loadWorkbenchState(storageKey).then((state) => {
      if (cancelled) return;
      setTurns(state.turns);
      setActiveTurnId(state.activeTurnId);
      setHistoryReady(true);
    }).catch(() => {
      if (!cancelled) setHistoryReady(true);
    });
    return () => {
      cancelled = true;
    };
  }, [storageKey]);

  useEffect(() => {
    if (!historyReady) return;
    saveWorkbenchState(storageKey, { version: 1, activeTurnId, turns: serializeWorkbenchTurns(turns) });
  }, [activeTurnId, historyReady, storageKey, turns]);

  async function addFiles(files: File[]) {
    const next = await Promise.all(files.filter((file) => file.type.startsWith("image/")).map(async (file) => ({ id: createID("ref"), name: file.name, file, dataUrl: await fileToDataURL(file) })));
    setRefs((current) => [...current, ...next].slice(0, 8));
  }

  useEffect(() => {
    if (!historyReady || !activeTaskKey) return;
    const ids = activeTaskKey.split(",").filter(Boolean);
    const poll = async () => {
      try {
        const data = await api.tasks(token, ids);
        setTasks((current) => mergeImageTasks(current, data.items));
        applyTaskUpdates(data.items);
        if (canRefreshArchive && data.items.some((task) => task.status === "success")) {
          api.images(token).then((data) => setImages(data.items || [])).catch(() => {});
        }
      } catch {
        // Keep polling quiet; task rows show the last known state.
      }
    };
    poll();
    const timer = window.setInterval(poll, 2500);
    return () => window.clearInterval(timer);
  }, [activeTaskKey, historyReady, token]);

  function applyTaskUpdates(items: ImageTask[]) {
    const taskMap = new Map(items.map((task) => [task.id, task]));
    setTurns((current) => current.map((turn) => {
      const images = turn.images.map((item) => {
        if (!item.taskId) return item;
        const task = taskMap.get(item.taskId);
        if (!task) return item;
        const result = parseTaskData(task.data)[0];
        return {
          ...item,
          status: taskStatusToWorkbench(task.status),
          image: result || item.image,
          error: task.error || item.error
        };
      });
      return { ...turn, images, status: deriveTurnStatus(images), error: images.find((item) => item.error)?.error };
    }));
  }

  async function createTaskForImage(turn: WorkbenchTurn, item: WorkbenchItem) {
    const taskSize = workbenchRequestSize(turn.size);
    if (turn.mode === "edit") {
      const form = new FormData();
      form.set("client_task_id", item.id);
      form.set("prompt", turn.prompt);
      form.set("model", turn.model);
      if (taskSize) form.set("size", taskSize);
      turn.refs.forEach((ref) => form.append("image", ref.file, ref.name));
      return api.createEditTask(token, form);
    }
    return api.createGenerationTask(token, { client_task_id: item.id, prompt: turn.prompt, model: turn.model, size: taskSize || undefined, n: 1 });
  }

  async function enqueueImages(turn: WorkbenchTurn, imageIds = turn.images.map((item) => item.id)) {
    let failed = 0;
    const submitted: ImageTask[] = [];
    for (const imageId of imageIds) {
      const item = turn.images.find((candidate) => candidate.id === imageId);
      if (!item) continue;
      try {
        const task = await createTaskForImage(turn, item);
        submitted.push(task);
        setTurns((current) => current.map((row) => {
          if (row.id !== turn.id) return row;
          const images = row.images.map((image) => image.id === imageId
            ? { ...image, taskId: task.id, status: taskStatusToWorkbench(task.status), image: parseTaskData(task.data)[0] || image.image, error: task.error }
            : image);
          return { ...row, images, status: deriveTurnStatus(images), error: images.find((image) => image.error)?.error };
        }));
      } catch (error) {
        failed += 1;
        const message = error instanceof Error ? error.message : "提交失败";
        setTurns((current) => current.map((row) => {
          if (row.id !== turn.id) return row;
          const images = row.images.map((image) => image.id === imageId ? { ...image, status: "error" as const, error: message } : image);
          return { ...row, images, status: deriveTurnStatus(images), error: message };
        }));
      }
    }
    if (submitted.length) {
      setTasks((current) => mergeImageTasks(current, submitted));
    }
    if (failed) throw new Error(`${failed} 个任务提交失败`);
  }

  async function runSyncTurn(turn: WorkbenchTurn) {
    try {
      const taskSize = workbenchRequestSize(turn.size);
      if (turn.mode === "edit") {
        const form = new FormData();
        form.set("prompt", turn.prompt);
        form.set("model", turn.model);
        form.set("response_format", "url");
        form.set("n", String(turn.count));
        if (taskSize) form.set("size", taskSize);
        turn.refs.forEach((ref) => form.append("image", ref.file, ref.name));
        const data = await request<{ data: ImageResult[] }>(token, "/v1/images/edits", { method: "POST", body: form });
        finishSyncTurn(turn.id, turn, data.data || []);
      } else {
        const data = await request<{ data: ImageResult[] }>(token, "/v1/images/generations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ prompt: turn.prompt, model: turn.model, size: taskSize || undefined, n: turn.count, response_format: "url" }) });
        finishSyncTurn(turn.id, turn, data.data || []);
      }
      if (canRefreshArchive) {
        api.images(token).then((data) => setImages(data.items || [])).catch(() => {});
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "生成失败";
      setTurns((current) => current.map((row) => row.id === turn.id ? {
        ...row,
        status: "error",
        error: message,
        images: row.images.map((image) => ({ ...image, status: "error", error: message }))
      } : row));
      throw error;
    }
  }

  function finishSyncTurn(turnId: string, source: WorkbenchTurn, images: ImageResult[]) {
    const nextImages = Array.from({ length: Math.max(1, images.length || source.count) }, (_, index) => ({
      id: createID("img"),
      status: images[index] ? "success" as const : "error" as const,
      prompt: source.prompt,
      model: source.model,
      size: source.size,
      image: images[index],
      error: images[index] ? undefined : "未返回图片"
    }));
    setTurns((current) => current.map((turn) => turn.id === turnId ? { ...turn, images: nextImages, status: deriveTurnStatus(nextImages), error: nextImages.find((item) => item.error)?.error } : turn));
  }

  async function submit() {
    const text = prompt.trim();
    if (!text) return;
    setBusy(true);
    try {
      const countValue = Math.max(1, Math.min(4, count || 1));
      const sizeValue = normalizeWorkbenchSize(size);
      const mode = refs.length ? "edit" : "generate";
      const images: WorkbenchItem[] = Array.from({ length: countValue }, () => ({ id: createID("img"), status: "queued", prompt: text, model, size: sizeValue }));
      const turn: WorkbenchTurn = {
        id: createID("turn"),
        prompt: text,
        model,
        size: sizeValue,
        count: countValue,
        mode,
        refs: mode === "edit" ? refs : [],
        images,
        status: "queued",
        createdAt: new Date().toISOString()
      };
      setTurns((current) => [turn, ...current]);
      setActiveTurnId(turn.id);
      setPrompt("");
      setRefs([]);
      if (asyncMode) {
        await enqueueImages(turn);
        toast("success", `已提交 ${countValue} 个任务`);
      } else {
        await runSyncTurn(turn);
        toast("success", "图片已生成");
      }
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "生成失败");
    } finally {
      setBusy(false);
    }
  }

  async function retryImage(turnId: string, imageId: string) {
    const source = turns.find((turn) => turn.id === turnId);
    const item = source?.images.find((image) => image.id === imageId);
    if (!source || !item) return;
    const retryId = createID("img");
    const retrySize = normalizeWorkbenchSize(source.size);
    const retryItem: WorkbenchItem = { ...item, id: retryId, status: "queued", taskId: undefined, image: undefined, error: undefined, size: retrySize };
    const retryTurn: WorkbenchTurn = {
      ...source,
      size: retrySize,
      images: source.images.map((image) => image.id === imageId ? retryItem : image),
      status: "queued",
      error: undefined
    };
    setTurns((current) => current.map((turn) => {
      if (turn.id !== turnId) return turn;
      const images = turn.images.map((image) => image.id === imageId ? retryItem : image);
      return { ...turn, size: retrySize, images, status: deriveTurnStatus(images), error: undefined };
    }));
    try {
      await enqueueImages(retryTurn, [retryId]);
      toast("success", "已重新提交");
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "重新提交失败");
    }
  }

  async function regenerateTurn(turn: WorkbenchTurn) {
    const turnSize = normalizeWorkbenchSize(turn.size);
    const images = Array.from({ length: turn.count }, () => ({ id: createID("img"), status: "queued" as const, prompt: turn.prompt, model: turn.model, size: turnSize }));
    const nextTurn = { ...turn, id: createID("turn"), size: turnSize, images, status: "queued" as const, createdAt: new Date().toISOString(), error: undefined };
    setTurns((current) => [nextTurn, ...current]);
    setActiveTurnId(nextTurn.id);
    try {
      await enqueueImages(nextTurn);
      toast("success", "已重新生成");
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "重新生成失败");
    }
  }

  function reuseTurn(turn: WorkbenchTurn) {
    setPrompt(turn.prompt);
    setModel(turn.model);
    setSize(normalizeWorkbenchSize(turn.size));
    setCount(turn.count);
    setRefs(turn.refs);
    setActiveTurnId(turn.id);
  }

  async function useAsReference(item: WorkbenchItem) {
    try {
      const ref = await buildReferenceFromResult(item);
      setRefs((current) => [...current, ref].slice(0, 8));
      toast("success", "已加入参考图");
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "无法加入参考图");
    }
  }

  function scrollToTurn(id: string) {
    setActiveTurnId(id);
    requestAnimationFrame(() => document.getElementById(`turn-${id}`)?.scrollIntoView({ block: "start", behavior: "smooth" }));
  }

  return (
    <div className="creator-page">
      <aside className="creator-rail">
        <div className="rail-actions">
          <button onClick={() => { setActiveTurnId(null); setPrompt(""); setRefs([]); }}><MessageSquarePlus size={16} />新建</button>
          <IconButton title="清空历史" disabled={!turns.length} onClick={() => {
            if (!confirm("清空当前页面的图片记录？")) return;
            setTurns([]);
            setActiveTurnId(null);
            localStorage.removeItem(storageKey);
          }}><Trash2 size={15} /></IconButton>
        </div>
        <div className="history-list">
          {!hasHistory ? <div className="history-empty">暂无图片记录</div> : turns.map((turn) => (
            <button key={turn.id} className={classNames("history-item", activeTurnId === turn.id && "active")} onClick={() => scrollToTurn(turn.id)}>
              <strong>{turn.prompt}</strong>
              <span>{turn.count} 张 · {turnModeLabel(turn.mode)} · {fmtDate(turn.createdAt)}</span>
              <Badge value={turn.status} />
            </button>
          ))}
        </div>
      </aside>

      <main className="creator-main">
        <div className={classNames("creation-feed", !hasHistory && "empty")}>
          {!hasHistory ? (
            <div className="workbench-empty">
              <Sparkles size={28} />
              <h1>Turn ideas into images</h1>
              <p>灵感、参考图与结果会在这里汇合。</p>
            </div>
          ) : turns.map((turn, turnIndex) => (
            <section key={turn.id} id={`turn-${turn.id}`} className={classNames("creator-turn", activeTurnId === turn.id && "active")}>
              <div className="prompt-row">
                <div className="prompt-bubble">
                  <div className="turn-meta"><span>第 {turns.length - turnIndex} 轮</span><span>{turnModeLabel(turn.mode)}</span><span>{turnStatusLabel(turn.status)}</span><span>{fmtDate(turn.createdAt)}</span></div>
                  <p>{turn.prompt}</p>
                  <div className="turn-actions">
                    <button className="ghost small" onClick={() => reuseTurn(turn)}>复用配置</button>
                    <IconButton title="删除本轮" onClick={() => setTurns((current) => current.filter((item) => item.id !== turn.id))}><Trash2 size={14} /></IconButton>
                  </div>
                </div>
              </div>

              <div className="result-flow">
                {turn.refs.length ? (
                  <div className="turn-ref-strip">
                    {turn.refs.map((ref) => <button key={ref.id} onClick={() => openLightbox(ref.dataUrl, ref.name)}><img src={ref.dataUrl} alt={ref.name} /></button>)}
                  </div>
                ) : null}
                <div className="turn-summary"><span>{turn.count} 张</span><span>{turn.model}</span><span>{displayImageSize(turn.size)}</span><Badge value={turn.status} /></div>
                <div className="creation-grid">
                  {turn.images.map((item, index) => (
                    <ResultCard
                      key={item.id}
                      item={item}
                      index={index}
                      size={turn.size}
                      openLightbox={openLightbox}
                      onUseAsReference={() => useAsReference(item)}
                      onRetry={() => retryImage(turn.id, item.id)}
                      onCopy={() => {
                        const src = item.image ? imageSrc(item.image) : "";
                        if (!src || src.startsWith("data:")) return;
                        copyText(src.startsWith("http") ? src : `${location.origin}${src}`).then(() => toast("success", "已复制链接"));
                      }}
                    />
                  ))}
                </div>
                {turn.error ? <p className="turn-error">{turn.error}</p> : null}
                <div className="turn-result-actions">
                  <button className="ghost small" onClick={() => regenerateTurn(turn)}><RotateCcw size={14} />全部重新生成</button>
                </div>
              </div>
            </section>
          ))}
        </div>

        <section className="creator-composer">
          <input ref={fileRef} className="hidden" type="file" accept="image/*" multiple onChange={(event) => { addFiles(Array.from(event.target.files || [])); event.currentTarget.value = ""; }} />
          {refs.length ? (
            <div className="reference-strip">
              {refs.map((ref) => <button key={ref.id} className="reference-thumb" onClick={() => openLightbox(ref.dataUrl, ref.name)}><img src={ref.dataUrl} alt={ref.name} /><span onClick={(event) => { event.stopPropagation(); setRefs((items) => items.filter((item) => item.id !== ref.id)); }}><X size={12} /></span></button>)}
            </div>
          ) : null}
          <div className="composer-surface">
            <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} onPaste={(event) => {
              const files = Array.from(event.clipboardData.files).filter((file) => file.type.startsWith("image/"));
              if (files.length) {
                event.preventDefault();
                addFiles(files);
              }
            }} onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                submit();
              }
            }} placeholder={refs.length ? "描述你希望如何修改参考图" : "输入你想要生成的画面，也可直接粘贴图片"} />
            <div className="composer-footer">
              <div className="composer-controls">
                <button className="composer-pill" onClick={() => fileRef.current?.click()}><ImagePlus size={16} />{refs.length ? "添加参考图" : "上传"}</button>
                <span className="composer-pill passive">额度 {quota}</span>
                {activeTaskCount > 0 ? <span className="composer-pill running"><LoaderCircle className="spin" size={14} />{activeTaskCount} 处理中</span> : null}
                <label className="composer-field"><span>模型</span><input value={model} onChange={(event) => setModel(event.target.value)} /></label>
                <label className="composer-field small-field"><span>比例</span><select value={size} onChange={(event) => setSize(event.target.value)}><option value="">默认</option><option>1:1</option><option>16:9</option><option>9:16</option><option>4:3</option><option>3:4</option></select></label>
                <label className="composer-field count-field"><span>张数</span><input type="number" min={1} max={4} value={count} onChange={(event) => setCount(Math.max(1, Math.min(4, Number(event.target.value) || 1)))} /></label>
                <div className="mode-toggle">
                  <button className={classNames(asyncMode && "active")} onClick={() => setAsyncMode(true)}>异步</button>
                  <button className={classNames(!asyncMode && "active")} onClick={() => setAsyncMode(false)}>同步</button>
                </div>
              </div>
              <button className="send-button" disabled={busy || !prompt.trim()} onClick={submit} aria-label={refs.length ? "编辑图片" : "生成图片"}>{busy ? <LoaderCircle className="spin" size={17} /> : <ArrowUp size={17} />}</button>
            </div>
          </div>
        </section>
      </main>
    </div>
  );
}

function ResultCard({ item, index, size, openLightbox, onUseAsReference, onRetry, onCopy }: { item: WorkbenchItem; index: number; size?: string; openLightbox: (src: string, title?: string) => void; onUseAsReference: () => void; onRetry: () => void; onCopy: () => void }) {
  const src = item.image ? imageSrc(item.image) : "";
  return (
    <article className={classNames("creation-image", sizeAspectClass(size), item.status === "error" && "error")}>
      {src ? (
        <button className="image-preview" onClick={() => openLightbox(src, item.prompt)}><img src={src} alt={item.prompt} loading="lazy" /></button>
      ) : (
        <div className="result-placeholder">
          {item.status === "error" ? <X size={22} /> : item.status === "queued" ? <Clock3 size={22} /> : <LoaderCircle className="spin" size={22} />}
          <span>{turnStatusLabel(item.status)}</span>
        </div>
      )}
      <div className="image-card-footer">
        <span>结果 {index + 1}</span>
        <Badge value={item.status} />
      </div>
      {src ? <div className="image-card-actions"><button className="ghost small" onClick={onUseAsReference}><Sparkles size={13} />加入编辑</button>{!src.startsWith("data:") ? <IconButton title="复制链接" onClick={onCopy}><Copy size={13} /></IconButton> : null}</div> : null}
      {item.status === "error" ? <button className="ghost small retry-button" onClick={onRetry}><RotateCcw size={13} />重新生成</button> : null}
      {item.error ? <p className="error-text">{item.error}</p> : null}
    </article>
  );
}

function deriveTurnStatus(images: WorkbenchItem[]): WorkbenchTurn["status"] {
  if (!images.length) return "queued";
  if (images.every((item) => item.status === "success")) return "success";
  if (images.some((item) => item.status === "running")) return "running";
  if (images.some((item) => item.status === "queued")) return "queued";
  return "error";
}

function taskStatusToWorkbench(status: ImageTask["status"]): WorkbenchItem["status"] {
  if (status === "success" || status === "error" || status === "running") return status;
  return "queued";
}

function turnModeLabel(mode: WorkbenchTurn["mode"]) {
  return mode === "edit" ? "编辑图" : "文生图";
}

function turnStatusLabel(status: WorkbenchTurn["status"] | WorkbenchItem["status"]) {
  if (status === "queued") return "排队中";
  if (status === "running") return "处理中";
  if (status === "success") return "已完成";
  return "失败";
}

function sizeAspectClass(size?: string) {
  if (size === "16:9") return "wide";
  if (size === "9:16") return "tall";
  if (size === "4:3") return "landscape";
  if (size === "3:4") return "portrait";
  return "square";
}

function normalizeWorkbenchSize(size?: string) {
  const value = (size || "").trim();
  if (value === "auto" || value === "default" || value === "默认") return "";
  return value;
}

function workbenchRequestSize(size?: string) {
  return normalizeWorkbenchSize(size);
}

function displayImageSize(size?: string) {
  return normalizeWorkbenchSize(size) || "默认";
}

function dataURLToFile(dataUrl: string, name: string) {
  const [header, body = ""] = dataUrl.split(",");
  const mime = /data:(.*?);base64/.exec(header)?.[1] || "image/png";
  const binary = atob(body);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return new File([bytes], name, { type: mime });
}

function serializeWorkbenchTurns(turns: WorkbenchTurn[]): StoredWorkbenchTurn[] {
  return turns.slice(0, 80).map((turn) => ({
    ...turn,
    refs: turn.refs.map(({ id, name, dataUrl }) => ({ id, name, dataUrl }))
  }));
}

async function loadWorkbenchState(key: string): Promise<{ activeTurnId: string | null; turns: WorkbenchTurn[] }> {
  const raw = localStorage.getItem(key);
  if (!raw) return { activeTurnId: null, turns: [] };
  const parsed = JSON.parse(raw) as Partial<StoredWorkbenchState>;
  if (parsed.version !== 1 || !Array.isArray(parsed.turns)) return { activeTurnId: null, turns: [] };
  const turns = await Promise.all(parsed.turns.map(restoreWorkbenchTurn));
  const activeTurnId = parsed.activeTurnId && turns.some((turn) => turn.id === parsed.activeTurnId) ? parsed.activeTurnId : turns[0]?.id ?? null;
  return { activeTurnId, turns };
}

async function restoreWorkbenchTurn(turn: StoredWorkbenchTurn): Promise<WorkbenchTurn> {
  const refs = turn.refs.map((ref) => {
    const file = dataURLToFile(ref.dataUrl, ref.name || `${ref.id}.png`);
    return { ...ref, file };
  });
  const images = Array.isArray(turn.images) ? turn.images : [];
  return {
    ...turn,
    refs,
    images,
    status: deriveTurnStatus(images),
    error: turn.error || images.find((item) => item.error)?.error
  };
}

function saveWorkbenchState(key: string, state: StoredWorkbenchState) {
  try {
    if (!state.turns.length) {
      localStorage.removeItem(key);
      return;
    }
    localStorage.setItem(key, JSON.stringify(state));
  } catch {
    // Browser storage can be full when many base64 images are kept; the UI still works in memory.
  }
}

async function buildReferenceFromResult(item: WorkbenchItem): Promise<ReferenceImage> {
  if (!item.image) throw new Error("没有可用图片");
  const src = imageSrc(item.image);
  if (!src) throw new Error("没有可用图片");
  const name = `result-${item.id}.png`;
  if (src.startsWith("data:")) {
    const file = dataURLToFile(src, name);
    return { id: createID("ref"), name, file, dataUrl: src };
  }
  const res = await fetch(src, { credentials: "same-origin" });
  if (!res.ok) throw new Error("读取结果图失败");
  const blob = await res.blob();
  const file = new File([blob], name, { type: blob.type || "image/png" });
  return { id: createID("ref"), name, file, dataUrl: await fileToDataURL(file) };
}

function mergeImageTasks(current: ImageTask[], updates: ImageTask[]) {
  if (!updates.length) return current;
  const map = new Map(current.map((task) => [task.id, task]));
  updates.forEach((task) => {
    const previous = map.get(task.id);
    map.set(task.id, previous ? { ...previous, ...task, data: task.data ?? previous.data } : task);
  });
  return Array.from(map.values()).sort((a, b) => b.updated_at.localeCompare(a.updated_at));
}

function hasLogDetail(detail: unknown) {
  return !!detail && typeof detail === "object" && !Array.isArray(detail) && Object.keys(detail as Record<string, unknown>).length > 0;
}

function mergeLogs(current: SystemLog[], updates: SystemLog[]) {
  if (!updates.length) return current;
  const map = new Map(current.map((log) => [log.id, log]));
  updates.forEach((log) => {
    const previous = map.get(log.id);
    map.set(log.id, previous ? { ...previous, ...log, detail: hasLogDetail(log.detail) ? log.detail : previous.detail } : log);
  });
  return Array.from(map.values()).sort((a, b) => b.time.localeCompare(a.time));
}

function ActivityPanel({ token, tasks, setTasks, logs, setLogs, openLightbox, toast }: { token: string; tasks: ImageTask[]; setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>; logs: SystemLog[]; setLogs: React.Dispatch<React.SetStateAction<SystemLog[]>>; openLightbox: (src: string, title?: string) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [view, setView] = useState<"tasks" | "logs">("tasks");
  async function refresh() {
    const [taskData, logData] = await Promise.all([api.tasks(token), api.logs(token)]);
    setTasks(taskData.items || []);
    setLogs(logData.items || []);
    toast("success", "任务日志已刷新");
  }
  return (
    <section className="panel">
      <PanelHead
        title="任务日志"
        subtitle="图片任务与系统日志统一查看，展开行可看完整上下文"
        action={<><div className="segmented"><button className={classNames(view === "tasks" && "active")} onClick={() => setView("tasks")}>任务</button><button className={classNames(view === "logs" && "active")} onClick={() => setView("logs")}>日志</button></div><button className="secondary" onClick={() => refresh().catch((error) => toast("error", error.message))}>刷新</button></>}
      />
      {view === "tasks"
        ? <TasksTable token={token} tasks={tasks} setTasks={setTasks} openLightbox={openLightbox} toast={toast} />
        : <LogsTable token={token} logs={logs} setLogs={setLogs} toast={toast} />}
    </section>
  );
}

function TasksTable({ token, tasks, setTasks, openLightbox, toast }: { token: string; tasks: ImageTask[]; setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>; openLightbox: (src: string, title?: string) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [loadingDetail, setLoadingDetail] = useState<string | null>(null);
  const rows = tasks.filter((item) => {
    const text = `${item.id} ${item.mode} ${item.status} ${item.model} ${item.size} ${item.prompt} ${item.error}`.toLowerCase();
    return (!query || text.includes(query.toLowerCase())) && (!status || item.status === status);
  });
  const selectedSet = new Set(selected);
  const allVisibleSelected = rows.length > 0 && rows.every((task) => selectedSet.has(task.id));
  function toggleVisible(checked: boolean) {
    const visibleIDs = rows.map((task) => task.id);
    setSelected((prev) => checked ? Array.from(new Set([...prev, ...visibleIDs])) : prev.filter((id) => !visibleIDs.includes(id)));
  }
  async function loadDetail(task: ImageTask) {
    if (expanded === task.id) {
      setExpanded(null);
      return;
    }
    setExpanded(task.id);
    if (task.data !== undefined) return;
    setLoadingDetail(task.id);
    try {
      const data = await api.tasks(token, [task.id]);
      setTasks((current) => mergeImageTasks(current, data.items || []));
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "读取任务详情失败");
    } finally {
      setLoadingDetail(null);
    }
  }
  async function removeSelected() {
    if (!selected.length || !confirm(`删除 ${selected.length} 个图片任务？`)) return;
    const data = await api.deleteTasks(token, selected);
    setTasks((current) => current.filter((task) => !selected.includes(task.id)));
    setSelected([]);
    toast("success", `已删除 ${data.removed} 个任务`);
  }
  return (
    <>
      <div className="filters activity-filters"><div className="searchbox"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索任务 ID、模型、提示词、状态" /></div><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">全部状态</option><option>queued</option><option>running</option><option>success</option><option>error</option></select><button className="secondary" onClick={() => api.tasks(token).then((data) => { setTasks(data.items || []); toast("success", "任务已刷新"); })}>刷新任务</button><button className="danger" disabled={!selected.length} onClick={removeSelected}>删除选中</button></div>
      <div className="table-wrap"><table className="activity-table task-table"><thead><tr><th><input type="checkbox" checked={allVisibleSelected} onChange={(event) => toggleVisible(event.target.checked)} aria-label="选择当前任务" /></th><th>ID</th><th>Mode</th><th>Status</th><th>Prompt</th><th>Model</th><th>Size</th><th>耗时</th><th>Result</th><th>Updated</th><th></th></tr></thead><tbody>{rows.map((task) => {
        const first = parseTaskData(task.data)[0];
        const src = first ? imageSrc(first) : "";
        const open = expanded === task.id;
        return (
          <React.Fragment key={task.id}>
            <tr>
              <td><input type="checkbox" checked={selectedSet.has(task.id)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, task.id] : prev.filter((id) => id !== task.id))} /></td>
              <td><code>{task.id}</code></td>
              <td>{task.mode}</td>
              <td><Badge value={task.status} /></td>
              <td>{task.prompt || "-"}</td>
              <td>{task.model || "-"}</td>
              <td>{task.size || "-"}</td>
              <td>{taskDuration(task)}</td>
              <td>{src ? <button className="link-button" onClick={() => openLightbox(src, task.id)}>预览</button> : "-"}</td>
              <td>{fmtDate(task.updated_at)}</td>
              <td><button className="ghost small" onClick={() => loadDetail(task)}>{loadingDetail === task.id ? "加载" : open ? "收起" : "详情"}</button></td>
            </tr>
            {open ? <tr className="detail-row"><td colSpan={11}>{loadingDetail === task.id ? <div className="detail-panel">加载详情中...</div> : <TaskDetail task={task} openLightbox={openLightbox} />}</td></tr> : null}
          </React.Fragment>
        );
      })}</tbody></table></div>
    </>
  );
}

function TaskDetail({ task, openLightbox }: { task: ImageTask; openLightbox: (src: string, title?: string) => void }) {
  const results = parseTaskData(task.data);
  return (
    <div className="detail-panel">
      <div className="detail-grid">
        <DetailItem label="任务 ID" value={task.id} code />
        <DetailItem label="状态" value={task.status} />
        <DetailItem label="创建时间" value={fmtDate(task.created_at)} />
        <DetailItem label="更新时间" value={fmtDate(task.updated_at)} />
        <DetailItem label="生成时间" value={taskDuration(task)} />
        <DetailItem label="模型" value={task.model || "-"} />
        <DetailItem label="比例" value={task.size || "-"} />
        <DetailItem label="模式" value={task.mode} />
      </div>
      {task.prompt ? <div className="detail-prompt"><span>提示词</span><p>{task.prompt}</p></div> : null}
      {task.error ? <p className="detail-error">{task.error}</p> : null}
      {results.length ? <div className="detail-images">{results.map((item, index) => { const src = imageSrc(item); return src ? <button key={index} onClick={() => openLightbox(src, task.id)}><img src={src} alt={`task ${index + 1}`} /></button> : null; })}</div> : null}
      <pre className="detail-json">{safeJSON({ ...task, data: results })}</pre>
    </div>
  );
}

function LogsTable({ token, logs, setLogs, toast }: { token: string; logs: SystemLog[]; setLogs: React.Dispatch<React.SetStateAction<SystemLog[]>>; toast: (type: Toast["type"], message: string) => void }) {
  const [type, setType] = useState("");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [loadingDetail, setLoadingDetail] = useState<string | null>(null);
  const rows = logs.filter((log) => {
    const text = `${log.type} ${log.summary} ${JSON.stringify(log.detail || {})}`.toLowerCase();
    return !query.trim() || text.includes(query.trim().toLowerCase());
  });
  const selectedSet = new Set(selected);
  const allVisibleSelected = rows.length > 0 && rows.every((log) => selectedSet.has(log.id));
  function toggleVisible(checked: boolean) {
    const visibleIDs = rows.map((log) => log.id);
    setSelected((prev) => checked ? Array.from(new Set([...prev, ...visibleIDs])) : prev.filter((id) => !visibleIDs.includes(id)));
  }
  async function load() { setLogs((await api.logs(token, type)).items || []); }
  async function loadDetail(log: SystemLog) {
    if (expanded === log.id) {
      setExpanded(null);
      return;
    }
    setExpanded(log.id);
    if (Object.keys(logDetail(log)).length > 0) return;
    setLoadingDetail(log.id);
    try {
      const data = await api.logs(token, "", [log.id]);
      setLogs((current) => mergeLogs(current, data.items || []));
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "读取日志详情失败");
    } finally {
      setLoadingDetail(null);
    }
  }
  async function clear() {
    if (!selected.length || !confirm(`清理 ${selected.length} 条日志？`)) return;
    const data = await api.deleteLogs(token, selected);
    setSelected([]);
    await load();
    toast("success", `已清理 ${data.removed} 条`);
  }
  return (
    <>
      <div className="filters activity-filters">
        <div className="searchbox"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索日志内容、接口、模型" /></div>
        <select value={type} onChange={(event) => setType(event.target.value)}><option value="">全部类型</option><option value="call">调用</option><option value="account">账号</option><option value="register">注册</option></select>
        <button className="secondary" onClick={() => load().catch((error) => toast("error", error.message))}>刷新日志</button>
        <button className="danger" disabled={!selected.length} onClick={() => clear().catch((error) => toast("error", error.message))}>清理选中</button>
      </div>
      <div className="table-wrap"><table className="activity-table log-table"><thead><tr><th><input type="checkbox" checked={allVisibleSelected} onChange={(event) => toggleVisible(event.target.checked)} aria-label="选择当前日志" /></th><th>Time</th><th>Type</th><th>Status</th><th>Endpoint</th><th>Model</th><th>耗时</th><th>Summary</th><th></th></tr></thead><tbody>{rows.map((log) => {
        const detail = logDetail(log);
        const open = expanded === log.id;
        return (
          <React.Fragment key={log.id}>
            <tr>
              <td><input type="checkbox" checked={selectedSet.has(log.id)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, log.id] : prev.filter((id) => id !== log.id))} /></td>
              <td>{fmtDate(log.time)}</td>
              <td>{log.type}</td>
              <td>{detail.status ? <Badge value={String(detail.status)} /> : "-"}</td>
              <td>{String(detail.endpoint || log.summary || "-")}</td>
              <td>{String(detail.model || "-")}</td>
              <td>{formatDuration(detail.duration_ms)}</td>
              <td>{log.summary}</td>
              <td><button className="ghost small" onClick={() => loadDetail(log)}>{loadingDetail === log.id ? "加载" : open ? "收起" : "详情"}</button></td>
            </tr>
            {open ? <tr className="detail-row"><td colSpan={9}>{loadingDetail === log.id ? <div className="detail-panel">加载详情中...</div> : <LogDetail log={log} />}</td></tr> : null}
          </React.Fragment>
        );
      })}</tbody></table></div>
    </>
  );
}

function LogDetail({ log }: { log: SystemLog }) {
  const detail = logDetail(log);
  return (
    <div className="detail-panel">
      <div className="detail-grid">
        <DetailItem label="日志 ID" value={log.id} code />
        <DetailItem label="时间" value={fmtDate(log.time)} />
        <DetailItem label="类型" value={log.type} />
        <DetailItem label="接口" value={String(detail.endpoint || log.summary)} />
        <DetailItem label="模型" value={String(detail.model || "-")} />
        <DetailItem label="状态" value={String(detail.status || "-")} />
        <DetailItem label="生成时间" value={formatDuration(detail.duration_ms)} />
        <DetailItem label="用户" value={String(detail.name || detail.subject_id || "-")} />
      </div>
      {detail.error ? <p className="detail-error">{String(detail.error)}</p> : null}
      <pre className="detail-json">{safeJSON({ id: log.id, time: log.time, type: log.type, summary: log.summary, detail })}</pre>
    </div>
  );
}

function DetailItem({ label, value, code }: { label: string; value: string; code?: boolean }) {
  return <div className="detail-item"><span>{label}</span>{code ? <code>{value}</code> : <strong>{value}</strong>}</div>;
}

function logDetail(log: SystemLog): Record<string, unknown> {
  if (!log.detail || typeof log.detail !== "object" || Array.isArray(log.detail)) return {};
  return log.detail as Record<string, unknown>;
}

function taskDuration(task: ImageTask) {
  const started = new Date(task.created_at).getTime();
  const ended = new Date(task.updated_at).getTime();
  if (!Number.isFinite(started) || !Number.isFinite(ended) || ended < started) return "-";
  return formatDuration(ended - started);
}

function formatDuration(value: unknown) {
  const ms = Number(value);
  if (!Number.isFinite(ms) || ms < 0) return "-";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`;
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

function UsersPanel({ token, users, setUsers, toast }: { token: string; users: User[]; setUsers: (items: User[]) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [form, setForm] = useState({ email: "", name: "", password: "", role: "user" });
  const [newKey, setNewKey] = useState("");
  async function reload() { setUsers((await api.users(token)).items || []); }
  async function create() {
    const data = await api.createUser(token, form);
    setForm({ email: "", name: "", password: "", role: "user" });
    if (data.key) setNewKey(data.key);
    await reload();
    toast("success", "用户已创建");
  }
  return (
    <section className="panel">
      <PanelHead title="用户" subtitle="创建用户、编辑资料、删除账号，并维护每个用户唯一 API Key" />
      <div className="toolbar user-toolbar">
        <input value={form.email} onChange={(event) => setForm({ ...form, email: event.target.value })} placeholder="email" />
        <input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="name" />
        <input value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} type="password" placeholder="password" />
        <select value={form.role} onChange={(event) => setForm({ ...form, role: event.target.value })}><option>user</option><option>admin</option></select>
        <button onClick={() => create().catch((error) => toast("error", error.message))}>创建用户</button>
      </div>
      {newKey ? <div className="notice"><span>新 API Key 只显示一次：</span><code>{newKey}</code><IconButton title="复制" onClick={() => copyText(newKey).then(() => toast("success", "已复制"))}><Copy size={14} /></IconButton><IconButton title="隐藏" onClick={() => setNewKey("")}><EyeOff size={14} /></IconButton></div> : null}
      <div className="table-wrap"><table className="users-table"><thead><tr><th>Email</th><th>Name</th><th>Role</th><th>Status</th><th>API Key</th><th>Last login</th><th></th></tr></thead><tbody>{users.map((user) => <UserRow key={user.id} token={token} user={user} reload={reload} toast={toast} showKey={(key) => setNewKey(key)} />)}</tbody></table></div>
    </section>
  );
}

function UserRow({ token, user, reload, toast, showKey }: { token: string; user: User; reload: () => Promise<void>; toast: (type: Toast["type"], message: string) => void; showKey: (key: string) => void }) {
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [draft, setDraft] = useState({ email: user.email, name: user.name || "", password: "", role: user.role, status: user.status });
  useEffect(() => {
    if (!editing) setDraft({ email: user.email, name: user.name || "", password: "", role: user.role, status: user.status });
  }, [editing, user.email, user.name, user.role, user.status]);
  async function save() {
    setSaving(true);
    try {
      const body: Partial<Pick<User, "email" | "name" | "role" | "status">> & { password?: string } = {
        email: draft.email.trim(),
        name: draft.name.trim(),
        role: draft.role,
        status: draft.status
      };
      if (draft.password.trim()) body.password = draft.password;
      await api.updateUser(token, user.id, body);
      await reload();
      setEditing(false);
      toast("success", "用户已更新");
    } finally {
      setSaving(false);
    }
  }
  async function toggleStatus() {
    await api.updateUser(token, user.id, { status: user.status === "active" ? "disabled" : "active" });
    await reload();
    toast("success", user.status === "active" ? "用户已停用" : "用户已启用");
  }
  async function remove() {
    if (!confirm(`删除用户 ${user.email}？`)) return;
    await api.deleteUser(token, user.id);
    await reload();
    toast("success", "用户已删除");
  }
  async function resetKey() {
    if (!confirm(`重置 ${user.email} 的 API Key？旧 key 会立即失效。`)) return;
    const data = await api.resetUserKey(token, user.id);
    showKey(data.key);
    await reload();
    toast("success", "API Key 已重置");
  }
  return (
    <tr>
      <td>{editing ? <input className="cell-input" value={draft.email} onChange={(event) => setDraft({ ...draft, email: event.target.value })} /> : user.email}</td>
      <td>{editing ? <input className="cell-input" value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} /> : (user.name || "-")}</td>
      <td>{editing ? <select className="cell-input" value={draft.role} onChange={(event) => setDraft({ ...draft, role: event.target.value as User["role"] })}><option>user</option><option>admin</option></select> : <Badge value={user.role} />}</td>
      <td>{editing ? <select className="cell-input" value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value as User["status"] })}><option value="active">active</option><option value="disabled">disabled</option></select> : <Badge value={user.status} />}</td>
      <td><Badge value={user.api_key?.enabled ?? false} /><small>{user.api_key ? `${user.api_key.name} · ${fmtDate(user.api_key.last_used_at)}` : "missing"}</small></td>
      <td>{fmtDate(user.last_login_at)}</td>
      <td className="row-actions">
        {editing ? (
          <>
            <input className="cell-input password-cell" type="password" value={draft.password} onChange={(event) => setDraft({ ...draft, password: event.target.value })} placeholder="new password" />
            <button className="secondary small" disabled={saving} onClick={save}>{saving ? "保存中" : "保存"}</button>
            <button className="ghost small" disabled={saving} onClick={() => setEditing(false)}>取消</button>
          </>
        ) : (
          <>
            <button className="ghost small" onClick={() => setEditing(true)}>编辑</button>
            <IconButton title={user.status === "active" ? "停用" : "启用"} onClick={() => toggleStatus().catch((error) => toast("error", error.message))}>{user.status === "active" ? <Ban size={15} /> : <Eye size={15} />}</IconButton>
            <IconButton title="重置 API Key" onClick={() => resetKey().catch((error) => toast("error", error.message))}><RotateCcw size={15} /></IconButton>
            <IconButton title="删除" className="danger-icon" onClick={() => remove().catch((error) => toast("error", error.message))}><Trash2 size={15} /></IconButton>
          </>
        )}
      </td>
    </tr>
  );
}

function SettingsPanel({ token, settings, setSettings, toast }: { token: string; settings: SettingsType; setSettings: (settings: SettingsType) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [json, setJson] = useState(safeJSON(settings));
  useEffect(() => setJson(safeJSON(settings)), [settings]);
  const aiReview = settings.ai_review && typeof settings.ai_review === "object" ? settings.ai_review as Record<string, unknown> : {};
  function updateField(key: string, value: unknown) { setSettings({ ...settings, [key]: value }); }
  async function save(next = settings) { const data = await api.saveSettings(token, next); setSettings(data.config || {}); toast("success", "设置已保存"); }
  return <div className="settings-layout"><section className="panel"><PanelHead title="常用设置" subtitle="保存后会同步写入配置表" action={<button onClick={() => save().catch((error) => toast("error", error.message))}>保存</button>} /><div className="settings-form"><label><span>Proxy</span><input value={String(settings.proxy || "")} onChange={(event) => updateField("proxy", event.target.value)} /></label><label><span>Base URL</span><input value={String(settings.base_url || "")} onChange={(event) => updateField("base_url", event.target.value)} /></label><label><span>图片保留天数</span><input type="number" value={Number(settings.image_retention_days || 30)} onChange={(event) => updateField("image_retention_days", Number(event.target.value))} /></label><label><span>图片轮询超时</span><input type="number" value={Number(settings.image_poll_timeout_secs || 120)} onChange={(event) => updateField("image_poll_timeout_secs", Number(event.target.value))} /></label><label className="inline"><input type="checkbox" checked={Boolean(settings.auto_remove_invalid_accounts)} onChange={(event) => updateField("auto_remove_invalid_accounts", event.target.checked)} /><span>自动移除异常账号</span></label><label className="inline"><input type="checkbox" checked={Boolean(aiReview.enabled)} onChange={(event) => updateField("ai_review", { ...aiReview, enabled: event.target.checked })} /><span>启用 AI 内容审核</span></label><label className="wide"><span>敏感词，每行一个</span><textarea value={Array.isArray(settings.sensitive_words) ? settings.sensitive_words.join("\n") : ""} onChange={(event) => updateField("sensitive_words", event.target.value.split("\n").map((line) => line.trim()).filter(Boolean))} /></label></div></section><section className="panel"><PanelHead title="原始 JSON" subtitle="高级设置可以直接编辑" action={<button className="secondary" onClick={() => { const parsed = parseJSON(json) as SettingsType; save(parsed).catch((error) => toast("error", error.message)); }}>保存 JSON</button>} /><textarea className="json-editor settings-json" value={json} onChange={(event) => setJson(event.target.value)} spellCheck={false} /></section></div>;
}

function RegisterPanel({ token, registerRuntime, setRegisterRuntime, toast }: { token: string; registerRuntime: RegisterRuntime | null; setRegisterRuntime: React.Dispatch<React.SetStateAction<RegisterRuntime | null>>; toast: (type: Toast["type"], message: string) => void }) {
  const registerConfig = registerRuntime?.state?.config;
  const registerMail = registerConfig?.mail || {};
  const [registerLogs, setRegisterLogs] = useState<SystemLog[]>([]);
  const [registerLogsExpanded, setRegisterLogsExpanded] = useState<string | null>(null);
  const [registerLogsLoading, setRegisterLogsLoading] = useState(false);
  const [registerLogDetailLoading, setRegisterLogDetailLoading] = useState<string | null>(null);
  const [draft, setDraft] = useState({
    proxy: String(registerConfig?.proxy || ""),
    mode: String(registerConfig?.mode || "total"),
    total: Number(registerConfig?.total || 10),
    threads: Number(registerConfig?.threads || 3),
    targetQuota: Number(registerConfig?.target_quota || 100),
    targetAvailable: Number(registerConfig?.target_available || 10),
    checkIntervalSeconds: parseCheckIntervalSeconds(registerConfig?.check_interval),
    apiBase: String(registerMail.inbucket_api_base || ""),
    domains: Array.isArray(registerMail.inbucket_domains) ? registerMail.inbucket_domains.join("\n") : "",
    randomSubdomain: Boolean(registerMail.random_subdomain ?? true)
  });
  useEffect(() => {
    setDraft({
      proxy: String(registerConfig?.proxy || ""),
      mode: String(registerConfig?.mode || "total"),
      total: Number(registerConfig?.total || 10),
      threads: Number(registerConfig?.threads || 3),
      targetQuota: Number(registerConfig?.target_quota || 100),
      targetAvailable: Number(registerConfig?.target_available || 10),
      checkIntervalSeconds: parseCheckIntervalSeconds(registerConfig?.check_interval),
      apiBase: String(registerMail.inbucket_api_base || ""),
      domains: Array.isArray(registerMail.inbucket_domains) ? registerMail.inbucket_domains.join("\n") : "",
      randomSubdomain: Boolean(registerMail.random_subdomain ?? true)
    });
  }, [registerConfig?.proxy, registerConfig?.mode, registerConfig?.total, registerConfig?.threads, registerConfig?.target_quota, registerConfig?.target_available, registerConfig?.check_interval, registerMail.inbucket_api_base, registerMail.inbucket_domains, registerMail.random_subdomain]);

  useEffect(() => {
    let cancelled = false;
    setRegisterLogsLoading(true);
    api.logs(token, "register")
      .then((data) => {
        if (cancelled) return;
        setRegisterLogs(data.items || []);
      })
      .catch(() => {})
      .finally(() => {
        if (cancelled) return;
        setRegisterLogsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  useEffect(() => {
    if (!registerRuntime?.running) return;
    const timer = window.setInterval(() => {
      api.logs(token, "register")
        .then((data) => setRegisterLogs((current) => mergeLogs(current, data.items || [])))
        .catch(() => {});
    }, 3000);
    return () => window.clearInterval(timer);
  }, [token, registerRuntime?.running]);

  async function reloadRegister() {
    const [runtime, logsData] = await Promise.all([api.registerState(token), api.logs(token, "register")]);
    setRegisterRuntime(runtime);
    setRegisterLogs(logsData.items || []);
  }
  async function saveRegister() {
    const data = await api.saveRegisterConfig(token, {
      proxy: draft.proxy,
      mode: draft.mode as "total" | "quota" | "available",
      total: Number(draft.total || 1),
      threads: Number(draft.threads || 1),
      target_quota: Number(draft.targetQuota || 1),
      target_available: Number(draft.targetAvailable || 1),
      check_interval_seconds: Number(draft.checkIntervalSeconds || 1),
      mail: {
        inbucket_api_base: draft.apiBase,
        inbucket_domains: draft.domains.split("\n").map((item) => item.trim()).filter(Boolean),
        random_subdomain: draft.randomSubdomain
      }
    });
    setRegisterRuntime((current) => current ? { ...current, state: data.state } : { state: data.state, running: false, last_error: "", last_result: null });
    setRegisterLogs((await api.logs(token, "register")).items || []);
    toast("success", "注册配置已保存");
  }
  async function startRegister() {
    const data = await api.startRegister(token);
    setRegisterRuntime(data);
    setRegisterLogs((await api.logs(token, "register")).items || []);
    toast("success", "注册任务已启动");
  }
  async function stopRegister() {
    const data = await api.stopRegister(token);
    setRegisterRuntime(data);
    setRegisterLogs((await api.logs(token, "register")).items || []);
    toast("success", "注册任务已停止");
  }
  async function runRegisterOnce() {
    const data = await api.runRegisterOnce(token);
    setRegisterRuntime(data);
    setRegisterLogs((await api.logs(token, "register")).items || []);
    toast("success", "单次注册已执行");
  }

  async function refreshRegisterLogs() {
    setRegisterLogsLoading(true);
    try {
      setRegisterLogs((await api.logs(token, "register")).items || []);
    } finally {
      setRegisterLogsLoading(false);
    }
  }

  async function toggleRegisterLogDetail(log: SystemLog) {
    if (registerLogsExpanded === log.id) {
      setRegisterLogsExpanded(null);
      return;
    }
    setRegisterLogsExpanded(log.id);
    if (Object.keys(logDetail(log)).length > 0) return;
    setRegisterLogDetailLoading(log.id);
    try {
      const data = await api.logs(token, "register", [log.id]);
      setRegisterLogs((current) => mergeLogs(current, data.items || []));
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "读取注册日志详情失败");
    } finally {
      setRegisterLogDetailLoading(null);
    }
  }

  return <div className="stack"><div className="register-layout"><section className="panel"><PanelHead title="注册配置" subtitle="inbucket 邮箱、并发和目标模式" action={<><button className="secondary small" onClick={() => reloadRegister().catch((error) => toast("error", error.message))}>刷新</button><button className="secondary small" onClick={() => saveRegister().catch((error) => toast("error", error.message))}>保存配置</button></>} /><div className="settings-form register-form"><label><span>Inbucket API Base</span><input value={draft.apiBase} onChange={(event) => setDraft({ ...draft, apiBase: event.target.value })} placeholder="http://127.0.0.1:9000" /></label><label><span>Register Proxy</span><input value={draft.proxy} onChange={(event) => setDraft({ ...draft, proxy: event.target.value })} placeholder="留空则继承全局 Proxy" /></label><label><span>模式</span><select value={draft.mode} onChange={(event) => setDraft({ ...draft, mode: event.target.value })}><option value="total">total</option><option value="quota">quota</option><option value="available">available</option></select></label><label><span>线程数</span><input type="number" value={draft.threads} onChange={(event) => setDraft({ ...draft, threads: Number(event.target.value) })} /></label><label><span>Total</span><input type="number" value={draft.total} onChange={(event) => setDraft({ ...draft, total: Number(event.target.value) })} /></label><label><span>Check Interval 秒</span><input type="number" value={draft.checkIntervalSeconds} onChange={(event) => setDraft({ ...draft, checkIntervalSeconds: Number(event.target.value) })} /></label><label><span>Target Quota</span><input type="number" value={draft.targetQuota} onChange={(event) => setDraft({ ...draft, targetQuota: Number(event.target.value) })} /></label><label><span>Target Available</span><input type="number" value={draft.targetAvailable} onChange={(event) => setDraft({ ...draft, targetAvailable: Number(event.target.value) })} /></label><label className="inline"><input type="checkbox" checked={draft.randomSubdomain} onChange={(event) => setDraft({ ...draft, randomSubdomain: event.target.checked })} /><span>随机子域名</span></label><label className="wide"><span>Inbucket Domains，每行一个</span><textarea value={draft.domains} onChange={(event) => setDraft({ ...draft, domains: event.target.value })} /></label></div></section><section className="panel"><PanelHead title="运行状态" subtitle="单次注册和批量注册控制" action={<div className="register-toolbar"><span className={classNames("badge", registerRuntime?.running ? "warn" : "ok")}>{registerRuntime?.running ? "running" : "idle"}</span></div>} /><div className="detail-grid register-stats"><DetailItem label="Success" value={String(Number(registerRuntime?.state?.stats?.success || 0))} /><DetailItem label="Fail" value={String(Number(registerRuntime?.state?.stats?.fail || 0))} /><DetailItem label="Done" value={String(Number(registerRuntime?.state?.stats?.done || 0))} /><DetailItem label="Running" value={String(Number(registerRuntime?.state?.stats?.running || 0))} /><DetailItem label="Quota" value={String(Number(registerRuntime?.state?.stats?.current_quota || 0))} /><DetailItem label="Available" value={String(Number(registerRuntime?.state?.stats?.current_available || 0))} /><DetailItem label="Started" value={fmtDate(registerRuntime?.state?.stats?.started_at)} /><DetailItem label="Updated" value={fmtDate(registerRuntime?.state?.stats?.updated_at)} /></div>{registerRuntime?.last_error ? <p className="detail-error">{registerRuntime.last_error}</p> : null}{registerRuntime?.last_result?.email ? <p className="register-last">last success: {registerRuntime.last_result.email} · {fmtDate(registerRuntime.last_result.created_at)}</p> : null}<div className="register-actions"><button onClick={() => runRegisterOnce().catch((error) => toast("error", error.message))}>单次注册</button><button className="secondary" onClick={() => startRegister().catch((error) => toast("error", error.message))}>启动批量</button><button className="secondary-danger" onClick={() => stopRegister().catch((error) => toast("error", error.message))}>停止批量</button></div></section></div><section className="panel"><PanelHead title="最近注册日志" subtitle="只显示 register 类型，方便直接排查当前注册流程" action={<button className="secondary small" onClick={() => refreshRegisterLogs().catch((error) => toast("error", error.message))}>{registerLogsLoading ? "刷新中" : "刷新日志"}</button>} /><div className="table-wrap"><table className="activity-table log-table register-log-table"><thead><tr><th>Time</th><th>Summary</th><th>Status</th><th>Email</th><th>Error</th><th></th></tr></thead><tbody>{registerLogs.length ? registerLogs.slice(0, 30).map((log) => {
    const detail = logDetail(log);
    const open = registerLogsExpanded === log.id;
    return (
      <React.Fragment key={log.id}>
        <tr>
          <td>{fmtDate(log.time)}</td>
          <td>{log.summary}</td>
          <td>{detail.status ? <Badge value={String(detail.status)} /> : /成功|完成|success/i.test(log.summary) ? <Badge value="success" /> : /失败|error/i.test(log.summary) ? <Badge value="error" /> : "-"}</td>
          <td>{String(detail.email || "-")}</td>
          <td className={detail.error ? "error-cell" : ""}>{String(detail.error || "-")}</td>
          <td><button className="ghost small" onClick={() => toggleRegisterLogDetail(log)}>{registerLogDetailLoading === log.id ? "加载" : open ? "收起" : "详情"}</button></td>
        </tr>
        {open ? <tr className="detail-row"><td colSpan={6}>{registerLogDetailLoading === log.id ? <div className="detail-panel">加载详情中...</div> : <LogDetail log={log} />}</td></tr> : null}
      </React.Fragment>
    );
  }) : <tr><td colSpan={6}><div className="register-log-empty">{registerLogsLoading ? "日志加载中..." : "暂无注册日志"}</div></td></tr>}</tbody></table></div></section></div>;
}

function parseCheckIntervalSeconds(value: unknown) {
  if (typeof value === "number" && Number.isFinite(value)) return Math.max(1, Math.round(value / 1_000_000_000));
  if (typeof value === "string") {
    if (/^\d+$/.test(value.trim())) return Number(value.trim());
    const matched = value.match(/(\d+)/);
    if (matched) return Number(matched[1]);
  }
  return 5;
}

createRoot(document.getElementById("root")!).render(<App />);
