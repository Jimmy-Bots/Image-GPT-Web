import type { Account, ImageResult, StoredImage } from "./types";

export function fmtDate(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const pad = (num: number) => String(num).padStart(2, "0");
  return `${date.getFullYear()}/${pad(date.getMonth() + 1)}/${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function formatRemainingTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const diff = date.getTime() - Date.now();
  if (diff <= 0) return "已到期";
  const totalMinutes = Math.ceil(diff / 60000);
  const days = Math.floor(totalMinutes / 1440);
  const hours = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;
  const parts = [];
  if (days > 0) parts.push(`${days}天`);
  if (hours > 0) parts.push(`${hours}小时`);
  if (minutes > 0 || parts.length === 0) parts.push(`${minutes}分钟`);
  return `${parts.join("")}后`;
}

export function formatNextRefreshTime(updatedAt?: string | null, intervalMinutes = 5) {
  if (!updatedAt) return "-";
  const date = new Date(updatedAt);
  if (Number.isNaN(date.getTime())) return "-";
  const next = new Date(date.getTime() + Math.max(1, intervalMinutes) * 60000);
  const remaining = next.getTime() - Date.now();
  if (remaining <= 0) return "待刷新";
  const minutes = Math.ceil(remaining / 60000);
  return `${minutes}分钟后`;
}

export function fmtBytes(value: number) {
  if (!Number.isFinite(value)) return "-";
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(2)} MB`;
}

export function compact(value: number) {
  if (!Number.isFinite(value)) return "-";
  if (value >= 1000) return `${(value / 1000).toFixed(1)}k`;
  return String(value);
}

export function classNames(...values: Array<string | false | undefined | null>) {
  return values.filter(Boolean).join(" ");
}

export function statusClass(status?: string | boolean) {
  const value = String(status ?? "");
  if (/正常|active|true|success|healthy|enabled/i.test(value)) return "ok";
  if (/限流|queued|running|warning|unknown/i.test(value)) return "warn";
  if (/异常|禁用|disabled|false|error|deleted|unhealthy|offline/i.test(value)) return "err";
  return "";
}

export function formatQuota(account: Account) {
  if (account.type === "pro" || account.type === "prolite") return "∞";
  if (account.image_quota_unknown) return "未知";
  return String(Math.max(0, account.quota || 0));
}

export function quotaSummary(accounts: Account[]) {
  const active = accounts.filter((item) => item.status === "正常");
  if (active.some((item) => item.type === "pro" || item.type === "prolite")) return "∞";
  if (active.some((item) => item.image_quota_unknown)) return "未知";
  return compact(active.reduce((sum, item) => sum + Math.max(0, item.quota || 0), 0));
}

export function createID(prefix = "task") {
  if (crypto.randomUUID) return crypto.randomUUID();
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function withImageToken(url: string, token?: string) {
  const value = String(url || "").trim();
  if (!value || value.startsWith("data:")) return value;
  return value;
}

export function imageSrc(item: ImageResult, token?: string) {
  if (item.url) return withImageToken(item.url, token);
  if (item.b64_json) return `data:image/png;base64,${item.b64_json}`;
  return "";
}

export function storedImageURL(item: StoredImage, token?: string) {
  return withImageToken(item.url, token);
}

export function parseTaskData(data?: unknown): ImageResult[] {
  if (!data) return [];
  if (Array.isArray(data)) return data as ImageResult[];
  if (typeof data === "string") {
    try {
      const parsed = JSON.parse(data);
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  }
  return [];
}

export async function fileToDataURL(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(new Error("读取图片失败"));
    reader.readAsDataURL(file);
  });
}

export function copyText(value: string) {
  return navigator.clipboard.writeText(value);
}

export function safeJSON(value: unknown) {
  return JSON.stringify(value, null, 2);
}

export function parseJSON(value: string) {
  try {
    return JSON.parse(value) as unknown;
  } catch (error) {
    throw new Error(error instanceof Error ? error.message : "JSON 无效");
  }
}
