import { api } from "./client";
import { endpoints } from "./endpoints";

/** Shape of /auth/me. Backend returns camelCase; extra fields are tolerated. */
export interface User {
  id: string;
  email?: string;
  displayName?: string;
  [key: string]: unknown;
}

export interface RegisterResult {
  /** UNCONFIRMED field name: set when the backend wants email verification first. */
  verificationRequired?: boolean;
  [key: string]: unknown;
}

export const authApi = {
  me: () => api.get<User>(endpoints.auth.me),
  login: (email: string, password: string) => api.post<User>(endpoints.auth.login, { email, password }),
  register: (displayName: string, email: string, password: string) =>
    api.post<RegisterResult>(endpoints.auth.register, { displayName, email, password }),
  logout: () => api.post<void>(endpoints.auth.logout),
};
