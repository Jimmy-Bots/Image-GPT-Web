import React, { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowUp,
  Ban,
  Clock3,
  Copy,
  Download,
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
  Users,
  WandSparkles,
  X
} from "lucide-react";
import { api, authHeaders, getStoredToken, request, setStoredToken } from "./api";
import type { Account, AccountListSummary, AccountRefreshStatus, BackupRemoteItem, BackupState, Identity, ImageResult, ImageTask, ModelItem, ModelPolicy, ReferenceImage, RegisterRuntime, RegisterStatus, Settings as SettingsType, StoredImage, SystemLog, TaskEvent, Toast, User } from "./types";
import { classNames, compact, copyText, createID, fileToDataURL, fmtBytes, fmtDate, formatNextRefreshTime, formatQuota, formatRemainingTime, imageSrc, parseJSON, parseTaskData, safeJSON, statusClass, storedImageURL } from "./utils";
import "./styles.css";

type Tab = "dashboard" | "accounts" | "register" | "activity" | "images" | "playground" | "users" | "settings";
type WorkbenchItem = {
  id: string;
  phase?: "submitting" | "waiting_slot" | "task";
  status: "queued" | "running" | "success" | "error";
  prompt: string;
  model: string;
  size?: string;
  startedAt?: string;
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

function formatBadgeValue(value: string | boolean | number | undefined) {
  const raw = String(value ?? "-");
  switch (raw) {
    case "true":
      return "Enabled";
    case "false":
      return "Disabled";
    case "healthy":
      return "Healthy";
    case "offline":
      return "Offline";
    case "active":
      return "Active";
    case "disabled":
      return "Disabled";
    case "admin":
      return "Admin";
    case "user":
      return "User";
    case "queued":
      return "Queued";
    case "running":
      return "Running";
    case "success":
      return "Success";
    case "error":
      return "Error";
    case "deleted":
      return "Deleted";
    case "warning":
      return "Warning";
    case "unknown":
      return "Unknown";
    default:
      return raw;
  }
}

function Badge({ value }: { value: string | boolean | number | undefined }) {
  return <span className={classNames("badge", statusClass(String(value ?? "")))}>{formatBadgeValue(value)}</span>;
}

function HealthStatusBadge({ status, className }: { status: "healthy" | "offline"; className?: string }) {
  return (
    <span className={classNames("status-pill", "status-with-dot", status === "offline" && "offline", className)}>
      <span className="status-dot" />
      {formatBadgeValue(status)}
    </span>
  );
}

function quotaLabelFromSummary(summary: AccountListSummary | null) {
  if (!summary) return "可用";
  if (summary.quota_unlimited) return "∞";
  if (summary.quota_unknown) return "未知";
  return compact(summary.quota_total || 0);
}

function quotaLabelFromUser(user: User | null) {
  if (!user) return "可用";
  if (user.quota_unlimited) return "∞";
  return compact(Number(user.available_quota || 0));
}

function quotaDetailFromUser(user: User | null) {
  if (!user) return "额度信息暂不可用";
  if (user.quota_unlimited) return "无限额度";
  return `永久 ${compact(Number(user.permanent_quota || 0))} · 当天 ${compact(Number(user.temporary_quota || 0))} · 可用 ${compact(Number(user.available_quota || 0))}`;
}

function QuotaBadge({ user, quota }: { user: User | null; quota: string }) {
  return (
    <span className="composer-pill passive quota-pill">
      额度 {quota}
      <span className="quota-popover" role="tooltip">
        {quotaDetailFromUser(user)}
      </span>
    </span>
  );
}

function describeWorkbenchError(error: unknown) {
  const message = error instanceof Error ? error.message : "操作失败";
  const code = typeof error === "object" && error && "code" in error ? String((error as { code?: unknown }).code || "") : "";
  const normalized = message.toLowerCase();

  if (code === "quota_exceeded") return "额度不足，请减少张数或联系管理员调整额度。";
  if (code === "content_rejected") {
    if (normalized.includes("sensitive")) return "请求内容命中了敏感词规则，请调整提示词后再试。";
    if (normalized.includes("review")) return "请求内容未通过内容审查，请调整提示词后再试。";
    return "请求内容未通过审查，请调整后再试。";
  }
  if (code === "invalid_model") return "当前模型不可用，请联系管理员检查工作台模型配置。";
  if (code === "task_queue_full" || normalized.includes("queue is full")) return "任务队列已满，请稍等片刻后再试。";
  if (code === "upstream_not_implemented") return "当前上游暂不支持这个操作。";
  if (code === "upstream_error") {
    if (normalized.includes("invalid_access_token")) return "可用账号登录态已失效，系统正在尝试恢复，请稍后再试。";
    if (normalized.includes("rate limit") || normalized.includes("too many requests")) return "上游触发限流，请稍后再试。";
    if (normalized.includes("content policy") || normalized.includes("moderation")) return "图片请求被上游内容策略拦截，请调整提示词。";
    return "上游服务暂时不可用，请稍后重试。";
  }
  if (normalized.includes("failed to fetch") || normalized.includes("networkerror") || normalized.includes("network request failed")) {
    return "网络请求失败，请检查服务是否在线后重试。";
  }
  if (normalized.includes("image file is required")) return "请先上传参考图，再执行编辑。";
  if (normalized.includes("prompt is required")) return "请输入提示词后再试。";
  if (normalized.includes("insufficient quota")) return "额度不足，请减少张数或联系管理员调整额度。";
  if (normalized.includes("request contains sensitive word")) return "请求内容命中了敏感词规则，请调整提示词后再试。";

  return message;
}

function describeRegisterError(error: unknown) {
  const message = error instanceof Error ? error.message : "操作失败";
  const code = typeof error === "object" && error && "code" in error ? String((error as { code?: unknown }).code || "") : "";
  const normalized = message.toLowerCase();

  if (code === "registration_disabled") {
    if (normalized.includes("quota is full")) return "当前注册名额已满，暂时无法继续注册。";
    return "当前暂未开放公开注册。";
  }
  if (code === "email_exists") return "这个邮箱已经注册过了，可以直接登录。";
  if (code === "bad_request") {
    if (normalized.includes("invalid email address")) return "邮箱格式不正确，请检查后重试。";
    if (normalized.includes("email domain is not allowed")) return "该邮箱后缀暂不支持注册，请更换符合要求的邮箱。";
    if (normalized.includes("verification code")) return "请填写正确的邮箱验证码。";
    if (normalized.includes("name")) return "请输入名字后再继续。";
    if (normalized.includes("email")) return "请输入邮箱后再继续。";
  }
  if (code === "verification_failed") {
    if (normalized.includes("expired")) return "验证码已过期，请重新发送。";
    if (normalized.includes("invalid")) return "验证码不正确，请重新输入。";
    return "验证码校验失败，请重试。";
  }
  if (code === "register_code_cooldown") {
    const match = message.match(/(\d+)/);
    return match ? `发送过于频繁，请等待 ${match[1]} 秒后再试。` : "发送过于频繁，请稍后再试。";
  }
  if (code === "smtp_config_invalid") return "管理员尚未完成注册邮件配置，暂时无法发送验证码。";
  if (code === "register_send_code_failed") return "验证码发送失败，请稍后再试；如果多次失败，请联系管理员检查邮件配置。";
  if (code === "create_user_failed") {
    if (normalized.includes("already exists") || normalized.includes("duplicate")) return "这个邮箱已经注册过了，可以直接登录。";
    return "注册失败，请稍后重试。";
  }
  if (code === "session_error") return "注册成功，但登录态创建失败，请稍后直接登录。";
  if (normalized.includes("failed to fetch") || normalized.includes("networkerror") || normalized.includes("network request failed")) {
    return "网络连接失败，请确认服务在线后重试。";
  }
  return message;
}

function extractWorkbenchTaskError(task: ImageTask, fallback?: string) {
  const raw = task.error || fallback || "";
  if (!raw) return "";
  return describeWorkbenchError(new Error(raw));
}

function formatUserQuotaBreakdown(user: User) {
  if (user.quota_unlimited) return "无限额度";
  const temporaryValue = Number(user.temporary_quota || 0);
  const temporaryDate = user.temporary_quota_date?.trim();
  const temporaryPart = temporaryValue > 0
    ? `当天 ${temporaryValue}${temporaryDate ? ` · ${temporaryDate}` : ""}`
    : "当天 0";
  return `永久 ${compact(user.permanent_quota || 0)} · ${temporaryPart}`;
}

function formatUserQuotaUsage(user: User) {
  return `今日 ${compact(Number(user.quota_used_today || 0))} · 累计 ${compact(Number(user.quota_used_total || 0))}`;
}

function localDayString(date = new Date()) {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function IconButton({ children, className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button className={classNames("icon-button", className)} {...props}>{children}</button>;
}

function ControlField({ label, className, children }: { label: string; className?: string; children: React.ReactNode }) {
  return <label className={classNames("control-field", className)}><span>{label}</span>{children}</label>;
}

function SearchControl({ label = "搜索", value, onChange, placeholder }: { label?: string; value: string; onChange: (event: React.ChangeEvent<HTMLInputElement>) => void; placeholder: string }) {
  return <ControlField label={label} className="control-field-search"><div className="searchbox"><Search size={16} /><input value={value} onChange={onChange} placeholder={placeholder} /></div></ControlField>;
}

function ScrollableTable({ tableRef, className, height = "medium", children }: { tableRef: React.RefObject<HTMLDivElement | null>; className?: string; height?: "medium" | "large" | "tall"; children: React.ReactNode }) {
  return <div ref={tableRef} className={classNames("table-wrap", "table-scroll-shell", className, `table-height-${height}`)}>{children}</div>;
}

function App() {
  const [token, setToken] = useState(getStoredToken());
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [currentUser, setCurrentUser] = useState<User | null>(null);
  const [modelPolicy, setModelPolicy] = useState<ModelPolicy>({});
  const [activeTab, setActiveTab] = useState<Tab>("dashboard");
  const [adminMode, setAdminMode] = useState(false);
  const [version, setVersion] = useState("-");
  const [healthBadge, setHealthBadge] = useState<"healthy" | "offline">("offline");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [accountSummary, setAccountSummary] = useState<AccountListSummary | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [models, setModels] = useState<ModelItem[]>([]);
  const [tasks, setTasks] = useState<ImageTask[]>([]);
  const [taskTotal, setTaskTotal] = useState(0);
  const [images, setImages] = useState<StoredImage[]>([]);
  const [settings, setSettings] = useState<SettingsType>({});
  const [registerRuntime, setRegisterRuntime] = useState<RegisterRuntime | null>(null);
  const [accountRefreshStatus, setAccountRefreshStatus] = useState<AccountRefreshStatus | null>(null);
  const [logs, setLogs] = useState<SystemLog[]>([]);
  const [storageStatus, setStorageStatus] = useState("-");
  const [busy, setBusy] = useState<string | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [lightbox, setLightbox] = useState<{ src: string; title?: string } | null>(null);
  const [loginError, setLoginError] = useState("");
  const [registerStatus, setRegisterStatus] = useState<RegisterStatus | null>(null);

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
    setCurrentUser(me.user || null);
    setModelPolicy(me.model_policy || {});
    setHealthBadge("healthy");
    if (me.identity.role !== "admin") setAdminMode(false);
    await refreshAll(currentToken, me.identity.role === "admin");
  }

  async function refreshCurrentUser(currentToken = token) {
    if (!currentToken) return;
    try {
      const me = await api.me(currentToken);
      setCurrentUser(me.user || null);
      setModelPolicy(me.model_policy || {});
    } catch {
      // Keep workbench quota refresh quiet.
    }
  }

  async function refreshAll(currentToken = token, admin = isAdmin) {
    const common = admin
      ? [
          api.models(currentToken).then((data) => setModels(data.data || [])),
          api.tasks(currentToken, [], { page: 1, pageSize: 25 }).then((data) => {
            setTasks(data.items || []);
            setTaskTotal(Number(data.total || 0));
          })
        ]
      : [];
    const adminLoads = admin
      ? [
          api.accounts(currentToken, { page: 1, pageSize: 1 }).then((data) => {
            setAccounts(data.items || []);
            setAccountSummary(data.summary || null);
          }),
          api.users(currentToken, { page: 1, pageSize: 25 }).then((data) => setUsers(data.items || [])),
          api.images(currentToken, { page: 1, pageSize: 24 }).then((data) => setImages(data.items || [])),
          api.settings(currentToken).then((data) => setSettings(data.config || {})),
          api.accountRefreshStatus(currentToken).then((data) => setAccountRefreshStatus(data.status || null)),
          api.registerState(currentToken).then((data) => setRegisterRuntime(data)),
          api.logs(currentToken, "", [], { page: 1, pageSize: 25 }).then((data) => setLogs(data.items || [])),
          api.storage(currentToken).then((data) => setStorageStatus(`${data.backend.type} · ${data.health.status}`))
        ]
      : [];
    await Promise.allSettled([...common, ...adminLoads]);
  }

  useEffect(() => {
      setModelPolicy((current) => ({
      ...current,
      workbench_model: String(settings.image_workbench_model || current.workbench_model || "gpt-image-2"),
      image_max_count: Number(settings.image_max_count || current.image_max_count || 4),
      allowed_public_models: Array.isArray(settings.allowed_public_models)
        ? settings.allowed_public_models.map((item) => String(item))
        : current.allowed_public_models
    }));
  }, [settings]);

  useEffect(() => {
    let cancelled = false;
    const pollHealth = () => {
      api.health().then((data) => {
        if (cancelled) return;
        setHealthBadge(data.ok ? "healthy" : "offline");
        if (!token && data.version) {
          setVersion(data.version || "-");
        }
      }).catch(() => {
        if (!cancelled) setHealthBadge("offline");
      });
    };
    pollHealth();
    const timer = window.setInterval(pollHealth, 15000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [token]);

  useEffect(() => {
    if (token) return;
    let cancelled = false;
    const load = () => {
      api.registerStatus().then((data) => {
        if (!cancelled) {
          setRegisterStatus(data.status || null);
        }
      }).catch(() => {
        if (!cancelled) {
          setRegisterStatus(null);
        }
      });
    };
    load();
    return () => {
      cancelled = true;
    };
  }, [token]);

  useEffect(() => {
    if (!token) {
      api.version("").then((data) => setVersion(data.version || "-")).catch(() => {});
      return;
    }
    bootstrap(token).catch(() => {
      setStoredToken("");
      setToken("");
      setIdentity(null);
      setCurrentUser(null);
      setHealthBadge("offline");
    });
  }, []);

  useEffect(() => {
    if (!token || !isAdmin || !registerRuntime?.running) return;
    const timer = window.setInterval(() => {
      api.registerState(token).then(setRegisterRuntime).catch(() => {});
    }, 3000);
    return () => window.clearInterval(timer);
  }, [token, isAdmin, registerRuntime?.running]);

  useEffect(() => {
    if (!token || !isAdmin || activeTab !== "accounts") return;
    let cancelled = false;
    const poll = () => {
      Promise.allSettled([api.accountRefreshStatus(token)]).then((results) => {
        if (cancelled) return;
        const statusResult = results[0];
        if (statusResult.status === "fulfilled") {
          setAccountRefreshStatus(statusResult.value.status || null);
        }
      }).catch(() => {});
    };
    poll();
    const timer = window.setInterval(poll, 4000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [token, isAdmin, activeTab]);

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

  async function submitRegister(email: string, name: string, password: string, verificationCode: string) {
    if (busy) return;
    setBusy("login");
    setLoginError("");
    try {
      const data = await api.registerWithPassword({
        email: email.trim(),
        name: name.trim(),
        password,
        verification_code: verificationCode.trim()
      });
      setStoredToken(data.token);
      setToken(data.token);
      await bootstrap(data.token);
    } catch (error) {
      setLoginError(describeRegisterError(error));
    } finally {
      setBusy(null);
    }
  }

  function logout() {
    setStoredToken("");
    setToken("");
    setIdentity(null);
    setCurrentUser(null);
    setActiveTab("dashboard");
    setAdminMode(false);
  }

  if (!token || !identity) {
    return <LoginView busy={busy === "login"} error={loginError} version={version} healthBadge={healthBadge} registerStatus={registerStatus} onLogin={submitLogin} onRegister={submitRegister} />;
  }

  if (!adminMode || !isAdmin) {
    return (
        <ImageHome
          token={token}
          identity={identity}
          user={currentUser}
          version={version}
          healthBadge={healthBadge}
          modelPolicy={modelPolicy}
          isAdmin={Boolean(isAdmin)}
          quotaLabel={quotaLabelFromUser(currentUser)}
          refreshUserState={refreshCurrentUser}
          setTasks={setTasks}
          setTaskTotal={setTaskTotal}
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
            <HealthStatusBadge status={healthBadge} />
            <button className="secondary" disabled={busy === "refresh"} onClick={() => runBusy("refresh", () => refreshAll())}>
              {busy === "refresh" ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}
              刷新
            </button>
          </div>
        </header>

        {activeTab === "dashboard" && isAdmin && <Dashboard accountSummary={accountSummary} models={models} tasks={tasks} taskTotal={taskTotal} storageStatus={storageStatus} onReloadModels={() => runBusy("models", async () => setModels((await api.models(token)).data || []))} />}
        {activeTab === "accounts" && isAdmin && <AccountsPanel token={token} refreshIntervalMinutes={Number(settings.refresh_account_interval_minute || 5)} refreshStatus={accountRefreshStatus} setAccountSummary={setAccountSummary} toast={toast} busy={busy} runBusy={runBusy} />}
        {activeTab === "register" && isAdmin && <RegisterPanel token={token} registerRuntime={registerRuntime} setRegisterRuntime={setRegisterRuntime} toast={toast} />}
        {activeTab === "activity" && isAdmin && <ActivityPanel token={token} tasks={tasks} setTasks={setTasks} setTaskTotal={setTaskTotal} logs={logs} setLogs={setLogs} openLightbox={(src, title) => setLightbox({ src, title })} toast={toast} />}
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

function LoginView({ busy, error, version, healthBadge, registerStatus, onLogin, onRegister }: { busy: boolean; error: string; version: string; healthBadge: "healthy" | "offline"; registerStatus: RegisterStatus | null; onLogin: (email: string, password: string) => void; onRegister: (email: string, name: string, password: string, verificationCode: string) => void }) {
  const [mode, setMode] = useState<"login" | "register">("login");
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [verificationCode, setVerificationCode] = useState("");
  const [localError, setLocalError] = useState("");
  const [notice, setNotice] = useState("");
  const [agreementChecked, setAgreementChecked] = useState(false);
  const [sendingCode, setSendingCode] = useState(false);
  const [cooldownUntil, setCooldownUntil] = useState<number>(0);
  const [cooldownNow, setCooldownNow] = useState(Date.now());
  const registerDisabled = mode === "register" && registerStatus !== null && !registerStatus.can_register;
  const cooldownRemaining = Math.max(0, Math.ceil((cooldownUntil - cooldownNow) / 1000));

  useEffect(() => {
    if (!cooldownUntil) return;
    const timer = window.setInterval(() => setCooldownNow(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, [cooldownUntil]);

  useEffect(() => {
    if (cooldownRemaining <= 0) {
      setCooldownUntil(0);
    }
  }, [cooldownRemaining]);

  async function sendCode() {
    const value = email.trim();
    setLocalError("");
    setNotice("");
    if (registerDisabled) {
      setLocalError(registerStatus?.disabled_reason || "当前无法注册");
      return;
    }
    if (cooldownRemaining > 0) {
      setLocalError(`请等待 ${cooldownRemaining}s 后再发送验证码`);
      return;
    }
    if (!value) {
      setLocalError("请输入邮箱");
      return;
    }
    setSendingCode(true);
    try {
      await api.sendRegisterCode(value);
      const seconds = Math.max(1, Number(registerStatus?.code_cooldown_seconds || 60));
      setCooldownUntil(Date.now() + seconds * 1000);
      setCooldownNow(Date.now());
      setNotice(`验证码已发送到 ${value}，请注意查收。`);
    } catch (error) {
      setLocalError(describeRegisterError(error));
    } finally {
      setSendingCode(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    setLocalError("");
    if (!email.trim() || !password.trim()) {
      setLocalError("请输入邮箱和密码");
      return;
    }
    if (mode === "register") {
      if (registerDisabled) {
        setLocalError(registerStatus?.disabled_reason || "当前无法注册");
        return;
      }
      if (!name.trim()) {
        setLocalError("请输入名字");
        return;
      }
      if (!verificationCode.trim()) {
        setLocalError("请输入邮箱验证码");
        return;
      }
      if (!confirmPassword.trim()) {
        setLocalError("请再次输入密码");
        return;
      }
      if (password !== confirmPassword) {
        setLocalError("两次输入的密码不一致，请重新检查");
        return;
      }
      if (!agreementChecked) {
        setLocalError("请先阅读并确认使用说明");
        return;
      }
      onRegister(email, name, password, verificationCode);
      return;
    }
    onLogin(email, password);
  }

  useEffect(() => {
    setLocalError("");
    setNotice("");
    setVerificationCode("");
    setConfirmPassword("");
  }, [mode]);

  const quotaText = registerStatus
    ? registerStatus.max_ordinary_users > 0
      ? `${Math.max(0, registerStatus.remaining_ordinary)}/${registerStatus.max_ordinary_users}`
      : "∞"
    : "-";
  const domainText = registerStatus?.allowed_email_domains?.length
    ? registerStatus.allowed_email_domains.map((item) => `@${item}`).join("、")
    : "不限";

  return (
    <main className="login-view">
      <form className="login-panel" onSubmit={submit}>
        <HealthStatusBadge status={healthBadge} className="login-status-badge" />
        <div className="brand login-brand">
          <div className="brand-mark">GI</div>
          <div>
            <strong>GPT Image Web</strong>
            <span>{version && version !== "-" ? `v${version}` : mode === "login" ? "继续登录" : "创建账号"}</span>
          </div>
        </div>
        <div className="auth-switch">
          <button type="button" className={classNames(mode === "login" && "active")} onClick={() => setMode("login")}>登录</button>
          <button type="button" className={classNames(mode === "register" && "active")} onClick={() => setMode("register")}>注册</button>
        </div>
        {mode === "register" ? (
          <div className="auth-meta">
            <span>剩余名额 {quotaText}</span>
            <span>邮箱后缀 {domainText}</span>
          </div>
        ) : null}
        {mode === "register" ? <label><span>Name</span><input value={name} onChange={(event) => setName(event.target.value)} autoComplete="name" placeholder="你的名字" /></label> : null}
        <label><span>Email</span><input value={email} onChange={(event) => setEmail(event.target.value)} autoComplete="username" placeholder="cc98@zju.edu.cn" /></label>
        {mode === "register" ? (
          <label>
            <span>Verification Code</span>
            <div className="verify-row">
              <input value={verificationCode} onChange={(event) => setVerificationCode(event.target.value)} inputMode="numeric" placeholder="6 位验证码" />
              <button type="button" className="secondary small" disabled={sendingCode || cooldownRemaining > 0 || registerDisabled} onClick={() => sendCode().catch((error) => setLocalError(error instanceof Error ? error.message : "验证码发送失败"))}>
                {sendingCode ? "发送中" : (cooldownRemaining > 0 ? `${cooldownRemaining}s` : "发送验证码")}
              </button>
            </div>
          </label>
        ) : null}
        <label><span>Password</span><input value={password} onChange={(event) => setPassword(event.target.value)} type="password" autoComplete={mode === "login" ? "current-password" : "new-password"} placeholder="账户密码" /></label>
        {mode === "register" ? <label><span>Confirm Password</span><input value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} type="password" autoComplete="new-password" placeholder="再次输入密码" /></label> : null}
        {mode === "register" ? (
          <div className="login-notice">
            <p>为完成功能实现，服务端可能会暂存你的部分账户信息、请求内容、任务日志和图片结果，并在用户使用结束后按系统策略删除。</p>
            <p>这些数据不会被用于非法用途，如果你介意相关暂存，请不要继续使用。</p>
            <p>本系统仅提供工具能力，用户应确保其输入、生成内容和使用行为合法合规；因违法违规使用产生的责任由用户本人承担。</p>
            <label className="login-agreement">
              <input type="checkbox" checked={agreementChecked} onChange={(event) => setAgreementChecked(event.target.checked)} />
              <span>我已阅读并同意上述说明，知悉数据与使用责任边界。</span>
            </label>
          </div>
        ) : null}
        {notice ? <p className="form-success">{notice}</p> : null}
        {mode === "register" && registerDisabled ? <p className="form-error">{describeRegisterError({ code: "registration_disabled", message: registerStatus?.disabled_reason || "public registration is disabled" })}</p> : null}
        {localError || error ? <p className="form-error">{localError || error}</p> : null}
        <button disabled={busy || registerDisabled}>{busy ? <LoaderCircle className="spin" size={16} /> : null}{mode === "login" ? "登录" : "注册"}</button>
      </form>
    </main>
  );
}

function ImageHome({
  token,
  identity,
  user,
  version,
  healthBadge,
  modelPolicy,
  isAdmin,
  quotaLabel,
  refreshUserState,
  setTasks,
  setTaskTotal,
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
  user: User | null;
  version: string;
  healthBadge: "healthy" | "offline";
  modelPolicy: ModelPolicy;
  isAdmin: boolean;
  quotaLabel: string;
  refreshUserState: () => Promise<void>;
  setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>;
  setTaskTotal: React.Dispatch<React.SetStateAction<number>>;
  setImages: React.Dispatch<React.SetStateAction<StoredImage[]>>;
  toast: (type: Toast["type"], message: string) => void;
  logout: () => void;
  openAdmin: () => void;
  openLightbox: (src: string, title?: string) => void;
  toasts: Toast[];
  lightbox: { src: string; title?: string } | null;
  closeLightbox: () => void;
}) {
  const identityMeta = [identity.name || "User", identity.role, version && version !== "-" ? version : ""].filter(Boolean).join(" · ");
  return (
    <div className="home-shell">
      <header className="home-header">
        <div className="brand home-brand">
          <div className="brand-mark">GI</div>
          <div>
            <strong>GPT Image Web</strong>
            <span>{identityMeta}</span>
          </div>
        </div>
        <div className="home-actions">
          <HealthStatusBadge status={healthBadge} />
          {isAdmin ? <button className="secondary compact" onClick={openAdmin}><LayoutDashboard size={15} />管理后台</button> : null}
          <button className="ghost compact" onClick={logout}><LogOut size={15} />退出</button>
        </div>
      </header>

      <ImageWorkbench token={token} identity={identity} user={user} modelPolicy={modelPolicy} quotaLabel={quotaLabel} refreshUserState={refreshUserState} canRefreshArchive={isAdmin} setTasks={setTasks} setTaskTotal={setTaskTotal} setImages={setImages} toast={toast} openLightbox={openLightbox} />

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

function Dashboard({ accountSummary, models, tasks, taskTotal, storageStatus, onReloadModels }: { accountSummary: AccountListSummary | null; models: ModelItem[]; tasks: ImageTask[]; taskTotal: number; storageStatus: string; onReloadModels: () => void }) {
  const totalAccounts = Number(accountSummary?.total || 0);
  const normal = Number(accountSummary?.normal || 0);
  const success = Number(accountSummary?.success || 0);
  const fail = Number(accountSummary?.fail || 0);
  const activeRequests = Number(accountSummary?.active_requests || 0);
  const totalConcurrency = Number(accountSummary?.total_concurrency || 0);
  const recent = tasks[0];
  return (
    <div className="stack">
      <div className="metrics">
        <Metric label="账号 / 正常" value={`${totalAccounts}/${normal}`} tone="ok" />
        <Metric label="可用额度" value={quotaLabelFromSummary(accountSummary)} />
        <Metric label="图片并发" value={`${activeRequests}/${totalConcurrency || 0}`} />
        <Metric label="任务总数" value={taskTotal} />
        <Metric label="成功 / 失败" value={`${success}/${fail}`} />
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
            <div><span>账号池</span><strong>{normal}/{totalAccounts} 正常</strong></div>
            <div><span>并发占用</span><strong>{activeRequests}/{totalConcurrency || 0}</strong></div>
            <div><span>最近任务</span><strong>{recent ? `${recent.status} · ${fmtDate(recent.updated_at)}` : "-"}</strong></div>
          </div>
        </section>
      </div>
    </div>
  );
}

function useHorizontalWheelScroll(ref: React.RefObject<HTMLDivElement | null>) {
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const handleWheel = (event: WheelEvent) => {
      const canScrollX = element.scrollWidth > element.clientWidth;
      const canScrollY = element.scrollHeight > element.clientHeight;
      if (!canScrollX) return;

      const primaryDelta = Math.abs(event.deltaX) > 0 ? event.deltaX : event.deltaY;
      const wantsHorizontal = event.shiftKey || Math.abs(event.deltaX) > Math.abs(event.deltaY);
      if (wantsHorizontal) {
        event.preventDefault();
        element.scrollLeft += primaryDelta;
        return;
      }

      if (!canScrollY || event.deltaY === 0) {
        event.preventDefault();
        element.scrollLeft += primaryDelta;
        return;
      }

      const atTop = element.scrollTop <= 0;
      const atBottom = Math.ceil(element.scrollTop + element.clientHeight) >= element.scrollHeight;
      const pushingPastTop = event.deltaY < 0 && atTop;
      const pushingPastBottom = event.deltaY > 0 && atBottom;
      if (pushingPastTop || pushingPastBottom) {
        event.preventDefault();
        element.scrollLeft += event.deltaY;
      }
    };
    element.addEventListener("wheel", handleWheel, { passive: false });
    return () => element.removeEventListener("wheel", handleWheel);
  }, [ref]);
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

function AccountsPanel({ token, refreshIntervalMinutes, refreshStatus, setAccountSummary, toast, busy, runBusy }: { token: string; refreshIntervalMinutes: number; refreshStatus: AccountRefreshStatus | null; setAccountSummary: React.Dispatch<React.SetStateAction<AccountListSummary | null>>; toast: (type: Toast["type"], message: string) => void; busy: string | null; runBusy: (id: string, fn: () => Promise<void>) => Promise<void> }) {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [summary, setSummary] = useState<AccountListSummary | null>(null);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [type, setType] = useState("");
  const [activeOnly, setActiveOnly] = useState(false);
  const [pageSize, setPageSize] = useState(25);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [selected, setSelected] = useState<string[]>([]);

  const types = useMemo(() => Array.from(new Set(accounts.map((item) => item.type || "Free"))), [accounts]);
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const selectedSet = new Set(selected);
  const tableWrapRef = useRef<HTMLDivElement | null>(null);
  useHorizontalWheelScroll(tableWrapRef);
  useEffect(() => setPage(1), [query, status, type, activeOnly, pageSize]);
  const reloadPageRef = useRef<(nextPage?: number) => Promise<void>>(async () => {});
  useEffect(() => {
    let cancelled = false;
    api.accounts(token, { page, pageSize, query, status, accountType: type, activeOnly }).then((data) => {
      if (cancelled) return;
      setAccounts(data.items || []);
      setTotal(Number(data.total || 0));
      setSummary(data.summary || null);
      setAccountSummary(data.summary || null);
      setSelected((prev) => prev.filter((ref) => (data.items || []).some((item) => item.token_ref === ref)));
    }).catch((error) => toast("error", error instanceof Error ? error.message : "加载账号失败"));
    return () => {
      cancelled = true;
    };
  }, [token, page, pageSize, query, status, type, activeOnly, setAccountSummary]);
  useEffect(() => {
    reloadPageRef.current = reloadPage;
  });
  useEffect(() => {
    let cancelled = false;
    const poll = () => {
      reloadPageRef.current(page).catch(() => {});
    };
    const timer = window.setInterval(() => {
      if (!cancelled) poll();
    }, 4000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [page]);

  async function reloadPage(nextPage = page) {
    const data = await api.accounts(token, { page: nextPage, pageSize, query, status, accountType: type, activeOnly });
    const nextItems = data.items || [];
    const nextTotal = Number(data.total || 0);
    const nextPageCount = Math.max(1, Math.ceil(nextTotal / pageSize));
    if (nextPage > nextPageCount) {
      setPage(nextPageCount);
      return;
    }
    setAccounts(nextItems);
    setTotal(nextTotal);
    setSummary(data.summary || null);
    setAccountSummary(data.summary || null);
    setSelected((prev) => prev.filter((ref) => nextItems.some((item) => item.token_ref === ref)));
  }
  async function refresh(refs = selected) {
    const result = await api.refreshAccounts(token, refs);
    await reloadPage();
    toast(result.errors.length ? "error" : "success", `刷新成功 ${result.refreshed} 个${result.errors.length ? `，失败 ${result.errors.length} 个` : ""}`);
  }
  async function refreshDue() {
    const result = await api.refreshDueAccounts(token);
    await reloadPage();
    toast(result.errors.length ? "error" : "success", `待刷新选中 ${result.selected} 个，成功 ${result.refreshed} 个${result.errors.length ? `，失败 ${result.errors.length} 个` : ""}`);
  }
  async function remove(refs: string[]) {
    if (!refs.length || !confirm(`删除 ${refs.length} 个账号？`)) return;
    const result = await api.deleteAccounts(token, refs);
    setSelected([]);
    await reloadPage(page);
    toast("success", `已删除 ${result.removed} 个账号`);
  }
  async function update(ref: string, body: { type?: string; status?: string; quota?: number; password?: string; max_concurrency?: number }) {
    await api.updateAccount(token, ref, body);
    await reloadPage();
  }

  return (
    <section className="panel">
      <PanelHead title="账号池" subtitle={`筛选、刷新和维护 ChatGPT access_token · 自动刷新间隔 ${refreshIntervalMinutes} 分钟`} action={<><button className="secondary" disabled={busy === "refresh-due-accounts"} onClick={() => runBusy("refresh-due-accounts", refreshDue)}>刷新待刷新</button><button className="secondary" disabled={busy === "refresh-all-accounts"} onClick={() => runBusy("refresh-all-accounts", () => refresh([]))}>刷新全部</button><button className="secondary-danger" disabled={busy === "remove-bad"} onClick={() => runBusy("remove-bad", () => remove(accounts.filter((item) => item.status === "异常").map((item) => item.token_ref)))}>移除异常</button></>} />
      <div className="auto-refresh-bar">
        <span className={classNames("badge", refreshStatus?.running ? "warn" : "ok")}>{refreshStatus?.running ? "自动刷新中" : "自动刷新空闲"}</span>
        <span className="chip">并发 {Number(refreshStatus?.concurrency || 0)}</span>
        <span className="chip">正常轮转批量 {Number(refreshStatus?.normal_batch_size || 0)}</span>
        <span className="chip">间隔 {Number(refreshStatus?.interval_minutes || refreshIntervalMinutes || 0)} 分钟</span>
        <span className="chip">待刷新 {Number(refreshStatus?.due_count || 0)}</span>
        <span className="chip">占用 {Number(summary?.active_requests || 0)} / 总并发 {Number(summary?.total_concurrency || 0)}</span>
        <span className="chip">下次 {fmtDate(refreshStatus?.next_run_at)}</span>
        <span className="chip">上次刷新 {Number(refreshStatus?.last_refreshed || 0)}</span>
        <span className="chip">上次失败 {Number(refreshStatus?.last_failed || 0)}</span>
        <span className="chip">本轮选择 {Number(refreshStatus?.last_selected || 0)}</span>
        <span className="chip">限流 {Number(refreshStatus?.last_limited || 0)} / 正常 {Number(refreshStatus?.last_normal || 0)}</span>
        <span className="chip">耗时 {formatDuration(refreshStatus?.last_duration_ms)}</span>
      </div>
      {refreshStatus?.last_error ? <p className="detail-error">{refreshStatus.last_error}</p> : null}
      <div className="filters filters-card">
        <SearchControl value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索邮箱、token ref、密码、类型" />
        <ControlField label="状态"><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">全部状态</option><option>正常</option><option>限流</option><option>异常</option><option>禁用</option></select></ControlField>
        <ControlField label="类型"><select value={type} onChange={(event) => setType(event.target.value)}><option value="">全部类型</option>{types.map((item) => <option key={item}>{item}</option>)}</select></ControlField>
        <ControlField label="占用"><select value={activeOnly ? "busy" : ""} onChange={(event) => setActiveOnly(event.target.value === "busy")}><option value="">全部</option><option value="busy">仅看占用中</option></select></ControlField>
        <ControlField label="每页"><select value={pageSize} onChange={(event) => setPageSize(Number(event.target.value))}><option>10</option><option>25</option><option>50</option><option>100</option></select></ControlField>
      </div>
      <div className="bulkbar">
        <label className="inline"><input type="checkbox" checked={accounts.length > 0 && accounts.every((item) => selectedSet.has(item.token_ref))} onChange={(event) => {
          setSelected((prev) => event.target.checked ? Array.from(new Set([...prev, ...accounts.map((item) => item.token_ref)])) : prev.filter((ref) => !accounts.some((item) => item.token_ref === ref)));
        }} /><span>选择当前页</span></label>
        <span>已选择 {selected.length} 项</span>
        <button className="ghost small" disabled={!selected.length || busy === "refresh-selected"} onClick={() => runBusy("refresh-selected", () => refresh(selected))}>刷新选中</button>
        <button className="ghost small" disabled={!selected.length || busy === "disable-selected"} onClick={() => runBusy("disable-selected", async () => { for (const ref of selected) await update(ref, { status: "禁用" }); toast("success", "已停用选中账号"); })}>停用选中</button>
        <button className="danger small" disabled={!selected.length || busy === "delete-selected"} onClick={() => runBusy("delete-selected", () => remove(selected))}>删除选中</button>
      </div>
      <datalist id="account-type-options">{types.map((value) => <option key={value}>{value}</option>)}</datalist>
      <ScrollableTable tableRef={tableWrapRef} className="account-table-wrap" height="large">
        <table className="accounts-table">
          <thead><tr><th></th><th>Email</th><th>Token</th><th>密码</th><th>类型</th><th>状态</th><th>额度</th><th>图片并发</th><th>恢复</th><th>预计下次刷新</th><th>成功/失败</th><th>最近使用</th><th></th></tr></thead>
          <tbody>{accounts.map((item) => (
            <AccountRow
              key={item.token_ref}
              item={item}
              refreshIntervalMinutes={refreshIntervalMinutes}
              selected={selectedSet.has(item.token_ref)}
              onSelect={(checked) => setSelected((prev) => checked ? [...prev, item.token_ref] : prev.filter((ref) => ref !== item.token_ref))}
              onRefresh={() => runBusy("refresh-one", () => refresh([item.token_ref]))}
              onToggle={() => runBusy("toggle-one", async () => update(item.token_ref, { status: item.status === "禁用" ? "正常" : "禁用" }))}
              onDelete={() => runBusy("delete-one", () => remove([item.token_ref]))}
              onSave={async (body) => {
                await update(item.token_ref, body);
                toast("success", "账号已更新");
              }}
              busy={busy}
              toast={toast}
            />
          ))}</tbody>
        </table>
      </ScrollableTable>
      <div className="pager"><button className="ghost small" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>上一页</button><span>{page} / {pageCount} · {total} 项</span><button className="ghost small" disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))}>下一页</button></div>
    </section>
  );
}

function AccountRow({ item, refreshIntervalMinutes, selected, onSelect, onRefresh, onToggle, onDelete, onSave, busy, toast }: { item: Account; refreshIntervalMinutes: number; selected: boolean; onSelect: (checked: boolean) => void; onRefresh: () => void; onToggle: () => void; onDelete: () => void; onSave: (body: { type?: string; status?: string; quota?: number; password?: string; max_concurrency?: number }) => Promise<void>; busy: string | null; toast: (type: Toast["type"], message: string) => void }) {
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [revealPassword, setRevealPassword] = useState(false);
  const [draft, setDraft] = useState({ type: item.type || "Free", status: item.status || "正常", quota: String(item.quota ?? 0), password: item.password || "", maxConcurrency: String(item.max_concurrency ?? 0) });
  useEffect(() => {
    if (!editing) setDraft({ type: item.type || "Free", status: item.status || "正常", quota: String(item.quota ?? 0), password: item.password || "", maxConcurrency: String(item.max_concurrency ?? 0) });
  }, [editing, item.type, item.status, item.quota, item.password, item.max_concurrency]);
  async function save() {
    setSaving(true);
    try {
      await onSave({ type: draft.type.trim() || "Free", status: draft.status, quota: Number(draft.quota) || 0, password: draft.password.trim(), max_concurrency: Math.max(0, Number(draft.maxConcurrency) || 0) });
      setEditing(false);
    } finally {
      setSaving(false);
    }
  }
  return (
    <tr>
      <td><input type="checkbox" checked={selected} onChange={(event) => onSelect(event.target.checked)} /></td>
      <td>{item.email || "-"}</td>
      <td><code>{item.access_token_masked || item.token_ref}</code><small>{item.token_ref}</small></td>
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
      <td>{editing ? <select className="cell-input" value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}><option>正常</option><option>限流</option><option>异常</option><option>禁用</option></select> : <div className="account-status-cell"><Badge value={item.status} />{item.recovery_state === "recovering" ? <small className="status-subtle warn">重登恢复中</small> : item.recovery_state === "recover_requires_otp" ? <small className="status-subtle warn" title={item.recovery_error || "-"}>重登需验证码</small> : item.recovery_state === "recover_failed" ? <small className="status-subtle err" title={item.recovery_error || "-"}>重登恢复失败</small> : null}</div>}</td>
      <td>{editing ? <input className="cell-input" type="number" min={0} value={draft.quota} onChange={(event) => setDraft({ ...draft, quota: event.target.value })} /> : formatQuota(item)}</td>
      <td>{editing ? <input className="cell-input" type="number" min={0} value={draft.maxConcurrency} onChange={(event) => setDraft({ ...draft, maxConcurrency: event.target.value })} /> : `${Number(item.active_requests || 0)}/${Number(item.allowed_concurrency || 0)}`}</td>
      <td title={item.restore_at ? fmtDate(item.restore_at) : "-"}>{formatRestoreCountdown(item.restore_at)}</td>
      <td title={formatNextAutoRefreshTitle(item.updated_at, refreshIntervalMinutes)}>{formatNextAutoRefresh(item.updated_at, refreshIntervalMinutes)}</td>
      <td>{item.success}/{item.fail}</td>
      <td>{fmtDate(item.last_used_at)}</td>
      <td className="accounts-actions-cell"><div className="row-actions">
        {editing ? (
          <>
            <button className="secondary small" disabled={saving} onClick={save}>{saving ? "保存中" : "保存"}</button>
            <button className="ghost small" disabled={saving} onClick={() => setEditing(false)}>取消</button>
          </>
        ) : (
          <>
            <button className="ghost small" onClick={() => setEditing(true)}>编辑</button>
            <IconButton title="刷新" disabled={busy === "refresh-one"} onClick={onRefresh}><RefreshCw size={15} /></IconButton>
            <IconButton title={item.status === "禁用" ? "启用" : "禁用"} disabled={busy === "toggle-one"} onClick={onToggle}><Ban size={15} /></IconButton>
            <IconButton title="删除" className="danger-icon" disabled={busy === "delete-one"} onClick={onDelete}><Trash2 size={15} /></IconButton>
          </>
        )}
      </div></td>
    </tr>
  );
}

function ImageWorkbench({ token, identity, user, modelPolicy, quotaLabel, refreshUserState, canRefreshArchive, setTasks, setTaskTotal, setImages, toast, openLightbox }: { token: string; identity: Identity; user: User | null; modelPolicy: ModelPolicy; quotaLabel: string; refreshUserState: () => Promise<void>; canRefreshArchive: boolean; setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>; setTaskTotal: React.Dispatch<React.SetStateAction<number>>; setImages: React.Dispatch<React.SetStateAction<StoredImage[]>>; toast: (type: Toast["type"], message: string) => void; openLightbox: (src: string, title?: string) => void }) {
  const [prompt, setPrompt] = useState("");
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
  const quota = quotaLabel;
  const model = String(modelPolicy.workbench_model || "gpt-image-2");
  const maxCount = Math.max(1, Number(modelPolicy.image_max_count || 4));
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

  useEffect(() => {
    setCount((current) => Math.max(1, Math.min(maxCount, current || 1)));
  }, [maxCount]);

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
        setTaskTotal((value) => Math.max(value, Number(data.total || value)));
        applyTaskUpdates(data.items);
        refreshUserState().catch(() => {});
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
          phase: task.phase === "waiting_slot" ? "waiting_slot" : task.phase === "processing" ? "task" : task.phase === "finished" ? item.phase : item.phase,
          status: taskStatusToWorkbench(task.status),
          startedAt: item.startedAt || task.created_at || turn.createdAt,
          image: result || item.image,
          error: extractWorkbenchTaskError(task, item.error)
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
            ? { ...image, phase: task.phase === "waiting_slot" ? "waiting_slot" : task.phase === "processing" ? "task" : image.phase, taskId: task.id, status: taskStatusToWorkbench(task.status), startedAt: image.startedAt || task.created_at || turn.createdAt, image: parseTaskData(task.data)[0] || image.image, error: task.error }
            : image);
          return { ...row, images, status: deriveTurnStatus(images), error: images.find((image) => image.error)?.error };
        }));
      } catch (error) {
        failed += 1;
        const message = describeWorkbenchError(error);
        setTurns((current) => current.map((row) => {
          if (row.id !== turn.id) return row;
          const images = row.images.map((image) => image.id === imageId ? { ...image, status: "error" as const, startedAt: image.startedAt || turn.createdAt, error: message } : image);
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
      setTurns((current) => current.map((row) => row.id === turn.id ? {
        ...row,
        status: "running",
        error: undefined,
        images: row.images.map((image) => ({
          ...image,
          phase: "task",
          status: "running",
          error: undefined,
          startedAt: image.startedAt || turn.createdAt
        }))
      } : row));
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
      try {
        const latestTasks = await api.tasks(token, [], { page: 1, pageSize: 8 });
        setTasks((current) => mergeImageTasks(current, latestTasks.items || []));
        setTaskTotal((value) => Math.max(value, Number(latestTasks.total || value)));
      } catch {
        // Keep sync generation smooth even if task list refresh fails.
      }
      if (canRefreshArchive) {
        api.images(token).then((data) => setImages(data.items || [])).catch(() => {});
      }
    } catch (error) {
      const message = describeWorkbenchError(error);
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
      const countValue = Math.max(1, Math.min(maxCount, count || 1));
      const sizeValue = normalizeWorkbenchSize(size);
      const mode = refs.length ? "edit" : "generate";
      const startedAt = new Date().toISOString();
      const images: WorkbenchItem[] = Array.from({ length: countValue }, () => ({ id: createID("img"), phase: "submitting", status: "queued", prompt: text, model, size: sizeValue, startedAt }));
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
        createdAt: startedAt
      };
      setTurns((current) => [turn, ...current]);
      setActiveTurnId(turn.id);
      setPrompt("");
      setRefs([]);
      if (asyncMode) {
        await enqueueImages(turn);
        await refreshUserState();
        toast("success", `已提交 ${countValue} 个任务`);
      } else {
        await runSyncTurn(turn);
        await refreshUserState();
        toast("success", "图片已生成");
      }
    } catch (error) {
      refreshUserState().catch(() => {});
      toast("error", describeWorkbenchError(error));
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
    const retryStartedAt = new Date().toISOString();
    const retryItem: WorkbenchItem = { ...item, id: retryId, phase: "submitting", status: "queued", taskId: undefined, image: undefined, error: undefined, size: retrySize, startedAt: retryStartedAt };
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
      toast("error", describeWorkbenchError(error));
    }
  }

  async function regenerateTurn(turn: WorkbenchTurn) {
    const turnSize = normalizeWorkbenchSize(turn.size);
    const regenerateStartedAt = new Date().toISOString();
    const images = Array.from({ length: turn.count }, () => ({ id: createID("img"), phase: "submitting" as const, status: "queued" as const, prompt: turn.prompt, model: turn.model, size: turnSize, startedAt: regenerateStartedAt }));
    const nextTurn = { ...turn, id: createID("turn"), size: turnSize, images, status: "queued" as const, createdAt: regenerateStartedAt, error: undefined };
    setTurns((current) => [nextTurn, ...current]);
    setActiveTurnId(nextTurn.id);
    try {
      await enqueueImages(nextTurn);
      toast("success", "已重新生成");
    } catch (error) {
      toast("error", describeWorkbenchError(error));
    }
  }

  function reuseTurn(turn: WorkbenchTurn) {
    setPrompt(turn.prompt);
      setSize(normalizeWorkbenchSize(turn.size));
      setCount(turn.count);
      setRefs(turn.refs);
    setActiveTurnId(turn.id);
  }

  async function useAsReference(item: WorkbenchItem) {
    try {
      const ref = await buildReferenceFromResult(item, token);
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
                        const src = item.image ? imageSrc(item.image, token) : "";
                        if (!src || src.startsWith("data:")) return;
                        copyText(src.startsWith("http") ? src : `${location.origin}${src}`).then(() => toast("success", "已复制链接"));
                      }}
                      onDownload={() => {
                        const src = item.image ? imageSrc(item.image, token) : "";
                        if (!src) return;
                        downloadWorkbenchImage(src, `result-${turn.id}-${index + 1}.png`);
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
                <QuotaBadge user={user} quota={quota} />
                {activeTaskCount > 0 ? <span className="composer-pill running"><LoaderCircle className="spin" size={14} />{activeTaskCount} 处理中</span> : null}
                <label className="composer-field"><span>模型</span><input value={model} readOnly /></label>
                <label className="composer-field small-field"><span>比例</span><select value={size} onChange={(event) => setSize(event.target.value)}><option value="">默认</option><option>1:1</option><option>16:9</option><option>9:16</option><option>4:3</option><option>3:4</option></select></label>
                <label className="composer-field count-field"><span>张数</span><input type="number" min={1} max={maxCount} value={count} onChange={(event) => setCount(Math.max(1, Math.min(maxCount, Number(event.target.value) || 1)))} /></label>
                <span className="composer-pill subtle">上限 {maxCount}</span>
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

function ResultCard({ item, index, size, openLightbox, onUseAsReference, onRetry, onCopy, onDownload }: { item: WorkbenchItem; index: number; size?: string; openLightbox: (src: string, title?: string) => void; onUseAsReference: () => void; onRetry: () => void; onCopy: () => void; onDownload: () => void }) {
  const src = item.image ? imageSrc(item.image) : "";
  const loadingLabel = workbenchLoadingLabel(item, index);
  const waitingHint = workbenchWaitingHint(item);
  return (
    <article className={classNames("creation-image", sizeAspectClass(size), item.status === "error" && "error")}>
      {src ? (
        <button className="image-preview" onClick={() => openLightbox(src, item.prompt)}><img src={src} alt={item.prompt} loading="lazy" /></button>
      ) : (
        <div className="result-placeholder">
          {item.status === "error" ? <X size={22} /> : item.status === "queued" ? <Clock3 size={22} /> : <LoaderCircle className="spin" size={22} />}
          <span>{item.status === "error" ? turnStatusLabel(item.status) : loadingLabel}</span>
          {waitingHint ? <small>{waitingHint}</small> : null}
        </div>
      )}
      <div className="image-card-footer">
        <span>结果 {index + 1}</span>
        <Badge value={item.status} />
      </div>
      {src ? <div className="image-card-actions"><button className="ghost small" onClick={onUseAsReference}><Sparkles size={13} />加入编辑</button><IconButton title="下载图片" onClick={onDownload}><Download size={13} /></IconButton>{!src.startsWith("data:") ? <IconButton title="复制链接" onClick={onCopy}><Copy size={13} /></IconButton> : null}</div> : null}
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

function workbenchLoadingLabel(item: WorkbenchItem, index: number) {
  if (item.phase === "submitting") {
    return "提交任务中…";
  }
  if (item.phase === "waiting_slot") {
    return "任务已提交，正在等待可用并发";
  }
  const steps = item.status === "queued"
    ? [
      "正在为你分析画面需求",
      "正在准备进入绘制流程",
      "正在等待第一版画面生成"
    ]
    : [
      "正在分析画面需求",
      "正在组织构图与光影",
      "正在绘制草图轮廓",
      "正在细化材质与颜色",
      "正在整理最终画面"
    ];
  const started = Date.parse(item.startedAt || "");
  if (!Number.isFinite(started)) {
    return steps[index % steps.length];
  }
  const elapsed = Math.max(0, Date.now() - started);
  const stepIndex = Math.min(steps.length - 1, Math.floor(elapsed / 12000));
  return steps[stepIndex];
}

function workbenchWaitingHint(item: WorkbenchItem) {
  if (item.phase !== "waiting_slot" || item.status !== "queued") return "";
  const started = Date.parse(item.startedAt || "");
  if (!Number.isFinite(started)) return "";
  const elapsed = Date.now() - started;
  if (elapsed < 12000) return "通常会在 1 分钟左右开始处理。";
  if (elapsed < 45000) return "当前高峰，通常会在 1 分钟左右开始处理，请耐心等待。";
  return "当前高峰，已经排队一会儿了，通常仍会在约 1 分钟内进入处理。";
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

async function buildReferenceFromResult(item: WorkbenchItem, token?: string): Promise<ReferenceImage> {
  if (!item.image) throw new Error("没有可用图片");
  const src = imageSrc(item.image, token);
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

function downloadWorkbenchImage(src: string, name: string) {
  const link = document.createElement("a");
  link.href = src.startsWith("http") ? src : `${location.origin}${src}`;
  link.download = name;
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
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

function compareLogs(a: SystemLog, b: SystemLog) {
  const byTime = a.time.localeCompare(b.time);
  if (byTime !== 0) return byTime;
  return a.id.localeCompare(b.id);
}

function sortLogs(items: SystemLog[]) {
  return items.slice().sort(compareLogs);
}

function mergeLogs(current: SystemLog[], updates: SystemLog[]) {
  if (!updates.length) return current;
  const map = new Map(current.map((log) => [log.id, log]));
  updates.forEach((log) => {
    const previous = map.get(log.id);
    map.set(log.id, previous ? { ...previous, ...log, detail: hasLogDetail(log.detail) ? log.detail : previous.detail } : log);
  });
  return Array.from(map.values()).sort((a, b) => b.time.localeCompare(a.time) || b.id.localeCompare(a.id));
}

function mergeRegisterLogs(current: SystemLog[], updates: SystemLog[]) {
  if (!updates.length) return current;
  const map = new Map(current.map((log) => [log.id, log]));
  updates.forEach((log) => {
    const previous = map.get(log.id);
    map.set(log.id, previous ? { ...previous, ...log, detail: hasLogDetail(log.detail) ? log.detail : previous.detail } : log);
  });
  return Array.from(map.values()).sort(compareLogs);
}

function ActivityPanel({ token, tasks, setTasks, setTaskTotal, logs, setLogs, openLightbox, toast }: { token: string; tasks: ImageTask[]; setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>; setTaskTotal: React.Dispatch<React.SetStateAction<number>>; logs: SystemLog[]; setLogs: React.Dispatch<React.SetStateAction<SystemLog[]>>; openLightbox: (src: string, title?: string) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [view, setView] = useState<"tasks" | "logs">("tasks");
  async function refresh() {
    const [taskData, logData] = await Promise.all([api.tasks(token, [], { page: 1, pageSize: 25 }), api.logs(token, "", [], { page: 1, pageSize: 25 })]);
    setTasks(taskData.items || []);
    setTaskTotal(Number(taskData.total || 0));
    setLogs(logData.items || []);
    toast("success", "任务日志已刷新");
  }
  return (
    <section className="panel">
      <PanelHead
        title="任务中心"
        subtitle="图片任务独立保存生命周期，系统日志保留账号、调用与后台审计"
        action={<><div className="segmented"><button className={classNames(view === "tasks" && "active")} onClick={() => setView("tasks")}>任务</button><button className={classNames(view === "logs" && "active")} onClick={() => setView("logs")}>日志</button></div><button className="secondary" onClick={() => refresh().catch((error) => toast("error", error.message))}>刷新</button></>}
      />
      {view === "tasks"
        ? <TasksTable token={token} tasks={tasks} setTasks={setTasks} setTaskTotal={setTaskTotal} openLightbox={openLightbox} toast={toast} />
        : <LogsTable token={token} logs={logs} setLogs={setLogs} toast={toast} />}
    </section>
  );
}

function TasksTable({ token, tasks, setTasks, setTaskTotal, openLightbox, toast }: { token: string; tasks: ImageTask[]; setTasks: React.Dispatch<React.SetStateAction<ImageTask[]>>; setTaskTotal: React.Dispatch<React.SetStateAction<number>>; openLightbox: (src: string, title?: string) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [mode, setMode] = useState("");
  const [modelFilter, setModelFilter] = useState("");
  const [sizeFilter, setSizeFilter] = useState("");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [detailTaskID, setDetailTaskID] = useState<string | null>(null);
  const [loadingDetail, setLoadingDetail] = useState<string | null>(null);
  const [loadingPreview, setLoadingPreview] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [deletedScope, setDeletedScope] = useState("active");
  const [total, setTotal] = useState(0);
  const tableWrapRef = useRef<HTMLDivElement | null>(null);
  useHorizontalWheelScroll(tableWrapRef);
  const rows = tasks;
  const selectedSet = new Set(selected);
  const allVisibleSelected = rows.length > 0 && rows.every((task) => selectedSet.has(task.id));
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const taskQueryParams = { page, pageSize, query, status, mode, model: modelFilter, size: sizeFilter, dateFrom, dateTo, deleted: deletedScope === "deleted" ? "only" : "", includeDeleted: deletedScope !== "active" };
  useEffect(() => setPage(1), [query, status, mode, modelFilter, sizeFilter, dateFrom, dateTo, pageSize, deletedScope]);
  useEffect(() => {
    let cancelled = false;
    api.tasks(token, [], taskQueryParams).then((data) => {
      if (cancelled) return;
      setTasks(data.items || []);
      setTotal(Number(data.total || 0));
      setTaskTotal(Number(data.total || 0));
      setSelected((prev) => prev.filter((id) => (data.items || []).some((item) => item.id === id)));
    }).catch((error) => toast("error", error instanceof Error ? error.message : "加载任务失败"));
    return () => {
      cancelled = true;
    };
  }, [token, page, pageSize, query, status, mode, modelFilter, sizeFilter, dateFrom, dateTo, deletedScope, setTaskTotal]);
  const detailTask = detailTaskID ? rows.find((task) => task.id === detailTaskID) || null : null;
  function toggleVisible(checked: boolean) {
    const visibleIDs = rows.map((task) => task.id);
    setSelected((prev) => checked ? Array.from(new Set([...prev, ...visibleIDs])) : prev.filter((id) => !visibleIDs.includes(id)));
  }
  async function ensureTaskDetail(task: ImageTask) {
    if (task.data !== undefined) return task;
    const data = await api.tasks(token, [task.id], { includeDeleted: Boolean(task.deleted_at) });
    const item = (data.items || [])[0];
    if (!item) {
      throw new Error("任务详情不存在");
    }
    setTasks((current) => mergeImageTasks(current, [item]));
    return item;
  }
  async function openDetail(task: ImageTask) {
    setDetailTaskID(task.id);
    if (task.data !== undefined) return;
    setLoadingDetail(task.id);
    try {
      await ensureTaskDetail(task);
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "读取任务详情失败");
    } finally {
      setLoadingDetail(null);
    }
  }
  async function openPreview(task: ImageTask) {
    if (task.status !== "success") return;
    setLoadingPreview(task.id);
    try {
      const resolved = await ensureTaskDetail(task);
      const first = parseTaskData(resolved.data)[0];
      const src = first ? imageSrc(first, token) : "";
      if (!src) {
        toast("info", "该任务暂无可预览图片");
        return;
      }
      openLightbox(src, task.id);
    } catch (error) {
      toast("error", error instanceof Error ? error.message : "读取任务预览失败");
    } finally {
      setLoadingPreview(null);
    }
  }
  async function removeSelected() {
    if (!selected.length || !confirm(`删除 ${selected.length} 个图片任务？`)) return;
    const data = await api.deleteTasks(token, selected);
    const next = await api.tasks(token, [], taskQueryParams);
    setTasks(next.items || []);
    setTotal(Number(next.total || 0));
    setTaskTotal(Number(next.total || 0));
    setSelected([]);
    toast("success", `已删除 ${data.removed} 个任务`);
  }
  return (
    <>
      <div className="filters filters-card activity-filters">
        <SearchControl value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索任务 ID、模型、提示词、状态" />
        <ControlField label="状态"><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">全部状态</option><option>queued</option><option>running</option><option>success</option><option>error</option></select></ControlField>
        <ControlField label="模式"><select value={mode} onChange={(event) => setMode(event.target.value)}><option value="">全部模式</option><option value="generate">generate</option><option value="edit">edit</option></select></ControlField>
        <ControlField label="模型"><input value={modelFilter} onChange={(event) => setModelFilter(event.target.value)} placeholder="gpt-image-2" /></ControlField>
        <ControlField label="每页"><select value={pageSize} onChange={(event) => setPageSize(Number(event.target.value))}><option>10</option><option>25</option><option>50</option><option>100</option></select></ControlField>
        <ControlField label="范围"><select value={deletedScope} onChange={(event) => setDeletedScope(event.target.value)}><option value="active">未删除</option><option value="all">含已删除</option><option value="deleted">仅已删除</option></select></ControlField>
        <ControlField label="比例"><input value={sizeFilter} onChange={(event) => setSizeFilter(event.target.value)} placeholder="auto / 1:1" /></ControlField>
        <ControlField label="开始"><input type="date" value={dateFrom} onChange={(event) => setDateFrom(event.target.value)} /></ControlField>
        <ControlField label="结束"><input type="date" value={dateTo} onChange={(event) => setDateTo(event.target.value)} /></ControlField>
        <div className="filter-actions"><button className="secondary" onClick={() => api.tasks(token, [], taskQueryParams).then((data) => { setTasks(data.items || []); setTotal(Number(data.total || 0)); setTaskTotal(Number(data.total || 0)); toast("success", "任务已刷新"); })}>刷新任务</button><button className="ghost small" onClick={() => { setQuery(""); setStatus(""); setMode(""); setModelFilter(""); setSizeFilter(""); setDateFrom(""); setDateTo(""); setDeletedScope("active"); }}>重置</button><button className="danger" disabled={!selected.length} onClick={removeSelected}>删除选中</button></div>
      </div>
      <ScrollableTable tableRef={tableWrapRef} className="data-table-wrap" height="medium"><table className="activity-table task-table"><thead><tr><th><input type="checkbox" checked={allVisibleSelected} onChange={(event) => toggleVisible(event.target.checked)} aria-label="选择当前任务" /></th><th>ID</th><th>Mode</th><th>Status</th><th>Prompt</th><th>Model</th><th>Size</th><th>耗时</th><th>Result</th><th>Updated</th><th></th></tr></thead><tbody>{rows.map((task) => {
        const first = parseTaskData(task.data)[0];
        const src = first ? imageSrc(first, token) : "";
        const canPreview = Boolean(src) || task.status === "success";
        const deleted = Boolean(task.deleted_at);
        return (
          <tr key={task.id}>
            <td><input type="checkbox" disabled={deleted} checked={selectedSet.has(task.id)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, task.id] : prev.filter((id) => id !== task.id))} /></td>
            <td><code>{task.id}</code></td>
            <td>{task.mode}</td>
            <td>{deleted ? <Badge value="deleted" /> : <Badge value={task.status} />}</td>
            <td>{task.prompt || "-"}</td>
            <td>{task.model || "-"}</td>
            <td>{task.size || "-"}</td>
            <td>{taskDuration(task)}</td>
            <td>{canPreview ? <button className="link-button" onClick={() => openPreview(task)}>{loadingPreview === task.id ? "加载" : "预览"}</button> : "-"}</td>
            <td>{fmtDate(task.deleted_at || task.updated_at)}</td>
            <td><button className="ghost small" onClick={() => openDetail(task)}>{loadingDetail === task.id ? "加载" : "详情"}</button></td>
          </tr>
        );
      })}</tbody></table></ScrollableTable>
      <div className="pager"><button className="ghost small" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>上一页</button><span>{page} / {pageCount} · {total} 项</span><button className="ghost small" disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))}>下一页</button></div>
      <DetailModal title={detailTask ? `任务详情 · ${detailTask.id}` : "任务详情"} open={Boolean(detailTaskID)} onClose={() => setDetailTaskID(null)}>
        {loadingDetail === detailTaskID || !detailTask ? <div className="detail-panel detail-panel-plain">加载详情中...</div> : <TaskDetail token={token} task={detailTask} openLightbox={openLightbox} />}
      </DetailModal>
    </>
  );
}

function TaskDetail({ token, task, openLightbox }: { token: string; task: ImageTask; openLightbox: (src: string, title?: string) => void }) {
  const results = parseTaskData(task.data);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [eventsLoading, setEventsLoading] = useState(true);
  const [eventsError, setEventsError] = useState("");
  useEffect(() => {
    let cancelled = false;
    setEventsLoading(true);
    setEventsError("");
    setEvents([]);
    api.taskEvents(token, task.id).then((data) => {
      if (cancelled) return;
      setEvents(data.items || []);
    }).catch((error) => {
      if (cancelled) return;
      setEventsError(error instanceof Error ? error.message : "读取任务事件失败");
    }).finally(() => {
      if (!cancelled) setEventsLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [token, task.id]);
  return (
    <div className="detail-panel">
      <div className="detail-grid">
        <DetailItem label="任务 ID" value={task.id} code />
        <DetailItem label="状态" value={task.status} />
        <DetailItem label="创建时间" value={fmtDate(task.created_at)} />
        <DetailItem label="更新时间" value={fmtDate(task.updated_at)} />
        {task.deleted_at ? <DetailItem label="删除时间" value={fmtDate(task.deleted_at)} /> : null}
        {task.deleted_by ? <DetailItem label="删除人" value={task.deleted_by} code /> : null}
        <DetailItem label="生成时间" value={taskDuration(task)} />
        <DetailItem label="模型" value={task.model || "-"} />
        <DetailItem label="比例" value={task.size || "-"} />
        <DetailItem label="模式" value={task.mode} />
        <DetailItem label="请求张数" value={String(task.requested_count || results.length || 0)} />
      </div>
      {task.prompt ? <div className="detail-prompt"><span>提示词</span><p>{task.prompt}</p></div> : null}
      {task.error ? <p className="detail-error">{task.error}</p> : null}
      {results.length ? <div className="detail-images">{results.map((item, index) => { const src = imageSrc(item, token); return src ? <button key={index} onClick={() => openLightbox(src, task.id)}><img src={src} alt={`task ${index + 1}`} /></button> : null; })}</div> : null}
      <TaskEventTimeline events={events} loading={eventsLoading} error={eventsError} />
      <pre className="detail-json">{safeJSON({ ...task, data: results })}</pre>
    </div>
  );
}

function TaskEventTimeline({ events, loading, error }: { events: TaskEvent[]; loading: boolean; error: string }) {
  return (
    <div className="task-timeline">
      <div className="task-timeline-head">
        <strong>任务事件</strong>
        <span>{loading ? "加载中" : `${events.length} 条`}</span>
      </div>
      {error ? <p className="detail-error">{error}</p> : null}
      {!loading && !error && events.length === 0 ? <p className="task-timeline-empty">暂无事件记录</p> : null}
      {events.map((event) => {
        const detail = taskEventDetail(event);
        return (
          <article key={event.id} className="task-event">
            <time>{fmtDate(event.time)}</time>
            <div>
              <div className="task-event-title"><Badge value={event.type} /><strong>{event.summary}</strong></div>
              <div className="task-event-meta">
                {detail.phase ? <span>phase={String(detail.phase)}</span> : null}
                {detail.attempt ? <span>attempt={String(detail.attempt)}</span> : null}
                {detail.quota_used !== undefined ? <span>quota={String(detail.quota_used)}</span> : null}
                {detail.items !== undefined ? <span>items={String(detail.items)}</span> : null}
              </div>
              {detail.error ? <p className="task-event-error">{String(detail.error)}</p> : null}
            </div>
          </article>
        );
      })}
    </div>
  );
}

function LogsTable({ token, logs, setLogs, toast }: { token: string; logs: SystemLog[]; setLogs: React.Dispatch<React.SetStateAction<SystemLog[]>>; toast: (type: Toast["type"], message: string) => void }) {
  const [type, setType] = useState("");
  const [query, setQuery] = useState("");
  const [logStatus, setLogStatus] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [actorID, setActorID] = useState("");
  const [subjectID, setSubjectID] = useState("");
  const [taskID, setTaskID] = useState("");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [detailLogID, setDetailLogID] = useState<string | null>(null);
  const [loadingDetail, setLoadingDetail] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [total, setTotal] = useState(0);
  const tableWrapRef = useRef<HTMLDivElement | null>(null);
  useHorizontalWheelScroll(tableWrapRef);
  const rows = logs;
  const selectedSet = new Set(selected);
  const allVisibleSelected = rows.length > 0 && rows.every((log) => selectedSet.has(log.id));
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const logQueryParams = { page, pageSize, query, status: logStatus, endpoint, actorID, subjectID, taskID, dateFrom, dateTo };
  useEffect(() => setPage(1), [type, query, logStatus, endpoint, actorID, subjectID, taskID, dateFrom, dateTo, pageSize]);
  useEffect(() => {
    let cancelled = false;
    api.logs(token, type, [], logQueryParams).then((data) => {
      if (cancelled) return;
      setLogs(data.items || []);
      setTotal(Number(data.total || 0));
      setSelected((prev) => prev.filter((id) => (data.items || []).some((item) => item.id === id)));
    }).catch((error) => toast("error", error instanceof Error ? error.message : "加载日志失败"));
    return () => {
      cancelled = true;
    };
  }, [token, type, page, pageSize, query, logStatus, endpoint, actorID, subjectID, taskID, dateFrom, dateTo]);
  const detailLog = detailLogID ? rows.find((log) => log.id === detailLogID) || null : null;
  function toggleVisible(checked: boolean) {
    const visibleIDs = rows.map((log) => log.id);
    setSelected((prev) => checked ? Array.from(new Set([...prev, ...visibleIDs])) : prev.filter((id) => !visibleIDs.includes(id)));
  }
  async function load() {
    const data = await api.logs(token, type, [], logQueryParams);
    setLogs(data.items || []);
    setTotal(Number(data.total || 0));
  }
  async function ensureLogDetail(log: SystemLog) {
    if (Object.keys(logDetail(log)).length > 0) return log;
    const data = await api.logs(token, "", [log.id]);
    const item = (data.items || [])[0];
    if (!item) {
      throw new Error("日志详情不存在");
    }
    setLogs((current) => mergeLogs(current, [item]));
    return item;
  }
  async function openDetail(log: SystemLog) {
    setDetailLogID(log.id);
    if (Object.keys(logDetail(log)).length > 0) return;
    setLoadingDetail(log.id);
    try {
      await ensureLogDetail(log);
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
      <div className="filters filters-card activity-filters">
        <SearchControl value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索日志内容、接口、模型、用户或任务" />
        <ControlField label="类型"><select value={type} onChange={(event) => setType(event.target.value)}><option value="">全部类型</option><option value="call">调用</option><option value="task">任务</option><option value="image">图片</option><option value="account">账号</option><option value="user">用户</option><option value="settings">设置</option><option value="mail">邮件</option><option value="register">注册</option><option value="backup">备份</option></select></ControlField>
        <ControlField label="状态"><select value={logStatus} onChange={(event) => setLogStatus(event.target.value)}><option value="">全部状态</option><option value="success">success</option><option value="failed">failed</option><option value="error">error</option><option value="partial_failed">partial_failed</option></select></ControlField>
        <ControlField label="端点"><input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} placeholder="/v1/images/generations" /></ControlField>
        <ControlField label="任务 ID"><input value={taskID} onChange={(event) => setTaskID(event.target.value)} placeholder="task id" /></ControlField>
        <ControlField label="用户 ID"><input value={subjectID} onChange={(event) => setSubjectID(event.target.value)} placeholder="subject / owner" /></ControlField>
        <ControlField label="操作者"><input value={actorID} onChange={(event) => setActorID(event.target.value)} placeholder="actor id" /></ControlField>
        <ControlField label="开始"><input type="date" value={dateFrom} onChange={(event) => setDateFrom(event.target.value)} /></ControlField>
        <ControlField label="结束"><input type="date" value={dateTo} onChange={(event) => setDateTo(event.target.value)} /></ControlField>
        <ControlField label="每页"><select value={pageSize} onChange={(event) => setPageSize(Number(event.target.value))}><option>10</option><option>25</option><option>50</option><option>100</option></select></ControlField>
        <div className="filter-actions"><button className="secondary" onClick={() => load().catch((error) => toast("error", error.message))}>刷新日志</button><button className="ghost small" onClick={() => { setQuery(""); setType(""); setLogStatus(""); setEndpoint(""); setActorID(""); setSubjectID(""); setTaskID(""); setDateFrom(""); setDateTo(""); }}>重置</button><button className="danger" disabled={!selected.length} onClick={() => clear().catch((error) => toast("error", error.message))}>清理选中</button></div>
      </div>
      <ScrollableTable tableRef={tableWrapRef} className="data-table-wrap" height="medium"><table className="activity-table log-table"><thead><tr><th><input type="checkbox" checked={allVisibleSelected} onChange={(event) => toggleVisible(event.target.checked)} aria-label="选择当前日志" /></th><th>Time</th><th>Type</th><th>Status</th><th>Endpoint</th><th>Subject</th><th>Actor</th><th>Task</th><th>耗时</th><th>Summary</th><th></th></tr></thead><tbody>{rows.map((log) => {
        const detail = logDetail(log);
        return (
          <tr key={log.id}>
            <td><input type="checkbox" checked={selectedSet.has(log.id)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, log.id] : prev.filter((id) => id !== log.id))} /></td>
            <td>{fmtDate(log.time)}</td>
            <td>{log.type}</td>
            <td>{log.status || detail.status ? <Badge value={String(log.status || detail.status)} /> : "-"}</td>
            <td>{String(log.endpoint || detail.endpoint || log.summary || "-")}</td>
            <td>{String(log.subject_id || detail.subject_id || detail.owner_id || detail.user_id || "-")}</td>
            <td>{String(log.actor_id || detail.actor_id || "-")}</td>
            <td>{String(log.task_id || detail.task_id || "-")}</td>
            <td>{formatDuration(detail.duration_ms)}</td>
            <td>{log.summary}</td>
            <td><button className="ghost small" onClick={() => openDetail(log)}>{loadingDetail === log.id ? "加载" : "详情"}</button></td>
          </tr>
        );
      })}</tbody></table></ScrollableTable>
      <div className="pager"><button className="ghost small" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>上一页</button><span>{page} / {pageCount} · {total} 项</span><button className="ghost small" disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))}>下一页</button></div>
      <DetailModal title={detailLog ? `日志详情 · ${detailLog.id}` : "日志详情"} open={Boolean(detailLogID)} onClose={() => setDetailLogID(null)}>
        {loadingDetail === detailLogID || !detailLog ? <div className="detail-panel detail-panel-plain">加载详情中...</div> : <LogDetail log={detailLog} />}
      </DetailModal>
    </>
  );
}

function DetailModal({ title, open, onClose, children }: { title: string; open: boolean; onClose: () => void; children: React.ReactNode }) {
  if (!open) return null;
  return (
    <div className="detail-modal-overlay" onClick={onClose}>
      <div className="detail-modal" onClick={(event) => event.stopPropagation()}>
        <div className="detail-modal-header">
          <strong>{title}</strong>
          <button className="detail-modal-close" onClick={onClose}><X size={18} /></button>
        </div>
        <div className="detail-modal-body">{children}</div>
      </div>
    </div>
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
        <DetailItem label="接口" value={String(log.endpoint || detail.endpoint || log.summary)} />
        <DetailItem label="模型" value={String(detail.model || "-")} />
        <DetailItem label="状态" value={String(log.status || detail.status || "-")} />
        <DetailItem label="生成时间" value={formatDuration(detail.duration_ms)} />
        <DetailItem label="用户" value={String(detail.name || detail.subject_id || "-")} />
        <DetailItem label="用户 ID" value={String(log.subject_id || detail.subject_id || detail.owner_id || detail.user_id || "-")} />
        <DetailItem label="操作者" value={String(log.actor_id || detail.actor_id || "-")} />
        <DetailItem label="任务 ID" value={String(log.task_id || detail.task_id || "-")} code />
        <DetailItem label="请求张数" value={String(detail.requested_count ?? detail.n ?? "-")} />
        <DetailItem label="消耗额度" value={String(detail.quota_used ?? detail.quota_reserved ?? "-")} />
        <DetailItem label="剩余额度" value={String(detail.available_quota ?? "-")} />
      </div>
      {detail.error ? <p className="detail-error">{String(detail.error)}</p> : null}
      <pre className="detail-json">{safeJSON({ id: log.id, time: log.time, type: log.type, summary: log.summary, actor_id: log.actor_id, subject_id: log.subject_id, task_id: log.task_id, endpoint: log.endpoint, status: log.status, detail })}</pre>
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

function taskEventDetail(event: TaskEvent): Record<string, unknown> {
  if (!event.detail || typeof event.detail !== "object" || Array.isArray(event.detail)) return {};
  return event.detail as Record<string, unknown>;
}

function formatRegisterLogLine(log: SystemLog) {
  const detail = logDetail(log);
  const parts: string[] = [];
  const thread = detail.thread;
  if (typeof thread === "number" && Number.isFinite(thread)) {
    parts.push(`thread=${thread}`);
  } else if (typeof thread === "string" && thread.trim()) {
    parts.push(`thread=${thread.trim()}`);
  }
  const summary = String(log.summary || "").trim().replace(/^\[[^\]]+\]\s*/, "");
  if (summary) {
    parts.push(summary);
  }
  const fields = [
    ["email", detail.email],
    ["code", detail.code],
    ["reason", detail.reason],
    ["status", detail.status],
    ["mode", detail.mode],
    ["threads", detail.threads],
    ["total", detail.total],
    ["success", detail.success],
    ["fail", detail.fail],
    ["done", detail.done],
    ["running", detail.running],
    ["quota", detail.current_quota],
    ["available", detail.current_available],
    ["error", detail.error]
  ] as const;
  for (const [key, value] of fields) {
    if (value === null || value === undefined || value === "") continue;
    parts.push(`${key}=${String(value)}`);
  }
  return parts.join(" | ");
}

function formatRestoreCountdown(value?: string | null) {
  return formatRemainingTime(value);
}

function formatNextAutoRefresh(updatedAt?: string | null, intervalMinutes = 5) {
  return formatNextRefreshTime(updatedAt, intervalMinutes);
}

function formatNextAutoRefreshTitle(updatedAt?: string | null, intervalMinutes = 5) {
  if (!updatedAt) return "-";
  const date = new Date(updatedAt);
  if (Number.isNaN(date.getTime())) return "-";
  return fmtDate(new Date(date.getTime() + Math.max(1, intervalMinutes) * 60000).toISOString());
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

function formatRegisterSeconds(value: unknown) {
  const seconds = Number(value);
  if (!Number.isFinite(seconds) || seconds <= 0) return "-";
  return formatDuration(seconds * 1000);
}

function ImagesPanel({ token, images, setImages, toast, openLightbox }: { token: string; images: StoredImage[]; setImages: (items: StoredImage[]) => void; toast: (type: Toast["type"], message: string) => void; openLightbox: (src: string, title?: string) => void }) {
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState("new");
  const [dateScope, setDateScope] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [detailPath, setDetailPath] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(24);
  const [total, setTotal] = useState(0);
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  useEffect(() => setPage(1), [pageSize, query, sort, dateScope]);
  useEffect(() => {
    let cancelled = false;
    api.images(token, { page, pageSize, query, sort, dateScope }).then((data) => {
      if (cancelled) return;
      setImages(data.items || []);
      setTotal(Number(data.total || 0));
      setSelected((prev) => prev.filter((path) => (data.items || []).some((item) => item.path === path)));
    }).catch((error) => toast("error", error instanceof Error ? error.message : "加载图片失败"));
    return () => {
      cancelled = true;
    };
  }, [token, page, pageSize, query, sort, dateScope, setImages]);
  const items = images;
  const detailImage = detailPath ? items.find((item) => item.path === detailPath) || null : null;
  const allVisibleSelected = items.length > 0 && items.every((item) => selected.includes(item.path));
  const groupedItems = items.reduce<Array<{ date: string; items: StoredImage[] }>>((groups, item) => {
    const date = fmtDate(item.created_at).split(" ")[0] || "未知日期";
    const current = groups[groups.length - 1];
    if (current && current.date === date) {
      current.items.push(item);
      return groups;
    }
    groups.push({ date, items: [item] });
    return groups;
  }, []);
  async function reload(nextPage = page) {
    const data = await api.images(token, { page: nextPage, pageSize, query, sort, dateScope });
    const nextItems = data.items || [];
    const nextTotal = Number(data.total || 0);
    const nextPageCount = Math.max(1, Math.ceil(nextTotal / pageSize));
    if (nextPage > nextPageCount) {
      setPage(nextPageCount);
      return;
    }
    setImages(nextItems);
    setTotal(nextTotal);
    setSelected((prev) => prev.filter((path) => nextItems.some((item) => item.path === path)));
  }
  async function remove() {
    if (!selected.length || !confirm(`删除 ${selected.length} 张图片？`)) return;
    const data = await api.deleteImages(token, selected);
    setSelected([]);
    await reload(page);
    toast("success", `已删除 ${data.removed} 张图片`);
  }
  return (
    <section className="panel">
      <PanelHead title="图片库" subtitle="本地归档图片，支持预览、复制链接和批量删除" action={<><button className="secondary" onClick={() => reload().catch((error) => toast("error", error.message))}>刷新图片</button><button className="danger" disabled={!selected.length} onClick={remove}>删除选中</button></>} />
      <div className="filters images-filters filters-card">
        <SearchControl value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索文件名或路径" />
        <ControlField label="排序"><select value={sort} onChange={(event) => setSort(event.target.value)}><option value="new">最新优先</option><option value="old">最早优先</option><option value="large">文件最大</option></select></ControlField>
        <ControlField label="时间"><select value={dateScope} onChange={(event) => setDateScope(event.target.value)}><option value="">全部时间</option><option value="today">仅看今日</option><option value="7d">最近 7 天</option></select></ControlField>
        <ControlField label="每页"><select value={pageSize} onChange={(event) => setPageSize(Number(event.target.value))}><option>12</option><option>24</option><option>48</option><option>96</option></select></ControlField>
        <div className="filter-actions metrics-only"><span className="chip">当前页 {items.length} / 总计 {total}</span></div>
      </div>
      <div className="bulkbar">
        <label className="inline"><input type="checkbox" checked={allVisibleSelected} onChange={(event) => setSelected((prev) => event.target.checked ? Array.from(new Set([...prev, ...items.map((item) => item.path)])) : prev.filter((path) => !items.some((item) => item.path === path)))} /><span>选择当前页</span></label>
        <span>已选择 {selected.length} 张</span>
        <button className="ghost small" disabled={!selected.length} onClick={remove}>删除选中</button>
      </div>
      <div className="image-groups">{groupedItems.map((group) => <section key={group.date} className="image-group"><div className="image-group-head"><span>{group.date}</span><small>{group.items.length} 张</small></div><div className="image-grid">{group.items.map((item) => {
        const assetURL = storedImageURL(item, token);
        const prompt = item.display_prompt || item.prompt || item.revised_prompt || item.name;
        return <article key={item.path} className="image-item"><div className="image-item-head"><label><input type="checkbox" checked={selected.includes(item.path)} onChange={(event) => setSelected((prev) => event.target.checked ? [...prev, item.path] : prev.filter((path) => path !== item.path))} /><span title={prompt}>{prompt}</span></label><div className="image-item-actions"><IconButton title="复制路径" onClick={() => copyText(item.path).then(() => toast("success", "已复制图片路径"))}><Copy size={14} /></IconButton><button className="ghost small" onClick={() => setDetailPath(item.path)}>详情</button></div></div><button className="image-item-preview" onClick={() => openLightbox(assetURL, prompt)}><img src={assetURL} alt={prompt} loading="lazy" /></button></article>;
      })}</div></section>)}</div>
      <div className="pager"><button className="ghost small" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>上一页</button><span>{page} / {pageCount} · {total} 项</span><button className="ghost small" disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))}>下一页</button></div>
      <DetailModal title={detailImage ? `图片详情 · ${detailImage.display_prompt || detailImage.prompt || detailImage.revised_prompt || detailImage.name}` : "图片详情"} open={Boolean(detailPath)} onClose={() => setDetailPath(null)}>
        {detailImage ? <ImageDetail token={token} image={detailImage} openLightbox={openLightbox} /> : <div className="detail-panel detail-panel-plain">图片详情不存在</div>}
      </DetailModal>
    </section>
  );
}

function ImageDetail({ token, image, openLightbox }: { token: string; image: StoredImage; openLightbox: (src: string, title?: string) => void }) {
  const prompt = image.display_prompt || image.prompt || image.revised_prompt || image.name;
  const src = storedImageURL(image, token);
  return (
    <div className="detail-panel">
      <div className="detail-grid">
        <DetailItem label="提示词" value={prompt} />
        <DetailItem label="时间" value={fmtDate(image.created_at)} />
        <DetailItem label="大小" value={fmtBytes(image.size)} />
        <DetailItem label="路径" value={image.path} code />
      </div>
      <button className="detail-image-hero" onClick={() => openLightbox(src, prompt)}>
        <img src={src} alt={prompt} loading="lazy" />
      </button>
      {image.prompt ? <div className="detail-prompt"><span>原始提示词</span><p>{image.prompt}</p></div> : null}
      {image.revised_prompt && image.revised_prompt !== image.prompt ? <div className="detail-prompt"><span>修订提示词</span><p>{image.revised_prompt}</p></div> : null}
    </div>
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
        {images.length ? <div className="play-image-preview">{images.map((image, index) => { const src = imageSrc(image, token); return src ? <button key={index} onClick={() => openLightbox(src)}><img src={src} alt={`result ${index + 1}`} /></button> : null; })}</div> : null}
        <pre className="output">{output || "等待运行"}</pre>
      </section>
    </div>
  );
}

function UsersPanel({ token, users, setUsers, toast }: { token: string; users: User[]; setUsers: (items: User[]) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [form, setForm] = useState({ email: "", name: "", password: "", role: "user", quotaUnlimited: false, permanentQuota: "0", temporaryQuota: "" });
  const [newKey, setNewKey] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [editingUser, setEditingUser] = useState<User | null>(null);
  const [editForm, setEditForm] = useState({ email: "", name: "", password: "", role: "user", status: "active", quotaUnlimited: false, permanentQuota: "0", temporaryQuota: "" });
  const [editBusy, setEditBusy] = useState(false);
  const [batchTempOpen, setBatchTempOpen] = useState(false);
  const [batchTempValue, setBatchTempValue] = useState("10");
  const [batchPermanentOpen, setBatchPermanentOpen] = useState(false);
  const [batchPermanentValue, setBatchPermanentValue] = useState("10");
  const [selected, setSelected] = useState<string[]>([]);
  const [batchBusy, setBatchBusy] = useState("");
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [role, setRole] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [total, setTotal] = useState(0);
  const [defaultTemporaryQuota, setDefaultTemporaryQuota] = useState(10);
  const tableWrapRef = useRef<HTMLDivElement | null>(null);
  useHorizontalWheelScroll(tableWrapRef);
  const selectedSet = useMemo(() => new Set(selected), [selected]);
  const allVisibleSelected = users.length > 0 && users.every((item) => selectedSet.has(item.id));
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  useEffect(() => setPage(1), [query, status, role, pageSize]);
  useEffect(() => {
    let cancelled = false;
    Promise.all([
      api.users(token, { page, pageSize, query, status, role }),
      api.settings(token)
    ]).then(([data, settingsData]) => {
      if (cancelled) return;
      setUsers(data.items || []);
      setTotal(Number(data.total || 0));
      setSelected((prev) => prev.filter((id) => (data.items || []).some((item) => item.id === id)));
      setDefaultTemporaryQuota(Math.max(0, Number(settingsData.config?.default_new_user_temporary_quota || 0)));
    }).catch((error) => toast("error", error instanceof Error ? error.message : "加载用户失败"));
    return () => {
      cancelled = true;
    };
  }, [token, page, pageSize, query, status, role]);
  async function reload() {
    const data = await api.users(token, { page, pageSize, query, status, role });
    setUsers(data.items || []);
    setTotal(Number(data.total || 0));
    setSelected((prev) => prev.filter((id) => (data.items || []).some((item) => item.id === id)));
  }
  async function create() {
    const quotaUnlimited = form.role === "admin" ? true : form.quotaUnlimited;
    const temporaryQuotaText = form.temporaryQuota.trim();
    const temporaryQuotaValue = temporaryQuotaText === "" ? defaultTemporaryQuota : Math.max(0, Number(temporaryQuotaText) || 0);
    const temporaryQuota = form.role === "admin" || quotaUnlimited ? 0 : temporaryQuotaValue;
    const data = await api.createUser(token, {
      email: form.email,
      name: form.name,
      password: form.password,
      role: form.role,
      quota_unlimited: quotaUnlimited,
      permanent_quota: quotaUnlimited ? 0 : Math.max(0, Number(form.permanentQuota) || 0),
      temporary_quota: temporaryQuota,
      temporary_quota_date: temporaryQuota > 0 ? localDayString() : ""
    });
    setForm({ email: "", name: "", password: "", role: "user", quotaUnlimited: false, permanentQuota: "0", temporaryQuota: "" });
    setCreateOpen(false);
    if (data.key) setNewKey(data.key);
    await reload();
    toast("success", "用户已创建");
  }
  function openEditUser(user: User) {
    setEditingUser(user);
    setEditForm({
      email: user.email,
      name: user.name || "",
      password: "",
      role: user.role,
      status: user.status,
      quotaUnlimited: user.quota_unlimited,
      permanentQuota: String(user.permanent_quota || 0),
      temporaryQuota: String(user.daily_temporary_quota || 0)
    });
  }
  async function saveEditUser() {
    if (!editingUser) return;
    const quotaUnlimited = editForm.role === "admin" ? true : editForm.quotaUnlimited;
    const temporaryQuota = Math.max(0, Number(editForm.temporaryQuota) || 0);
    setEditBusy(true);
    try {
      const body: Partial<Pick<User, "email" | "name" | "role" | "status" | "quota_unlimited" | "permanent_quota" | "temporary_quota" | "temporary_quota_date" | "daily_temporary_quota">> & { password?: string } = {
        email: editForm.email.trim(),
        name: editForm.name.trim(),
        role: editForm.role as User["role"],
        status: editForm.status as User["status"],
        quota_unlimited: quotaUnlimited,
        permanent_quota: quotaUnlimited ? 0 : Math.max(0, Number(editForm.permanentQuota) || 0),
        temporary_quota: quotaUnlimited ? 0 : temporaryQuota,
        temporary_quota_date: temporaryQuota > 0 ? localDayString() : "",
        daily_temporary_quota: quotaUnlimited ? 0 : temporaryQuota
      };
      if (editForm.password.trim()) body.password = editForm.password.trim();
      await api.updateUser(token, editingUser.id, body);
      await reload();
      setEditingUser(null);
      toast("success", "用户已更新");
    } finally {
      setEditBusy(false);
    }
  }
  async function runBatch(action: "enable" | "disable" | "delete" | "grant_temporary_quota" | "grant_permanent_quota" | "set_temporary_quota", options?: { temporaryQuota?: number; permanentQuota?: number; successMessage?: string; confirmText?: string }) {
    if (!selected.length) return;
    if (options?.confirmText && !confirm(options.confirmText)) return;
    setBatchBusy(action);
    try {
      const result = await api.batchUsers(token, {
        ids: selected,
        action,
        temporary_quota: options?.temporaryQuota,
        permanent_quota: options?.permanentQuota
      });
      setSelected([]);
      await reload();
      toast("success", options?.successMessage || `已处理 ${result.updated} 个用户`);
    } finally {
      setBatchBusy("");
    }
  }
  return (
    <section className="panel">
      <PanelHead title="用户" subtitle="创建用户、编辑资料、删除账号，并维护每个用户唯一 API Key" action={<button onClick={() => { setForm({ email: "", name: "", password: "", role: "user", quotaUnlimited: false, permanentQuota: "0", temporaryQuota: "" }); setCreateOpen(true); }}>创建用户</button>} />
      <div className="toolbar user-toolbar form-toolbar">
        <SearchControl value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索邮箱或名称" />
        <div className="toolbar-actions"><span className="chip">{total} 用户</span></div>
      </div>
      <div className="filters filters-card activity-filters">
        <ControlField label="状态"><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">全部状态</option><option value="active">Active</option><option value="disabled">Disabled</option></select></ControlField>
        <ControlField label="角色"><select value={role} onChange={(event) => setRole(event.target.value)}><option value="">全部角色</option><option value="user">User</option><option value="admin">Admin</option></select></ControlField>
        <ControlField label="每页"><select value={pageSize} onChange={(event) => setPageSize(Number(event.target.value))}><option>10</option><option>25</option><option>50</option><option>100</option></select></ControlField>
        <div className="filter-actions"><button className="secondary" onClick={() => reload().catch((error) => toast("error", error.message))}>刷新用户</button></div>
      </div>
      <div className="bulkbar">
        <label className="inline"><input type="checkbox" checked={allVisibleSelected} onChange={(event) => {
          setSelected((prev) => event.target.checked ? Array.from(new Set([...prev, ...users.map((item) => item.id)])) : prev.filter((id) => !users.some((item) => item.id === id)));
        }} /><span>选择当前页</span></label>
        <span>已选择 {selected.length} 项</span>
        <button className="ghost small" disabled={!selected.length || batchBusy === "enable"} onClick={() => runBatch("enable", { successMessage: "已启用选中用户" }).catch((error) => toast("error", error.message))}>启用选中</button>
        <button className="ghost small" disabled={!selected.length || batchBusy === "disable"} onClick={() => runBatch("disable", { successMessage: "已停用选中用户" }).catch((error) => toast("error", error.message))}>停用选中</button>
        <button className="ghost small" disabled={!selected.length || batchBusy === "grant_permanent_quota"} onClick={() => { setBatchPermanentValue("10"); setBatchPermanentOpen(true); }}>发永久额度</button>
        <button className="ghost small" disabled={!selected.length || batchBusy === "set_temporary_quota"} onClick={() => { setBatchTempValue("10"); setBatchTempOpen(true); }}>设当天额度</button>
        <button className="danger small" disabled={!selected.length || batchBusy === "delete"} onClick={() => runBatch("delete", { successMessage: "已删除选中用户", confirmText: `删除 ${selected.length} 个用户？` }).catch((error) => toast("error", error.message))}>删除选中</button>
      </div>
      {newKey ? <div className="notice"><span>新 API Key 只显示一次：</span><code>{newKey}</code><IconButton title="复制" onClick={() => copyText(newKey).then(() => toast("success", "已复制"))}><Copy size={14} /></IconButton><IconButton title="隐藏" onClick={() => setNewKey("")}><EyeOff size={14} /></IconButton></div> : null}
      <ScrollableTable tableRef={tableWrapRef} className="data-table-wrap" height="large"><table className="users-table"><thead><tr><th><input type="checkbox" checked={allVisibleSelected} onChange={(event) => {
        setSelected((prev) => event.target.checked ? Array.from(new Set([...prev, ...users.map((item) => item.id)])) : prev.filter((id) => !users.some((item) => item.id === id)));
      }} aria-label="选择当前用户" /></th><th>Email</th><th>Name</th><th>Role</th><th>Status</th><th>可用额度</th><th>额度明细</th><th>消耗统计</th><th>API Key</th><th>Last Login</th><th></th></tr></thead><tbody>{users.map((user) => <UserRow key={user.id} token={token} user={user} reload={reload} toast={toast} showKey={(key) => setNewKey(key)} selected={selectedSet.has(user.id)} onSelect={(checked) => setSelected((prev) => checked ? [...prev, user.id] : prev.filter((id) => id !== user.id))} onEdit={openEditUser} />)}</tbody></table></ScrollableTable>
      <div className="pager"><button className="ghost small" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>上一页</button><span>{page} / {pageCount} · {total} 项</span><button className="ghost small" disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))}>下一页</button></div>
      <DetailModal title="创建用户" open={createOpen} onClose={() => setCreateOpen(false)}>
        <div className="detail-panel">
          <div className="detail-grid detail-grid-two">
            <ControlField label="邮箱"><input value={form.email} onChange={(event) => setForm({ ...form, email: event.target.value })} placeholder="email" /></ControlField>
            <ControlField label="名称"><input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="name" /></ControlField>
            <ControlField label="密码"><input value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} type="password" placeholder="password" /></ControlField>
            <ControlField label="角色"><select value={form.role} onChange={(event) => setForm({ ...form, role: event.target.value })}><option>user</option><option>admin</option></select></ControlField>
            <ControlField label="无限额度"><select value={form.role === "admin" || form.quotaUnlimited ? "true" : "false"} onChange={(event) => setForm({ ...form, quotaUnlimited: event.target.value === "true" })} disabled={form.role === "admin"}><option value="false">有限额度</option><option value="true">无限额度</option></select></ControlField>
            <ControlField label="永久额度"><input type="number" min={0} value={form.permanentQuota} onChange={(event) => setForm({ ...form, permanentQuota: event.target.value })} disabled={form.role === "admin" || form.quotaUnlimited} /></ControlField>
            <ControlField label="当天额度"><input type="number" min={0} value={form.temporaryQuota} onChange={(event) => setForm({ ...form, temporaryQuota: event.target.value })} disabled={form.role === "admin" || form.quotaUnlimited} placeholder={`默认 ${defaultTemporaryQuota}`} /></ControlField>
          </div>
          <div className="modal-actions"><button className="secondary" onClick={() => setCreateOpen(false)}>取消</button><button onClick={() => create().catch((error) => toast("error", error.message))}>创建用户</button></div>
        </div>
      </DetailModal>
      <DetailModal title={editingUser ? `编辑用户 · ${editingUser.email}` : "编辑用户"} open={Boolean(editingUser)} onClose={() => !editBusy && setEditingUser(null)}>
        <div className="detail-panel">
          <div className="detail-grid detail-grid-two">
            <ControlField label="邮箱"><input value={editForm.email} onChange={(event) => setEditForm({ ...editForm, email: event.target.value })} placeholder="email" /></ControlField>
            <ControlField label="名称"><input value={editForm.name} onChange={(event) => setEditForm({ ...editForm, name: event.target.value })} placeholder="name" /></ControlField>
            <ControlField label="新密码"><input value={editForm.password} onChange={(event) => setEditForm({ ...editForm, password: event.target.value })} type="password" placeholder="留空则不修改" /></ControlField>
            <ControlField label="角色"><select value={editForm.role} onChange={(event) => setEditForm({ ...editForm, role: event.target.value })}><option>user</option><option>admin</option></select></ControlField>
            <ControlField label="状态"><select value={editForm.status} onChange={(event) => setEditForm({ ...editForm, status: event.target.value })}><option value="active">Active</option><option value="disabled">Disabled</option></select></ControlField>
            <ControlField label="无限额度"><select value={editForm.role === "admin" || editForm.quotaUnlimited ? "true" : "false"} onChange={(event) => setEditForm({ ...editForm, quotaUnlimited: event.target.value === "true" })} disabled={editForm.role === "admin"}><option value="false">有限额度</option><option value="true">无限额度</option></select></ControlField>
            <ControlField label="永久额度"><input type="number" min={0} value={editForm.permanentQuota} onChange={(event) => setEditForm({ ...editForm, permanentQuota: event.target.value })} disabled={editForm.role === "admin" || editForm.quotaUnlimited} /></ControlField>
            <ControlField label="当天额度"><input type="number" min={0} value={editForm.temporaryQuota} onChange={(event) => setEditForm({ ...editForm, temporaryQuota: event.target.value })} disabled={editForm.role === "admin" || editForm.quotaUnlimited} /></ControlField>
          </div>
          <div className="modal-actions"><button className="secondary" disabled={editBusy} onClick={() => setEditingUser(null)}>取消</button><button disabled={editBusy} onClick={() => saveEditUser().catch((error) => toast("error", error.message))}>{editBusy ? "保存中" : "保存"}</button></div>
        </div>
      </DetailModal>
      <DetailModal title="批量发放永久额度" open={batchPermanentOpen} onClose={() => setBatchPermanentOpen(false)}>
        <div className="detail-panel">
          <div className="detail-grid detail-grid-two">
            <ControlField label="追加数量"><input type="number" min={0} value={batchPermanentValue} onChange={(event) => setBatchPermanentValue(event.target.value)} /></ControlField>
          </div>
          <div className="modal-actions"><button className="secondary" onClick={() => setBatchPermanentOpen(false)}>取消</button><button disabled={batchBusy === "grant_permanent_quota"} onClick={() => {
            const value = Math.max(0, Number(batchPermanentValue) || 0);
            runBatch("grant_permanent_quota", { permanentQuota: value, successMessage: value > 0 ? `已给 ${selected.length} 个用户追加永久额度` : "永久额度追加为 0，未发生变化" })
              .then(() => setBatchPermanentOpen(false))
              .catch((error) => toast("error", error.message));
          }}>{batchBusy === "grant_permanent_quota" ? "发放中" : "确认发放"}</button></div>
        </div>
      </DetailModal>
      <DetailModal title="批量设置当天额度" open={batchTempOpen} onClose={() => setBatchTempOpen(false)}>
        <div className="detail-panel">
          <div className="detail-grid detail-grid-two">
            <ControlField label="数量"><input type="number" min={0} value={batchTempValue} onChange={(event) => setBatchTempValue(event.target.value)} /></ControlField>
          </div>
          <div className="modal-actions"><button className="secondary" onClick={() => setBatchTempOpen(false)}>取消</button><button disabled={batchBusy === "set_temporary_quota"} onClick={() => {
            const value = Math.max(0, Number(batchTempValue) || 0);
            runBatch("set_temporary_quota", { temporaryQuota: value, successMessage: value > 0 ? `已为 ${selected.length} 个用户设置当天额度` : "已清空选中用户当天额度" })
              .then(() => setBatchTempOpen(false))
              .catch((error) => toast("error", error.message));
          }}>{batchBusy === "set_temporary_quota" ? "保存中" : "确认保存"}</button></div>
        </div>
      </DetailModal>
    </section>
  );
}

function UserRow({ token, user, reload, toast, showKey, selected, onSelect, onEdit }: { token: string; user: User; reload: () => Promise<void>; toast: (type: Toast["type"], message: string) => void; showKey: (key: string) => void; selected: boolean; onSelect: (checked: boolean) => void; onEdit: (user: User) => void }) {
  const [grantingTemp, setGrantingTemp] = useState(false);
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
  async function grantTemporaryQuota() {
    const input = window.prompt(`给 ${user.email} 发放今日额度`, "10");
    if (input === null) return;
    const value = Math.max(0, Number(input) || 0);
    setGrantingTemp(true);
    try {
      await api.updateUser(token, user.id, {
        temporary_quota: value,
        temporary_quota_date: value > 0 ? localDayString() : ""
      });
      await reload();
      toast("success", value > 0 ? `已发放今日额度 ${value}` : "已清空今日额度");
    } finally {
      setGrantingTemp(false);
    }
  }
  return (
    <tr>
      <td><input type="checkbox" checked={selected} onChange={(event) => onSelect(event.target.checked)} /></td>
      <td>{user.email}</td>
      <td>{user.name || "-"}</td>
      <td><Badge value={user.role} /></td>
      <td><Badge value={user.status} /></td>
      <td>{user.quota_unlimited ? "∞" : compact(user.available_quota || 0)}</td>
      <td><><strong>{formatUserQuotaBreakdown(user)}</strong><small>{user.daily_temporary_quota ? `每日当天额度 ${user.daily_temporary_quota}` : (user.temporary_quota > 0 ? "今日已发放" : "无当天额度")}</small></></td>
      <td><><strong>{formatUserQuotaUsage(user)}</strong><small>{user.quota_used_date?.trim() || "今日"}</small></></td>
      <td><Badge value={user.api_key?.enabled ?? false} /><small>{user.api_key ? `${user.api_key.name} · ${fmtDate(user.api_key.last_used_at)}` : "Missing"}</small></td>
      <td>{fmtDate(user.last_login_at)}</td>
      <td className="users-actions-cell"><div className="row-actions">
        <>
          <button className="ghost small" onClick={() => onEdit(user)}>编辑</button>
          <button className="ghost small" disabled={grantingTemp} onClick={() => grantTemporaryQuota().catch((error) => toast("error", error.message))}>{grantingTemp ? "发放中" : "发今日额度"}</button>
          <IconButton title={user.status === "active" ? "停用" : "启用"} onClick={() => toggleStatus().catch((error) => toast("error", error.message))}>{user.status === "active" ? <Ban size={15} /> : <Eye size={15} />}</IconButton>
          <IconButton title="重置 API Key" onClick={() => resetKey().catch((error) => toast("error", error.message))}><RotateCcw size={15} /></IconButton>
          <IconButton title="删除" className="danger-icon" onClick={() => remove().catch((error) => toast("error", error.message))}><Trash2 size={15} /></IconButton>
        </>
      </div></td>
    </tr>
  );
}

function SettingsPanel({ token, settings, setSettings, toast }: { token: string; settings: SettingsType; setSettings: (settings: SettingsType) => void; toast: (type: Toast["type"], message: string) => void }) {
  const [json, setJson] = useState(safeJSON(settings));
  const [backupState, setBackupState] = useState<BackupState | null>(null);
  const [backupItems, setBackupItems] = useState<BackupRemoteItem[]>([]);
  const [backupBusy, setBackupBusy] = useState<"" | "run" | "reload" | "list">("");
  const [deletingBackupKey, setDeletingBackupKey] = useState("");
  const [smtpTestTo, setSMTPTestTo] = useState("");
  const [smtpTestBusy, setSMTPTestBusy] = useState(false);
  const backupTableRef = useRef<HTMLDivElement | null>(null);
  useHorizontalWheelScroll(backupTableRef);
  useEffect(() => setJson(safeJSON(settings)), [settings]);
  const aiReview = settings.ai_review && typeof settings.ai_review === "object" ? settings.ai_review as Record<string, unknown> : {};
  const allowedPublicModels = Array.isArray(settings.allowed_public_models) ? settings.allowed_public_models.map((item) => String(item)) : [];
  const backup = settings.backup && typeof settings.backup === "object" ? settings.backup as Record<string, unknown> : {};
  const smtpMail = settings.smtp_mail && typeof settings.smtp_mail === "object" ? settings.smtp_mail as Record<string, unknown> : {};
  function updateField(key: string, value: unknown) { setSettings({ ...settings, [key]: value }); }
  function updateBackupField(key: string, value: unknown) { updateField("backup", { ...backup, [key]: value }); }
  function updateSMTPField(key: string, value: unknown) { updateField("smtp_mail", { ...smtpMail, [key]: value }); }
  async function save(next = settings) { const data = await api.saveSettings(token, next); setSettings(data.config || {}); toast("success", "设置已保存"); }
  useEffect(() => {
    let cancelled = false;
    api.backupState(token).then((data) => {
      if (!cancelled) setBackupState(data.state || null);
    }).catch(() => {});
    api.listBackups(token).then((data) => {
      if (!cancelled) setBackupItems(data.items || []);
    }).catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [token]);
  async function reloadBackupState() {
    setBackupBusy("reload");
    try {
      const data = await api.backupState(token);
      setBackupState(data.state || null);
    } finally {
      setBackupBusy("");
    }
  }
  async function reloadBackups() {
    setBackupBusy("list");
    try {
      const data = await api.listBackups(token);
      setBackupItems(data.items || []);
    } finally {
      setBackupBusy("");
    }
  }
  async function runBackupNow() {
    setBackupBusy("run");
    try {
      const data = await api.runBackup(token);
      setBackupState(data.state || null);
      const listed = await api.listBackups(token);
      setBackupItems(listed.items || []);
      toast("success", data.artifact?.key ? `备份已完成：${data.artifact.key}` : "备份已完成");
    } finally {
      setBackupBusy("");
    }
  }
  async function removeBackup(key: string) {
    if (!key || !confirm(`删除远端备份？\n${key}`)) return;
    setDeletingBackupKey(key);
    try {
      await api.deleteBackup(token, key);
      setBackupItems((current) => current.filter((item) => item.key !== key));
      toast("success", "远端备份已删除");
    } finally {
      setDeletingBackupKey("");
    }
  }
  async function downloadBackup(key: string) {
    if (!key) return;
    setDeletingBackupKey(`download:${key}`);
    try {
      const res = await fetch(`/api/backup/download?key=${encodeURIComponent(key)}`, {
        headers: authHeaders(token),
        credentials: "same-origin"
      });
      if (!res.ok) {
        const text = await res.text();
        let message = "下载备份失败";
        if (text) {
          try {
            const data = JSON.parse(text) as { error?: { message?: string } };
            message = data.error?.message || message;
          } catch {
            message = text;
          }
        }
        throw new Error(message);
      }
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      const match = /filename="?([^"]+)"?/.exec(res.headers.get("Content-Disposition") || "");
      link.href = url;
      link.download = match?.[1] || backupDisplayName(key);
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
    } finally {
      setDeletingBackupKey("");
    }
  }
  function backupDisplayName(key: string) {
    const raw = String(key || "").split("/").pop() || key || "-";
    return raw.replace(/\.enc$/i, "");
  }
  function backupPrefixLabel(key: string) {
    const value = String(key || "");
    const index = value.lastIndexOf("/");
    return index > 0 ? value.slice(0, index) : "root";
  }
  async function sendSMTPTest() {
    const to = smtpTestTo.trim();
    if (!to) {
      toast("error", "请先填写测试收件邮箱");
      return;
    }
    setSMTPTestBusy(true);
    try {
      await api.testSMTPMail(token, to);
      toast("success", `测试邮件已发送到 ${to}`);
    } finally {
      setSMTPTestBusy(false);
    }
  }

  return (
    <div className="settings-layout">
      <section className="panel">
        <PanelHead
          title="常用设置"
          subtitle="保存后会同步写入配置表"
          action={<button onClick={() => save().catch((error) => toast("error", error.message))}>保存配置</button>}
        />
        <div className="settings-form">
          <label><span>Proxy</span><input value={String(settings.proxy || "")} onChange={(event) => updateField("proxy", event.target.value)} /></label>
          <label><span>Base URL</span><input value={String(settings.base_url || "")} onChange={(event) => updateField("base_url", event.target.value)} /></label>
          <label><span>图片工作台固定模型</span><input value={String(settings.image_workbench_model || "gpt-image-2")} onChange={(event) => updateField("image_workbench_model", event.target.value)} placeholder="gpt-image-2" /></label>
          <label><span>图片单次张数上限</span><input type="number" min={1} value={Number(settings.image_max_count || 4)} onChange={(event) => updateField("image_max_count", Number(event.target.value))} /></label>
          <label className="wide"><span>公开允许模型，每行一个</span><textarea value={allowedPublicModels.join("\n")} onChange={(event) => updateField("allowed_public_models", event.target.value.split("\n").map((line) => line.trim()).filter(Boolean))} placeholder={"gpt-image-2\ngpt-5\nauto"} /></label>
          <label><span>账号自动刷新间隔（分钟）</span><input type="number" min={1} value={Number(settings.refresh_account_interval_minute || 5)} onChange={(event) => updateField("refresh_account_interval_minute", Number(event.target.value))} /></label>
          <label><span>账号刷新并发（自动刷新/手动刷新共用）</span><input type="number" min={1} value={Number(settings.refresh_account_concurrency || 4)} onChange={(event) => updateField("refresh_account_concurrency", Number(event.target.value))} /></label>
          <label><span>正常账号轮转批大小</span><input type="number" min={1} value={Number(settings.refresh_account_normal_batch_size || 8)} onChange={(event) => updateField("refresh_account_normal_batch_size", Number(event.target.value))} /></label>
          <label><span>单账号默认图片并发</span><input type="number" min={1} value={Number(settings.image_account_concurrency || 1)} onChange={(event) => updateField("image_account_concurrency", Number(event.target.value))} /></label>
          <label><span>图片保留天数</span><input type="number" value={Number(settings.image_retention_days || 30)} onChange={(event) => updateField("image_retention_days", Number(event.target.value))} /></label>
          <label><span>图片轮询超时</span><input type="number" value={Number(settings.image_poll_timeout_secs || 120)} onChange={(event) => updateField("image_poll_timeout_secs", Number(event.target.value))} /></label>
          <label><span>新用户默认临时额度</span><input type="number" min={0} value={Number(settings.default_new_user_temporary_quota || 0)} onChange={(event) => updateField("default_new_user_temporary_quota", Math.max(0, Number(event.target.value) || 0))} /></label>
          <label className="inline"><input type="checkbox" checked={Boolean(settings.public_registration_enabled)} onChange={(event) => updateField("public_registration_enabled", event.target.checked)} /><span>启用公开注册</span></label>
          <label><span>注册验证码冷却（秒）</span><input type="number" min={1} value={Number(settings.register_code_cooldown_seconds || 60)} onChange={(event) => updateField("register_code_cooldown_seconds", Math.max(1, Number(event.target.value) || 60))} /></label>
          <label><span>普通用户上限</span><input type="number" min={0} value={Number(settings.register_max_ordinary_users || 0)} onChange={(event) => updateField("register_max_ordinary_users", Math.max(0, Number(event.target.value) || 0))} placeholder="0 表示不限" /></label>
          <label className="wide"><span>可注册邮箱后缀，每行一个</span><textarea value={Array.isArray(settings.register_allowed_email_domains) ? settings.register_allowed_email_domains.join("\n") : ""} onChange={(event) => updateField("register_allowed_email_domains", event.target.value.split("\n").map((line) => line.trim().replace(/^@+/, "")).filter(Boolean))} placeholder={"gmail.com\nzju.edu.cn"} /></label>
          <label className="inline"><input type="checkbox" checked={Boolean(settings.auto_remove_invalid_accounts)} onChange={(event) => updateField("auto_remove_invalid_accounts", event.target.checked)} /><span>自动移除异常账号</span></label>
          <label className="inline"><input type="checkbox" checked={Boolean(aiReview.enabled)} onChange={(event) => updateField("ai_review", { ...aiReview, enabled: event.target.checked })} /><span>启用 AI 内容审核</span></label>
          <label className="wide"><span>敏感词，每行一个</span><textarea value={Array.isArray(settings.sensitive_words) ? settings.sensitive_words.join("\n") : ""} onChange={(event) => updateField("sensitive_words", event.target.value.split("\n").map((line) => line.trim()).filter(Boolean))} /></label>
        </div>
      </section>

      <section className="panel">
        <PanelHead
          title="SMTP 邮件"
          subtitle="用于后续注册验证码发送，支持测试发送"
          action={
            <div className="actions">
              <button onClick={() => save().catch((error) => toast("error", error.message))}>保存配置</button>
              <button className="secondary" disabled={smtpTestBusy} onClick={() => sendSMTPTest().catch((error) => toast("error", error.message))}>
                {smtpTestBusy ? "发送中" : "发送测试"}
              </button>
            </div>
          }
        />
        <div className="settings-form">
          <label className="inline"><input type="checkbox" checked={Boolean(smtpMail.enabled)} onChange={(event) => updateSMTPField("enabled", event.target.checked)} /><span>启用 SMTP</span></label>
          <label className="inline">
            <input
              type="checkbox"
              checked={Boolean(smtpMail.starttls ?? true)}
              disabled={Boolean(smtpMail.implicit_tls)}
              onChange={(event) => updateSMTPField("starttls", event.target.checked)}
            />
            <span>启用 STARTTLS</span>
          </label>
          <label className="inline">
            <input
              type="checkbox"
              checked={Boolean(smtpMail.implicit_tls)}
              onChange={(event) => {
                const checked = event.target.checked;
                updateField("smtp_mail", { ...smtpMail, implicit_tls: checked, starttls: checked ? false : smtpMail.starttls ?? true });
              }}
            />
            <span>启用隐式 TLS / SSL（常用于 465）</span>
          </label>
          <label><span>SMTP Host</span><input value={String(smtpMail.host || "")} onChange={(event) => updateSMTPField("host", event.target.value)} placeholder="smtp.example.com" /></label>
          <label><span>SMTP Port</span><input type="number" min={1} max={65535} value={Number(smtpMail.port ?? 587)} onChange={(event) => updateSMTPField("port", Number(event.target.value))} /></label>
          <label><span>用户名</span><input value={String(smtpMail.username || "")} onChange={(event) => updateSMTPField("username", event.target.value)} placeholder="user@example.com" /></label>
          <label><span>密码 / 授权码</span><input type="password" value={String(smtpMail.password || "")} onChange={(event) => updateSMTPField("password", event.target.value)} placeholder="SMTP password or app password" /></label>
          <label><span>发件邮箱</span><input value={String(smtpMail.from_address || "")} onChange={(event) => updateSMTPField("from_address", event.target.value)} placeholder="no-reply@example.com" /></label>
          <label><span>发件人名称</span><input value={String(smtpMail.from_name || "")} onChange={(event) => updateSMTPField("from_name", event.target.value)} placeholder="GPT Image Web" /></label>
          <label><span>Reply-To</span><input value={String(smtpMail.reply_to || "")} onChange={(event) => updateSMTPField("reply_to", event.target.value)} placeholder="support@example.com" /></label>
          <label><span>测试收件邮箱</span><input value={smtpTestTo} onChange={(event) => setSMTPTestTo(event.target.value)} placeholder="you@example.com" /></label>
        </div>
        <div className="detail-panel detail-panel-plain">
          <p className="backup-note">测试发送会读取当前已保存的 SMTP 配置，并发送一封纯文本测试邮件。常见组合是 `587 + STARTTLS` 或 `465 + 隐式 TLS / SSL`。</p>
        </div>
      </section>

      <section className="panel">
        <PanelHead
          title="备份"
          subtitle="按间隔自动备份数据库和关键配置并上传到 Cloudflare R2"
          action={
            <div className="actions">
              <button onClick={() => save().catch((error) => toast("error", error.message))}>保存配置</button>
              <button className="secondary" disabled={backupBusy === "reload" || backupBusy === "list"} onClick={() => reloadBackupState().catch((error) => toast("error", error.message))}>{backupBusy === "reload" ? "刷新中" : "刷新状态"}</button>
              <button className="secondary" disabled={backupBusy === "reload" || backupBusy === "list"} onClick={() => reloadBackups().catch((error) => toast("error", error.message))}>{backupBusy === "list" ? "列表刷新中" : "刷新列表"}</button>
              <button disabled={backupBusy === "run"} onClick={() => runBackupNow().catch((error) => toast("error", error.message))}>{backupBusy === "run" ? "备份中" : "立即备份"}</button>
            </div>
          }
        />
        <div className="settings-form">
          <label className="inline"><input type="checkbox" checked={Boolean(backup.enabled)} onChange={(event) => updateBackupField("enabled", event.target.checked)} /><span>启用自动备份</span></label>
          <label className="inline"><input type="checkbox" checked={Boolean(backup.encrypt ?? true)} onChange={(event) => updateBackupField("encrypt", event.target.checked)} /><span>启用加密</span></label>
          <label><span>备份间隔小时</span><input type="number" min={0} max={720} value={Number(backup.schedule_hour ?? 24)} onChange={(event) => updateBackupField("schedule_hour", Number(event.target.value))} /></label>
          <label><span>备份间隔分钟</span><input type="number" min={0} max={59} value={Number(backup.schedule_minute ?? 0)} onChange={(event) => updateBackupField("schedule_minute", Number(event.target.value))} /></label>
          <label><span>轮替保留份数</span><input type="number" min={1} value={Number(backup.keep_latest ?? 7)} onChange={(event) => updateBackupField("keep_latest", Number(event.target.value))} /></label>
          <label><span>R2 Prefix</span><input value={String(backup.r2_prefix || "gpt-image-web")} onChange={(event) => updateBackupField("r2_prefix", event.target.value)} placeholder="gpt-image-web" /></label>
          <label><span>R2 Account ID</span><input value={String(backup.r2_account_id || "")} onChange={(event) => updateBackupField("r2_account_id", event.target.value)} /></label>
          <label><span>R2 Access Key ID</span><input value={String(backup.r2_access_key_id || "")} onChange={(event) => updateBackupField("r2_access_key_id", event.target.value)} /></label>
          <label><span>R2 Secret Access Key</span><input type="password" value={String(backup.r2_secret_access_key || "")} onChange={(event) => updateBackupField("r2_secret_access_key", event.target.value)} /></label>
          <label><span>R2 Bucket</span><input value={String(backup.r2_bucket || "")} onChange={(event) => updateBackupField("r2_bucket", event.target.value)} /></label>
          <label className="wide"><span>备份加密口令</span><input type="password" value={String(backup.passphrase || "")} onChange={(event) => updateBackupField("passphrase", event.target.value)} placeholder="用于 AES-256-GCM 加密备份包" /></label>
          <label className="inline"><input type="checkbox" checked={Boolean(backup.include_env ?? true)} onChange={(event) => updateBackupField("include_env", event.target.checked)} /><span>包含 .env</span></label>
          <label className="inline"><input type="checkbox" checked={Boolean(backup.include_compose ?? true)} onChange={(event) => updateBackupField("include_compose", event.target.checked)} /><span>包含 docker-compose.yml</span></label>
          <label className="inline"><input type="checkbox" checked={Boolean(backup.include_version ?? true)} onChange={(event) => updateBackupField("include_version", event.target.checked)} /><span>包含 VERSION</span></label>
        </div>

        <div className="detail-panel detail-panel-plain">
          <div className="detail-grid detail-grid-two">
            <DetailItem label="状态" value={backupState?.last_status || (backupState?.enabled ? "Idle" : "Disabled")} />
            <DetailItem label="下次执行" value={fmtDate(backupState?.next_run_at)} />
            <DetailItem label="当前间隔" value={`${Number(backupState?.schedule_hour || 0)} 小时 ${Number(backupState?.schedule_minute || 0)} 分钟`} />
            <DetailItem label="最近开始" value={fmtDate(backupState?.last_started_at)} />
            <DetailItem label="最近结束" value={fmtDate(backupState?.last_finished_at)} />
            <DetailItem label="耗时" value={formatDuration(backupState?.last_duration_ms)} />
            <DetailItem label="触发方式" value={String(backupState?.last_trigger || "-")} />
            <DetailItem label="最近对象" value={String(backupState?.last_artifact?.key || "-")} />
            <DetailItem label="最近大小" value={backupState?.last_artifact?.size_bytes ? fmtBytes(backupState.last_artifact.size_bytes) : "-"} />
          </div>
          <p className="backup-note">`0 小时 1 分钟` 表示每 1 分钟自动备份一次；最小间隔为 1 分钟。</p>
          {backupState?.last_error ? <p className="detail-error">{backupState.last_error}</p> : null}
        </div>

        <div className="detail-panel detail-panel-plain">
          <div className="backup-list-head">
            <div className="backup-list-title">
              <strong>最近远端备份</strong>
              <span>{backupItems.length ? `${backupItems.length} 项` : "暂无远端备份"}</span>
            </div>
            <span className="chip">下载时自动返回解密后的 tar.gz</span>
          </div>
          <ScrollableTable tableRef={backupTableRef} className="data-table-wrap" height="medium">
            <table className="activity-table backup-table">
              <thead>
                <tr>
                  <th>备份文件</th>
                  <th>最近修改</th>
                  <th>大小</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {backupItems.length ? backupItems.map((item) => (
                  <tr key={item.key}>
                    <td>
                      <div className="backup-key-cell" title={item.key}>
                        <strong>{backupDisplayName(item.key)}</strong>
                        <small>{backupPrefixLabel(item.key)}</small>
                      </div>
                    </td>
                    <td>{fmtDate(item.last_modified)}</td>
                    <td>{item.size_bytes ? fmtBytes(item.size_bytes) : "-"}</td>
                    <td>
                      <div className="row-actions">
                        <IconButton title="下载解密后的压缩包" onClick={() => downloadBackup(item.key)}><Download size={15} /></IconButton>
                        <button className="ghost small danger-text" disabled={deletingBackupKey === item.key} onClick={() => removeBackup(item.key).catch((error) => toast("error", error.message))}>
                          {deletingBackupKey === item.key ? "删除中" : "删除"}
                        </button>
                      </div>
                    </td>
                  </tr>
                )) : (
                  <tr>
                    <td colSpan={4} className="table-empty">{backupBusy === "list" ? "远端备份加载中..." : "暂无远端备份"}</td>
                  </tr>
                )}
              </tbody>
            </table>
          </ScrollableTable>
        </div>
      </section>

      <section className="panel">
        <PanelHead
          title="原始 JSON"
          subtitle="高级设置可以直接编辑"
          action={<button className="secondary" onClick={() => { const parsed = parseJSON(json) as SettingsType; save(parsed).catch((error) => toast("error", error.message)); }}>保存 JSON</button>}
        />
        <textarea className="json-editor settings-json" value={json} onChange={(event) => setJson(event.target.value)} spellCheck={false} />
      </section>
    </div>
  );
}

function RegisterPanel({ token, registerRuntime, setRegisterRuntime, toast }: { token: string; registerRuntime: RegisterRuntime | null; setRegisterRuntime: React.Dispatch<React.SetStateAction<RegisterRuntime | null>>; toast: (type: Toast["type"], message: string) => void }) {
  const registerConfig = registerRuntime?.state?.config;
  const registerMail = registerConfig?.mail || {};
  const registerDomainsText = Array.isArray(registerMail.inbucket_domains) ? registerMail.inbucket_domains.join("\n") : "";
  const [registerLogs, setRegisterLogs] = useState<SystemLog[]>([]);
  const [registerLogsLoading, setRegisterLogsLoading] = useState(false);
  const [registerBusy, setRegisterBusy] = useState<"" | "save" | "start" | "stop" | "run-once" | "reload">("");
  const [registerLogStickBottom, setRegisterLogStickBottom] = useState(true);
  const logViewportRef = useRef<HTMLDivElement | null>(null);
  const [draftDirty, setDraftDirty] = useState(false);
  function registerDraftFromRuntime(runtime: RegisterRuntime | null) {
    const config = runtime?.state?.config;
    const mail = config?.mail || {};
    return {
      proxy: String(config?.proxy || ""),
      mode: String(config?.mode || "total"),
      total: Number(config?.total || 10),
      threads: Number(config?.threads || 3),
      targetQuota: Number(config?.target_quota || 100),
      targetAvailable: Number(config?.target_available || 10),
      checkIntervalSeconds: parseCheckIntervalSeconds(config?.check_interval),
      apiBase: String(mail.inbucket_api_base || ""),
      domains: Array.isArray(mail.inbucket_domains) ? mail.inbucket_domains.join("\n") : "",
      randomSubdomain: Boolean(mail.random_subdomain ?? true)
    };
  }
  const [draft, setDraft] = useState(() => registerDraftFromRuntime(registerRuntime));
  function updateDraft(patch: Partial<typeof draft>) {
    setDraft((current) => ({ ...current, ...patch }));
    setDraftDirty(true);
  }
  useEffect(() => {
    if (draftDirty) return;
    setDraft(registerDraftFromRuntime(registerRuntime));
  }, [draftDirty, registerRuntime, registerConfig?.proxy, registerConfig?.mode, registerConfig?.total, registerConfig?.threads, registerConfig?.target_quota, registerConfig?.target_available, registerConfig?.check_interval, registerMail.inbucket_api_base, registerDomainsText, registerMail.random_subdomain]);

  useEffect(() => {
    let cancelled = false;
    setRegisterLogsLoading(true);
    api.registerLogs(token)
      .then((data) => {
        if (cancelled) return;
        setRegisterLogs(sortLogs(data.items || []));
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
    if (document.visibilityState === "hidden") return;
    const timer = window.setInterval(() => {
      Promise.allSettled([api.registerLogs(token), api.registerState(token)]).then((results) => {
        const logResult = results[0];
        const stateResult = results[1];
        if (logResult.status === "fulfilled") {
          setRegisterLogs((current) => mergeRegisterLogs(current, logResult.value.items || []));
        }
        if (stateResult.status === "fulfilled") {
          setRegisterRuntime(stateResult.value);
        }
      }).catch(() => {});
    }, 2000);
    return () => window.clearInterval(timer);
  }, [token, setRegisterRuntime]);

  useEffect(() => {
    const viewport = logViewportRef.current;
    if (!viewport || !registerLogStickBottom) return;
    viewport.scrollTop = viewport.scrollHeight;
  }, [registerLogs, registerLogStickBottom]);

  function handleRegisterLogScroll() {
    const viewport = logViewportRef.current;
    if (!viewport) return;
    const threshold = 24;
    const distanceToBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight;
    setRegisterLogStickBottom(distanceToBottom <= threshold);
  }

  async function reloadRegister() {
    if (draftDirty && !window.confirm("刷新会丢弃当前未保存的注册配置修改，继续吗？")) {
      return;
    }
    setRegisterBusy("reload");
    try {
      const [runtime, logsData] = await Promise.all([api.registerState(token), api.registerLogs(token)]);
      setRegisterRuntime(runtime);
      setDraft(registerDraftFromRuntime(runtime));
      setDraftDirty(false);
      setRegisterLogs(sortLogs(logsData.items || []));
    } finally {
      setRegisterBusy("");
    }
  }
  async function saveRegister() {
    setRegisterBusy("save");
    try {
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
      const nextRuntime = registerRuntime ? { ...registerRuntime, state: data.state } : { state: data.state, running: false, last_error: "", last_result: null };
      setRegisterRuntime(nextRuntime);
      setDraft(registerDraftFromRuntime(nextRuntime));
      setDraftDirty(false);
      setRegisterLogs(sortLogs((await api.registerLogs(token)).items || []));
      toast("success", "注册配置已保存");
    } finally {
      setRegisterBusy("");
    }
  }
  async function startRegister() {
    setRegisterBusy("start");
    try {
      const data = await api.startRegister(token);
      setRegisterRuntime(data);
      setRegisterLogs(sortLogs((await api.registerLogs(token)).items || []));
      toast("success", "注册任务已启动");
    } finally {
      setRegisterBusy("");
    }
  }
  async function stopRegister() {
    setRegisterBusy("stop");
    try {
      const data = await api.stopRegister(token);
      setRegisterRuntime(data);
      setRegisterLogs(sortLogs((await api.registerLogs(token)).items || []));
      toast("success", "注册任务已停止");
    } finally {
      setRegisterBusy("");
    }
  }
  async function runRegisterOnce() {
    setRegisterBusy("run-once");
    try {
      const data = await api.runRegisterOnce(token);
      setRegisterRuntime(data);
      setRegisterLogs(sortLogs((await api.registerLogs(token)).items || []));
      toast("success", "单次注册已执行");
    } finally {
      setRegisterBusy("");
    }
  }

  async function refreshRegisterLogs() {
    setRegisterLogsLoading(true);
    try {
      setRegisterLogs(sortLogs((await api.registerLogs(token)).items || []));
    } finally {
      setRegisterLogsLoading(false);
    }
  }
  const registerRunning = Boolean(registerRuntime?.running);
  const registerBusyNow = registerBusy !== "";
  return <div className="stack"><div className="register-layout"><section className="panel"><PanelHead title="注册配置" subtitle="inbucket 邮箱、并发和目标模式" action={<><span className={classNames("chip", draftDirty && "warn")}>{draftDirty ? "未保存" : "已保存"}</span><button className="secondary small" disabled={registerBusyNow} onClick={() => reloadRegister().catch((error) => toast("error", error.message))}>{registerBusy === "reload" ? "刷新中" : "刷新"}</button><button className="secondary small" disabled={registerBusyNow || registerRunning} onClick={() => saveRegister().catch((error) => toast("error", error.message))}>{registerBusy === "save" ? "保存中" : "保存配置"}</button></>} /><div className="settings-form register-form"><label><span>Inbucket API Base</span><input value={draft.apiBase} onChange={(event) => updateDraft({ apiBase: event.target.value })} placeholder="http://127.0.0.1:9000" /></label><label><span>Register Proxy</span><input value={draft.proxy} onChange={(event) => updateDraft({ proxy: event.target.value })} placeholder="留空则继承全局 Proxy" /></label><label><span>模式</span><select value={draft.mode} onChange={(event) => updateDraft({ mode: event.target.value })}><option value="total">total</option><option value="quota">quota</option><option value="available">available</option></select></label><label><span>线程数</span><input type="number" value={draft.threads} onChange={(event) => updateDraft({ threads: Number(event.target.value) })} /></label><label><span>Total</span><input type="number" value={draft.total} onChange={(event) => updateDraft({ total: Number(event.target.value) })} /></label><label><span>Check Interval 秒</span><input type="number" value={draft.checkIntervalSeconds} onChange={(event) => updateDraft({ checkIntervalSeconds: Number(event.target.value) })} /></label><label><span>Target Quota</span><input type="number" value={draft.targetQuota} onChange={(event) => updateDraft({ targetQuota: Number(event.target.value) })} /></label><label><span>Target Available</span><input type="number" value={draft.targetAvailable} onChange={(event) => updateDraft({ targetAvailable: Number(event.target.value) })} /></label><label className="inline"><input type="checkbox" checked={draft.randomSubdomain} onChange={(event) => updateDraft({ randomSubdomain: event.target.checked })} /><span>随机子域名</span></label><label className="wide"><span>Inbucket Domains，每行一个</span><textarea value={draft.domains} onChange={(event) => updateDraft({ domains: event.target.value })} /></label></div></section><section className="panel"><PanelHead title="运行状态" subtitle="单次注册和批量注册控制" action={<div className="register-toolbar"><span className={classNames("badge", registerRuntime?.running ? "warn" : "ok")}>{registerRuntime?.running ? "Running" : "Idle"}</span></div>} /><div className="detail-grid register-stats"><DetailItem label="Success" value={String(Number(registerRuntime?.state?.stats?.success || 0))} /><DetailItem label="Fail" value={String(Number(registerRuntime?.state?.stats?.fail || 0))} /><DetailItem label="Done" value={String(Number(registerRuntime?.state?.stats?.done || 0))} /><DetailItem label="Running" value={String(Number(registerRuntime?.state?.stats?.running || 0))} /><DetailItem label="Success Rate" value={`${Number(registerRuntime?.state?.stats?.success_rate || 0).toFixed(1)}%`} /><DetailItem label="Quota" value={String(Number(registerRuntime?.state?.stats?.current_quota || 0))} /><DetailItem label="Available" value={String(Number(registerRuntime?.state?.stats?.current_available || 0))} /><DetailItem label="Elapsed" value={formatRegisterSeconds(registerRuntime?.state?.stats?.elapsed_seconds)} /><DetailItem label="Avg / Success" value={formatRegisterSeconds(registerRuntime?.state?.stats?.avg_seconds)} /><DetailItem label="Started" value={fmtDate(registerRuntime?.state?.stats?.started_at)} /><DetailItem label="Updated" value={fmtDate(registerRuntime?.state?.stats?.updated_at)} /></div>{registerRuntime?.last_error ? <p className="detail-error">{registerRuntime.last_error}</p> : null}{registerRuntime?.last_result?.email ? <p className="register-last">Last Success: {registerRuntime.last_result.email} · {fmtDate(registerRuntime.last_result.created_at)}</p> : null}<div className="register-actions"><button disabled={registerBusyNow || registerRunning} onClick={() => runRegisterOnce().catch((error) => toast("error", error.message))}>{registerBusy === "run-once" ? "执行中" : "单次注册"}</button><button className="secondary" disabled={registerBusyNow || registerRunning} onClick={() => startRegister().catch((error) => toast("error", error.message))}>{registerBusy === "start" ? "启动中" : "启动批量"}</button><button className="secondary-danger" disabled={registerBusyNow || !registerRunning} onClick={() => stopRegister().catch((error) => toast("error", error.message))}>{registerBusy === "stop" ? "停止中" : "停止批量"}</button></div></section></div><section className="panel"><PanelHead title="注册流水日志" subtitle="像终端输出一样实时追踪注册每一步进度" action={<div className="register-toolbar"><span className={classNames("chip", !registerLogStickBottom && "warn")}>{registerLogStickBottom ? "自动跟随" : "查看历史中"}</span><button className="secondary small" onClick={() => refreshRegisterLogs().catch((error) => toast("error", error.message))}>{registerLogsLoading ? "刷新中" : "刷新日志"}</button></div>} /><div ref={logViewportRef} className="terminal-log" onScroll={handleRegisterLogScroll}>{registerLogs.length ? registerLogs.map((log) => {
    const detail = logDetail(log);
    const level = typeof detail.level === "string" ? detail.level : "";
    const lineClass = classNames("terminal-log-line", Boolean(detail.error) && "err", level === "warn" && "warn");
    return <div key={log.id} className={lineClass}><span className="terminal-log-time">{fmtDate(log.time)}</span><span className="terminal-log-text">{formatRegisterLogLine(log)}</span></div>;
  }) : <div className="terminal-log-empty">{registerLogsLoading ? "日志加载中..." : "暂无注册日志"}</div>}</div></section></div>;
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
