import type { Account, AccountListSummary, AccountRefreshStatus, ApiKey, AuthResponse, BackupArtifact, BackupRemoteItem, BackupState, ImageTask, ModelItem, PagedResult, RegisterConfig, RegisterRuntime, RegisterStatus, Settings, StoredImage, StoredReferenceImage, SystemLog, TaskEvent, User } from "./types";

const storageKey = "gpt_image_web_token";

export function getStoredToken() {
  const persisted = localStorage.getItem(storageKey) || "";
  if (persisted) return persisted;
  const sessionToken = sessionStorage.getItem(storageKey) || "";
  if (sessionToken) {
    localStorage.setItem(storageKey, sessionToken);
    sessionStorage.removeItem(storageKey);
  }
  return sessionToken;
}

export function setStoredToken(token: string) {
  if (token) {
    localStorage.setItem(storageKey, token);
    sessionStorage.removeItem(storageKey);
    return;
  }
  localStorage.removeItem(storageKey);
  sessionStorage.removeItem(storageKey);
}

export function authHeaders(token: string, extra: HeadersInit = {}) {
  const headers = new Headers(extra);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return headers;
}

export function withTokenQuery(path: string, token: string, params: Record<string, string | number | boolean | undefined | null> = {}) {
  const search = new URLSearchParams();
  if (token) search.set("access_token", token);
  Object.entries(params).forEach(([key, value]) => {
    if (value === undefined || value === null || value === "") return;
    search.set(key, String(value));
  });
  const query = search.toString();
  return query ? `${path}?${query}` : path;
}

export async function request<T>(token: string, path: string, options: RequestInit = {}): Promise<T> {
  const headers = authHeaders(token, options.headers);
  const res = await fetch(path, { ...options, headers, credentials: "same-origin" });
  const text = await res.text();
  let data: unknown = {};
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!res.ok) {
    const errorCode =
      typeof data === "object" && data && "error" in data
        ? String((data as { error?: { code?: string } }).error?.code || "")
        : "";
    const message =
      typeof data === "object" && data && "error" in data
        ? String((data as { error?: { message?: string } }).error?.message || res.statusText)
        : res.statusText;
    const error = new Error(message) as Error & { code?: string; status?: number };
    error.code = errorCode;
    error.status = res.status;
    throw error;
  }
  return data as T;
}

export type MeResponse = {
  identity: {
    id: string;
    key_id?: string;
    name: string;
    role: "admin" | "user";
    auth_type: "session" | "api_key";
  };
  version?: string;
  model_policy?: {
    workbench_model?: string;
    image_max_count?: number;
    allowed_public_models?: string[];
    is_admin?: boolean;
  };
  user?: User;
};

function withQuery(path: string, params: Record<string, string | number | boolean | undefined | null>) {
  const search = new URLSearchParams();
  Object.entries(params).forEach(([key, value]) => {
    if (value === undefined || value === null || value === "") return;
    search.set(key, String(value));
  });
  const query = search.toString();
  return query ? `${path}?${query}` : path;
}

export const api = {
  health: () => request<{ ok: boolean; version?: string }>("", "/healthz"),
  registerStatus: () => request<{ status: RegisterStatus }>("", "/auth/register/status"),
  loginWithPassword: (email: string, password: string) =>
    request<AuthResponse>("", "/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password })
    }),
  registerWithPassword: (body: { email: string; name?: string; password: string; verification_code: string }) =>
    request<{ user: User; token: string; expires_at?: string }>("", "/auth/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }),
  sendRegisterCode: (email: string) =>
    request<{ ok: boolean }>("", "/auth/register/send-code", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email })
    }),
  sendPasswordResetCode: (email: string) =>
    request<{ ok: boolean }>("", "/auth/password-reset/send-code", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email })
    }),
  resetPassword: (body: { email: string; password: string; verification_code: string }) =>
    request<{ ok: boolean }>("", "/auth/password-reset/confirm", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }),
  me: (token: string) => request<MeResponse>(token, "/api/me"),
  version: (token: string) => request<{ version: string }>(token, "/version"),
  models: (token: string) => request<{ data: ModelItem[] }>(token, "/v1/models"),
  accounts: (token: string, params: { page?: number; pageSize?: number; query?: string; status?: string; accountType?: string; activeOnly?: boolean; dueOnly?: boolean } = {}) =>
    request<PagedResult<Account> & { summary: AccountListSummary }>(token, withQuery("/api/accounts", {
      page: params.page,
      page_size: params.pageSize,
      query: params.query,
      status: params.status,
      account_type: params.accountType,
      active_only: params.activeOnly,
      due_only: params.dueOnly
    })),
  accountRefreshStatus: (token: string) => request<{ status: AccountRefreshStatus }>(token, "/api/accounts/refresh-status"),
  deleteAccounts: (token: string, tokenRefs: string[]) =>
    request<{ removed: number }>(token, "/api/accounts", {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token_refs: tokenRefs })
    }),
  refreshAccounts: (token: string, tokenRefs: string[] = []) =>
    request<{ refreshed: number; errors: Array<{ token_ref?: string; error?: string }> }>(token, "/api/accounts/refresh", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token_refs: tokenRefs })
    }),
  refreshDueAccounts: (token: string) =>
    request<{ selected: number; refreshed: number; errors: Array<{ token_ref?: string; error?: string }> }>(token, "/api/accounts/refresh-due", {
      method: "POST"
    }),
  updateAccount: (token: string, tokenRef: string, body: { status?: string; type?: string; quota?: number; password?: string; max_concurrency?: number }) =>
    request<{ item: Account }>(token, "/api/accounts/update", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token_ref: tokenRef, ...body })
    }),
  users: (token: string, params: { page?: number; pageSize?: number; query?: string; status?: string; role?: string } = {}) =>
    request<PagedResult<User>>(token, withQuery("/api/users", {
      page: params.page,
      page_size: params.pageSize,
      query: params.query,
      status: params.status,
      role: params.role
    })),
  createUser: (token: string, body: { email: string; name?: string; password: string; role: string; quota_unlimited?: boolean; permanent_quota?: number; temporary_quota?: number; temporary_quota_date?: string }) =>
    request<{ item: User; api_key?: ApiKey; key?: string }>(token, "/api/users", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }),
  updateUser: (token: string, id: string, body: Partial<Pick<User, "email" | "name" | "role" | "status" | "quota_unlimited" | "permanent_quota" | "temporary_quota" | "temporary_quota_date" | "daily_temporary_quota">> & { password?: string; add_permanent_quota?: number }) =>
    request<{ item: User }>(token, `/api/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }),
  batchUsers: (token: string, body: { ids: string[]; action: "enable" | "disable" | "delete" | "grant_temporary_quota" | "grant_permanent_quota" | "set_temporary_quota"; status?: string; temporary_quota?: number; permanent_quota?: number }) =>
    request<{ updated: number; items: User[] }>(token, "/api/users/batch", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }),
  deleteUser: (token: string, id: string) =>
    request<{ item: User }>(token, `/api/users/${encodeURIComponent(id)}`, {
      method: "DELETE"
    }),
  resetUserKey: (token: string, id: string) =>
    request<{ item: ApiKey; key: string }>(token, `/api/users/${encodeURIComponent(id)}/api-key/reset`, {
      method: "POST"
    }),
  tasks: (token: string, ids: string[] = [], params: { page?: number; pageSize?: number; query?: string; status?: string; mode?: string; model?: string; size?: string; ownerID?: string; dateFrom?: string; dateTo?: string; deleted?: string; includeDeleted?: boolean } = {}) =>
    request<PagedResult<ImageTask> & { missing_ids: string[] }>(token, ids.length
      ? withQuery("/api/image-tasks", { ids: ids.join(","), owner_id: params.ownerID, include_deleted: params.includeDeleted })
      : withQuery("/api/image-tasks", {
          page: params.page,
          page_size: params.pageSize,
          query: params.query,
          status: params.status,
          mode: params.mode,
          model: params.model,
          owner_id: params.ownerID,
          size: params.size,
          date_from: params.dateFrom,
          date_to: params.dateTo,
          deleted: params.deleted,
          include_deleted: params.includeDeleted
        })),
  taskEvents: (token: string, id: string, params: { ownerID?: string } = {}) =>
    request<{ items: TaskEvent[]; total: number }>(token, withQuery(`/api/image-tasks/${encodeURIComponent(id)}/events`, { owner_id: params.ownerID })),
  deleteTasks: (token: string, items: Array<{ id: string; owner_id?: string }>) =>
    request<{ removed: number }>(token, "/api/image-tasks/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ items })
    }),
  createGenerationTask: (token: string, body: { client_task_id: string; prompt: string; model: string; size?: string; n?: number }) =>
    request<ImageTask>(token, "/api/image-tasks/generations", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }),
  createEditTask: (token: string, body: FormData) =>
    request<ImageTask>(token, "/api/image-tasks/edits", {
      method: "POST",
      body
    }),
  images: (token: string, params: { page?: number; pageSize?: number; query?: string; sort?: string; dateScope?: string } = {}) =>
    request<PagedResult<StoredImage>>(token, withQuery("/api/images", {
      page: params.page,
      page_size: params.pageSize,
      query: params.query,
      sort: params.sort,
      date_scope: params.dateScope
    })),
  deleteImages: (token: string, paths: string[]) =>
    request<{ removed: number }>(token, "/api/images/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ paths })
    }),
  referenceImages: (token: string, params: { page?: number; pageSize?: number; query?: string } = {}) =>
    request<PagedResult<StoredReferenceImage>>(token, withQuery("/api/reference-images", {
      page: params.page,
      page_size: params.pageSize,
      query: params.query
    })),
  deleteReferenceImages: (token: string, paths: string[]) =>
    request<{ removed: number }>(token, "/api/reference-images/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ paths })
    }),
  settings: (token: string) => request<{ config: Settings }>(token, "/api/settings"),
  saveSettings: (token: string, settings: Settings) =>
    request<{ config: Settings }>(token, "/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(settings)
    }),
  testSMTPMail: (token: string, to: string) =>
    request<{ ok: boolean }>(token, "/api/settings/mail/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ to })
    }),
  registerState: (token: string) => request<RegisterRuntime>(token, "/api/register/state"),
  registerLogs: (token: string) => request<{ items: SystemLog[] }>(token, "/api/register/logs"),
  saveRegisterConfig: (token: string, config: RegisterConfig) =>
    request<{ state: RegisterRuntime["state"] }>(token, "/api/register/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(config)
    }),
  startRegister: (token: string) =>
    request<RegisterRuntime>(token, "/api/register/start", {
      method: "POST"
    }),
  stopRegister: (token: string) =>
    request<RegisterRuntime>(token, "/api/register/stop", {
      method: "POST"
    }),
  runRegisterOnce: (token: string) =>
    request<RegisterRuntime>(token, "/api/register/run-once", {
      method: "POST"
    }),
  backupState: (token: string) => request<{ state: BackupState }>(token, "/api/backup/state"),
  runBackup: (token: string) =>
    request<{ state: BackupState; artifact?: BackupArtifact | null }>(token, "/api/backup/run", {
      method: "POST"
    }),
  listBackups: (token: string) =>
    request<{ items: BackupRemoteItem[] }>(token, "/api/backup/items"),
  deleteBackup: (token: string, key: string) =>
    request<{ ok: boolean }>(token, "/api/backup/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ key })
    }),
  storage: (token: string) => request<{ backend: { type: string; path: string }; health: { status: string } }>(token, "/api/storage/info"),
  logs: (token: string, type = "", ids: string[] = [], params: { page?: number; pageSize?: number; query?: string; actorID?: string; subjectID?: string; taskID?: string; endpoint?: string; status?: string; dateFrom?: string; dateTo?: string } = {}) =>
    request<PagedResult<SystemLog>>(token, withQuery("/api/logs", {
      type,
      ids: ids.length ? ids.join(",") : "",
      page: ids.length ? undefined : params.page,
      page_size: ids.length ? undefined : params.pageSize,
      query: ids.length ? undefined : params.query,
      actor_id: ids.length ? undefined : params.actorID,
      subject_id: ids.length ? undefined : params.subjectID,
      task_id: ids.length ? undefined : params.taskID,
      endpoint: ids.length ? undefined : params.endpoint,
      status: ids.length ? undefined : params.status,
      date_from: ids.length ? undefined : params.dateFrom,
      date_to: ids.length ? undefined : params.dateTo
    })),
  deleteLogs: (token: string, ids: string[]) =>
    request<{ removed: number }>(token, "/api/logs/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids })
    })
};
