/**
 * Rovingr API client.
 *
 * - Talks to the Go/Gin backend using cookie sessions (credentials: "include").
 * - Errors from the backend look like {"error": "..."}  -> thrown as ApiError.
 * - Listings look like {"items": [...], "nextCursor": "..."} -> see Page<T>.
 *
 * Set PUBLIC_API_URL in .env (e.g. https://api.rovingr.zelisline.com).
 * For cookies to flow, the API must send CORS headers with
 * Access-Control-Allow-Credentials: true and an explicit allowed origin.
 */

// const REGISTER_PATH = "/auth/register";
// const LOGIN_PATH = "/auth/login";
// const BASE_URL = (import.meta.env.PUBLIC_API_URL ?? "http://localhost:8080").replace(
//   /\/+$/,
//   "",
// );
const BASE_URL = (
  import.meta.env.PUBLIC_API_URL ??
  "http://localhost:8080/api/v1"
).replace(/\/+$/, "");

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export type Page<T> = {
  items: T[];
  nextCursor: string | null;
};

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

async function request<T>(
  method: Method,
  path: string,
  body?: unknown,
  init: RequestInit = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(`${BASE_URL}${path}`, {
    method,
    credentials: "include",
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    ...init,
  });

  if (res.status === 204) return undefined as T;

  const data = await res.json().catch(() => null);

  if (!res.ok) {
    const message =
      (data && typeof data.error === "string" && data.error) ||
      res.statusText ||
      "Request failed";
    throw new ApiError(res.status, message);
  }

  return data as T;
}

export const api = {
  get: <T>(path: string, init?: RequestInit) =>
    request<T>("GET", path, undefined, init),
  post: <T>(path: string, body?: unknown, init?: RequestInit) =>
    request<T>("POST", path, body ?? {}, init),
  put: <T>(path: string, body?: unknown, init?: RequestInit) =>
    request<T>("PUT", path, body ?? {}, init),
  patch: <T>(path: string, body?: unknown, init?: RequestInit) =>
    request<T>("PATCH", path, body ?? {}, init),
  delete: <T>(path: string, init?: RequestInit) =>
    request<T>("DELETE", path, undefined, init),
};
