/**
 * Every backend path the frontend calls lives here, so a wrong guess is a
 * one-line fix.
 *
 *  CONFIRMED   = named in the project brief.
 *  UNCONFIRMED = assumed from conventions. Check against your Gin router.
 */
export const endpoints = {
  auth: {
    me: "/auth/me", // CONFIRMED
    login: "/auth/login", // UNCONFIRMED
    register: "/auth/register", // UNCONFIRMED
    logout: "/auth/logout", // UNCONFIRMED
  },
  groups: {
    list: "/groups", // UNCONFIRMED  (GET, supports ?q= &visibility= &membership=)
    create: "/groups", // UNCONFIRMED  (POST)
    detail: (id: string) => `/groups/${encodeURIComponent(id)}`, // UNCONFIRMED
    join: (id: string) => `/groups/${encodeURIComponent(id)}/join`, // UNCONFIRMED
  },
} as const;
