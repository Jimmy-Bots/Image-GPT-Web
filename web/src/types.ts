export type Role = "admin" | "user";

export type Identity = {
  id: string;
  key_id?: string;
  name: string;
  role: Role;
  auth_type: "session" | "api_key";
};

export type ModelPolicy = {
  workbench_model?: string;
  image_max_count?: number;
  allowed_public_models?: string[];
  is_admin?: boolean;
};

export type AuthResponse = {
  token: string;
  role: string;
  name: string;
  version: string;
  user?: User;
};

export type RegisterStatus = {
  enabled: boolean;
  needs_bootstrap: boolean;
  ordinary_users: number;
  max_ordinary_users: number;
  remaining_ordinary: number;
  allowed_email_domains: string[];
  invite_enabled?: boolean;
  code_cooldown_seconds: number;
  can_register: boolean;
  disabled_reason?: string;
};

export type RegistrationSettingsState = {
  public_registration_enabled?: boolean;
  invite_registration_enabled?: boolean;
  register_code_cooldown_seconds?: number;
  register_allowed_email_domains?: string[];
  register_max_ordinary_users?: number;
};

export type InviteCode = {
  code: string;
  enabled: boolean;
  max_uses: number;
  used_count: number;
  created_at: string;
  updated_at: string;
  last_used_at?: string;
  last_used_by?: string;
  description?: string;
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
  recovery_state?: string;
  recovery_error?: string;
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
  daily_temporary_quota?: number;
  quota_used_total: number;
  quota_used_today: number;
  quota_used_date?: string;
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

export type ApiKeyResetResponse = {
  item: ApiKey;
  key: string;
};

export type ImageTask = {
  id: string;
  owner_id?: string;
  owner_email?: string;
  owner_name?: string;
  owner_role?: Role;
  status: "queued" | "running" | "success" | "error" | "cancelled";
  phase?: string;
  mode: "generate" | "edit";
  model?: string;
  size?: string;
  prompt?: string;
  requested_count?: number;
  data?: ImageResult[];
  error?: string;
  created_at: string;
  updated_at: string;
  deleted_at?: string;
  deleted_by?: string;
};

export type TaskEvent = {
  id: string;
  task_id: string;
  time: string;
  type: string;
  summary: string;
  detail?: unknown;
};

export type ImageResult = {
  url?: string;
  preview_url?: string;
  path?: string;
  b64_json?: string;
  revised_prompt?: string;
};

export type StoredImage = {
  path: string;
  name: string;
  url: string;
  preview_url?: string;
  size: number;
  created_at: string;
  prompt?: string;
  revised_prompt?: string;
  display_prompt?: string;
};

export type StoredReferenceImage = {
  path: string;
  name: string;
  url?: string;
  preview_url?: string;
  size: number;
  created_at: string;
  owner_id?: string;
  original_name?: string;
  content_type?: string;
};

export type SystemLog = {
  id: string;
  time: string;
  type: string;
  summary: string;
  actor_id?: string;
  subject_id?: string;
  task_id?: string;
  endpoint?: string;
  status?: string;
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
    provider?: "inbucket" | "spamok";
    inbucket_api_base?: string;
    inbucket_domains?: string[];
    random_subdomain?: boolean;
    spamok_base_url?: string;
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
      provider?: "inbucket" | "spamok";
      inbucket_api_base?: string;
      inbucket_domains?: string[];
      random_subdomain?: boolean;
      spamok_base_url?: string;
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
  mode?: string;
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
  current_token_ref?: string;
  current_stage?: string;
};

export type BackupArtifact = {
  key: string;
  size_bytes: number;
  sha256: string;
};

export type BackupRemoteItem = {
  key: string;
  size_bytes: number;
  last_modified?: string;
};

export type BackupState = {
  running: boolean;
  enabled: boolean;
  schedule_hour: number;
  schedule_minute: number;
  keep_latest: number;
  next_run_at?: string;
  last_started_at?: string;
  last_finished_at?: string;
  last_duration_ms?: number;
  last_status?: string;
  last_error?: string;
  last_trigger?: string;
  last_artifact?: BackupArtifact | null;
};
