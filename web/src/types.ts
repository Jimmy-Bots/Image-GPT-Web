export type Role = "admin" | "user";

export type Identity = {
  id: string;
  key_id?: string;
  name: string;
  role: Role;
  auth_type: "legacy" | "session" | "api_key";
};

export type ModelPolicy = {
  workbench_model?: string;
  image_max_count?: number;
  allowed_public_models?: string[];
  is_admin?: boolean;
};

export type Account = {
  token_ref: string;
  access_token_masked: string;
  password?: string;
  type: string;
  status: string;
  quota: number;
  max_concurrency?: number;
  image_quota_unknown?: boolean;
  email?: string;
  user_id?: string;
  limits_progress?: unknown;
  default_model_slug?: string;
  restore_at?: string;
  success: number;
  fail: number;
  active_requests?: number;
  allowed_concurrency?: number;
  last_used_at?: string;
  created_at: string;
  updated_at: string;
};

export type User = {
  id: string;
  email: string;
  name: string;
  role: Role;
  status: "active" | "disabled" | "deleted";
  quota_unlimited: boolean;
  permanent_quota: number;
  temporary_quota: number;
  temporary_quota_date?: string;
  available_quota: number;
  api_key?: ApiKey;
  created_at: string;
  updated_at: string;
  last_login_at?: string;
};

export type ApiKey = {
  id: string;
  user_id?: string;
  name: string;
  role: Role;
  enabled: boolean;
  created_at: string;
  last_used_at?: string;
};

export type ImageTask = {
  id: string;
  status: "queued" | "running" | "success" | "error";
  mode: "generate" | "edit";
  model?: string;
  size?: string;
  prompt?: string;
  data?: ImageResult[];
  error?: string;
  created_at: string;
  updated_at: string;
};

export type ImageResult = {
  url?: string;
  path?: string;
  b64_json?: string;
  revised_prompt?: string;
};

export type StoredImage = {
  path: string;
  name: string;
  url: string;
  size: number;
  created_at: string;
  prompt?: string;
  revised_prompt?: string;
  display_prompt?: string;
};

export type SystemLog = {
  id: string;
  time: string;
  type: string;
  summary: string;
  detail?: unknown;
};

export type PagedResult<T> = {
  items: T[];
  total: number;
  page: number;
  page_size: number;
};

export type AccountListSummary = {
  total: number;
  normal: number;
  success: number;
  fail: number;
  quota_total: number;
  quota_unknown: boolean;
  quota_unlimited: boolean;
  active_requests?: number;
  total_concurrency?: number;
};

export type ModelItem = {
  id: string;
  object?: string;
  owned_by?: string;
};

export type Settings = Record<string, unknown>;

export type RegisterConfig = {
  proxy?: string;
  mode?: "total" | "quota" | "available";
  total?: number;
  threads?: number;
  target_quota?: number;
  target_available?: number;
  check_interval_seconds?: number;
  mail?: {
    inbucket_api_base?: string;
    inbucket_domains?: string[];
    random_subdomain?: boolean;
  };
};

export type RegisterState = {
  config: {
    proxy?: string;
    total: number;
    threads: number;
    mode: "total" | "quota" | "available";
    target_quota: number;
    target_available: number;
    check_interval: number | string;
    mail?: {
      inbucket_api_base?: string;
      inbucket_domains?: string[];
      random_subdomain?: boolean;
    };
  };
  enabled: boolean;
  stats?: {
    success?: number;
    fail?: number;
    done?: number;
    running?: number;
    threads?: number;
    elapsed_seconds?: number;
    avg_seconds?: number;
    success_rate?: number;
    current_quota?: number;
    current_available?: number;
    started_at?: string;
    updated_at?: string;
    finished_at?: string;
  };
};

export type RegisterRuntime = {
  state: RegisterState;
  last_error?: string;
  running: boolean;
  last_result?: {
    email?: string;
    created_at?: string;
  } | null;
};

export type Toast = {
  id: string;
  type: "success" | "error" | "info";
  message: string;
};

export type ReferenceImage = {
  id: string;
  name: string;
  file: File;
  dataUrl: string;
};

export type AccountRefreshStatus = {
  running: boolean;
  interval_minutes: number;
  concurrency: number;
  normal_batch_size: number;
  due_count?: number;
  next_run_at?: string;
  last_started_at?: string;
  last_finished_at?: string;
  last_duration_ms?: number;
  last_selected: number;
  last_limited: number;
  last_normal: number;
  last_refreshed: number;
  last_failed: number;
  last_error?: string;
};
