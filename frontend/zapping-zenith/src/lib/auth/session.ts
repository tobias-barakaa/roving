import { authApi, type User } from "../api/auth";
import { ApiError, describeError } from "../api/client";

/**
 * Client-side session check for dashboard pages. This is for UX only:
 * the Go backend is what actually protects data.
 * The result is cached so the layout, sidebar and page all share one /auth/me call.
 */
let cached: Promise<User> | null = null;

export function getSession(): Promise<User> {
  if (!cached) cached = authApi.me();
  return cached;
}

export function clearSession() { cached = null; }

/** Resolves with the user, or redirects to /login on 401. Other errors are surfaced to the caller. */
export async function requireUser(): Promise<User> {
  try {
    return await getSession();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      window.location.replace("/login");
      return new Promise<User>(() => {}); // never resolves; page is leaving
    }
    cached = null;
    throw new Error(describeError(err));
  }
}

export function displayNameOf(u: User): string {
  return (u.displayName as string) || (u.email ? String(u.email).split("@")[0] : "reader");
}
