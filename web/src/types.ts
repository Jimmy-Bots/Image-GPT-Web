export type Role = "admin" | "user";

export type Identity = {
  id: string;
  key_id?: string;
  name: string;
  role: Role;
  auth_type: "legacy" | "session" | "api_key";
};

export type Account = {
  token_ref: string;
  access_token_masked: string;
  type: string;
  status: string;
  quota: number;
  image_quota_unknown?: boolean;
  email?: string;
  user_id?: string;
  limits_progress?: unknown;
  default_model_slug?: string;
  restore_at?: string;
  success: number;
  fail: number;
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
};

export type SystemLog = {
  id: string;
  time: string;
  type: string;
  summary: string;
  detail?: unknown;
};

export type ModelItem = {
  id: string;
  object?: string;
  owned_by?: string;
};

export type Settings = Record<string, unknown>;

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
