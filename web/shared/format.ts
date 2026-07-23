import type { Locale } from "./i18n";

export function formatDate(value: string | number | undefined, locale: Locale, fallback = "-") {
  if (value === undefined || value === null || value === "") return fallback;
  const date = typeof value === "number" && value < 10_000_000_000 ? new Date(value * 1000) : new Date(value);
  if (Number.isNaN(date.getTime())) return fallback;
  return new Intl.DateTimeFormat(locale === "zh" ? "zh-CN" : "en", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

export function formatDuration(milliseconds: number | undefined) {
  if (milliseconds === undefined || Number.isNaN(milliseconds)) return "-";
  if (milliseconds < 1000) return `${Math.max(0, Math.round(milliseconds))} ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 1 : 0)} s`;
  return `${Math.floor(milliseconds / 60_000)}m ${Math.round((milliseconds % 60_000) / 1000)}s`;
}

export function truncateMiddle(value: string, max = 28) {
  if (value.length <= max) return value;
  const side = Math.floor((max - 1) / 2);
  return `${value.slice(0, side)}…${value.slice(-side)}`;
}

/** Format a timestamp value to "YYYY-MM-DD HH:mm:ss" (East Eight Time Zone). */
export function formatTimestamp(value: string | number | undefined, fallback = "-") {
  if (value === undefined || value === null || value === "") return fallback;
  const date = typeof value === "number" && value < 10_000_000_000 ? new Date(value * 1000) : new Date(value);
  if (Number.isNaN(date.getTime())) return fallback;
  const pad = (n: number) => String(n).padStart(2, "0");
  const Y = date.getFullYear();
  const M = pad(date.getMonth() + 1);
  const D = pad(date.getDate());
  const h = pad(date.getHours());
  const m = pad(date.getMinutes());
  const s = pad(date.getSeconds());
  return `${Y}-${M}-${D} ${h}:${m}:${s}`;
}
