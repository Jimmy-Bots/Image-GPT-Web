import type { Account, AccountListSummary, AccountRefreshStatus, ApiKey, ImageTask, ModelItem, PagedResult, RegisterConfig, RegisterRuntime, Settings, StoredImage, SystemLog, User } from "./types";

const storageKey = "gpt_image_web_token";

export function getStoredToken() {
  return sessionStorage.getItem(storageKey) || "";
}

export function setStoredToken(token: string) {
  if (token) sessionStorage.setItem(storageKey, token);
  else sessionStorage.removeItem(storageKey);
}

export function authHeaders(token: string, extra: HeadersInit = {}) {
  const headers = new Headers(extra);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return headers;
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
    const message =
      typeof data === "object" && data && "error" in data
        ? String((data as { error?: { message?: string } }).error?.message || res.statusText)
        : res.statusText;
    throw new Error(message);
  }
  return data as T;
}

export type MeResponse = {
  identity: {
    id: string;
    key_id?: string;
    name: string;
    role: "admin" | "user";
    auth_type: "legacy" | "session" | "api_key";
  };
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
  loginWithPassword: (email: string, password: string) =>
    request<{ token: string; role: string; name: string; version: string }>("", "/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password })
    }),
  me: (token: string) => request<MeResponse>(token, "/api/me"),
  version: (token: string) => request<{ version: string }>(token, "/version"),
  models: (token: string) => request<{ data: ModelItem[] }>(token, "/v1/models"),
  accounts: (token: string, params: { page?: number; pageSize?: number; query?: string; status?: string; accountType?: string } = {}) =>
    request<PagedResult<Account> & { summary: AccountListSummary }>(token, withQuery("/api/accounts", {
      page: params.page,
      page_size: params.pageSize,
      query: params.query,
      status: params.status,
      account_type: params.accountType
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
  updateAccount: (token: string, tokenRef: string, body: { status?: string; type?: string; quota?: number; password?: string }) =>
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
  createUser: (token: string, body: { email: string; name?: string; password: string; role: string }) =>
    request<{ item: User; api_key?: ApiKey; key?: string }>(token, "/api/users", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }),
  updateUser: (token: string, id: string, body: Partial<Pick<User, "email" | "name" | "role" | "status">> & { password?: string }) =>
    request<{ item: User }>(token, `/api/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
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
  tasks: (token: string, ids: string[] = [], params: { page?: number; pageSize?: number; query?: string; status?: string } = {}) =>
    request<PagedResult<ImageTask> & { missing_ids: string[] }>(token, ids.length
      ? `/api/image-tasks?ids=${encodeURIComponent(ids.join(","))}`
      : withQuery("/api/image-tasks", {
          page: params.page,
          page_size: params.pageSize,
          query: params.query,
          status: params.status
        })),
  deleteTasks: (token: string, ids: string[]) =>
    request<{ removed: number }>(token, "/api/image-tasks/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids })
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
  settings: (token: string) => request<{ config: Settings }>(token, "/api/settings"),
  saveSettings: (token: string, settings: Settings) =>
    request<{ config: Settings }>(token, "/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(settings)
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
  storage: (token: string) => request<{ backend: { type: string; path: string }; health: { status: string } }>(token, "/api/storage/info"),
  logs: (token: string, type = "", ids: string[] = [], params: { page?: number; pageSize?: number; query?: string } = {}) =>
    request<PagedResult<SystemLog>>(token, withQuery("/api/logs", {
      type,
      ids: ids.length ? ids.join(",") : "",
      page: ids.length ? undefined : params.page,
      page_size: ids.length ? undefined : params.pageSize,
      query: ids.length ? undefined : params.query
    })),
  deleteLogs: (token: string, ids: string[]) =>
    request<{ removed: number }>(token, "/api/logs/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids })
    })
};
