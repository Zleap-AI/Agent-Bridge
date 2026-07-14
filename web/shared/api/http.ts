export interface ApiErrorBody {
  code?: string;
  message?: string;
  error?: string | { code?: string; message?: string };
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(message: string, status = 0, code = "REQUEST_FAILED") {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === "AbortError";
}

function errorFromBody(body: ApiErrorBody | null, status: number, fallback: string) {
  const nested = typeof body?.error === "object" ? body.error : null;
  const message = nested?.message || body?.message || (typeof body?.error === "string" ? body.error : fallback);
  const code = nested?.code || body?.code || `HTTP_${status}`;
  return new ApiError(message, status, code);
}

export async function requestJSON<T>(path: string, init: RequestInit = {}, timeoutMs = 30_000): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  headers.set("Accept", "application/json");

  const controller = new AbortController();
  let timedOut = false;
  const abortFromCaller = () => controller.abort(init.signal?.reason);
  if (init.signal?.aborted) abortFromCaller();
  else init.signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout = timeoutMs > 0 ? window.setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs) : null;

  try {
    const response = await fetch(path, { ...init, headers, credentials: "same-origin", signal: controller.signal });
    const text = await response.text();
    let body: unknown = null;
    if (text) {
      try {
        body = JSON.parse(text);
      } catch {
        if (response.ok) throw new ApiError("The server returned invalid JSON", response.status, "INVALID_RESPONSE");
      }
    }

    if (!response.ok) {
      throw errorFromBody((body || null) as ApiErrorBody | null, response.status, response.statusText || "Request failed");
    }
    return body as T;
  } catch (error) {
    if (timedOut) throw new ApiError("Request timed out", 0, "TIMEOUT");
    if (init.signal?.aborted || isAbortError(error)) throw error;
    if (error instanceof ApiError) throw error;
    throw new ApiError(error instanceof Error ? error.message : "Network request failed", 0, "NETWORK_ERROR");
  } finally {
    if (timeout !== null) window.clearTimeout(timeout);
    init.signal?.removeEventListener("abort", abortFromCaller);
  }
}

export function listFrom<T>(value: unknown, ...keys: string[]): T[] {
  if (Array.isArray(value)) return value as T[];
  if (!value || typeof value !== "object") return [];
  const record = value as Record<string, unknown>;
  for (const key of keys) {
    if (Array.isArray(record[key])) return record[key] as T[];
  }
  if (record.data) return listFrom<T>(record.data, ...keys);
  return [];
}

interface RawSSEEvent {
  event: string;
  data: string;
}

export async function readSSE(
  response: Response,
  onEvent: (event: RawSSEEvent) => void,
): Promise<void> {
  if (!response.body) throw new ApiError("Streaming response has no body", response.status, "INVALID_STREAM");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  const emitBlock = (block: string) => {
    let event = "message";
    const data: string[] = [];
    for (const line of block.split(/\r?\n/)) {
      if (!line || line.startsWith(":")) continue;
      const separator = line.indexOf(":");
      const field = separator < 0 ? line : line.slice(0, separator);
      const value = separator < 0 ? "" : line.slice(separator + 1).replace(/^ /, "");
      if (field === "event") event = value;
      if (field === "data") data.push(value);
    }
    if (data.length) onEvent({ event, data: data.join("\n") });
  };

  while (true) {
    const { done, value } = await reader.read();
    buffer += decoder.decode(value, { stream: !done }).replace(/\r\n/g, "\n");
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      emitBlock(buffer.slice(0, boundary));
      buffer = buffer.slice(boundary + 2);
      boundary = buffer.indexOf("\n\n");
    }
    if (done) break;
  }
  if (buffer.trim()) emitBlock(buffer);
}

export async function streamRequest(
  path: string,
  body: unknown,
  onEvent: (event: RawSSEEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  let response: Response;
  try {
    response = await fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: { Accept: "text/event-stream", "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw new ApiError(error instanceof Error ? error.message : "Network request failed", 0, "NETWORK_ERROR");
  }
  if (!response.ok) {
    let bodyValue: ApiErrorBody | null = null;
    try { bodyValue = await response.json() as ApiErrorBody; } catch { /* response may be empty */ }
    throw errorFromBody(bodyValue, response.status, response.statusText || "Request failed");
  }
  await readSSE(response, onEvent);
}
