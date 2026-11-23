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
    mine: "/groups",
    public: "/groups/public",
    list: "/groups",
    create: "/groups",
    detail: (id: string) => `/groups/${encodeURIComponent(id)}`,
    join: (id: string) => `/groups/${encodeURIComponent(id)}/join`,
  },

  rooms: {
    list: (gid: string) => `/groups/${encodeURIComponent(gid)}/rooms`, // CONFIRMED  GET  -> {"rooms": [...]}
    create: (gid: string) => `/groups/${encodeURIComponent(gid)}/rooms`, // CONFIRMED  POST
    detail: (gid: string, rid: string) =>
      `/groups/${encodeURIComponent(gid)}/rooms/${encodeURIComponent(rid)}`, // CONFIRMED  GET (adds canEnter, myRoomRole)
  },

  // CONFIRMED against internal/notes/handler.go
  notes: {
    mine: "/notes", // GET ?groupId=&q=&limit=  -> {"notes":[...]}   POST -> NoteResponse (201)
    one: (id: string) => `/notes/${encodeURIComponent(id)}`, // PATCH -> 204
    shared: (gid: string) => `/groups/${encodeURIComponent(gid)}/notes`, // GET -> {"notes":[...]}
  },
} as const;


// /**
//  * Every backend path the frontend calls lives here, so a wrong guess is a
//  * one-line fix.
//  *
//  *  CONFIRMED   = named in the project brief.
//  *  UNCONFIRMED = assumed from conventions. Check against your Gin router.
//  */
// export const endpoints = {
//   auth: {
//     me: "/auth/me", // CONFIRMED
//     login: "/auth/login", // UNCONFIRMED
//     register: "/auth/register", // UNCONFIRMED
//     logout: "/auth/logout", // UNCONFIRMED
//   },
 
//   groups: {
//     mine: "/groups",
//     public: "/groups/public",
//     list: "/groups",
//     create: "/groups",
  
//     detail: (id: string) =>
//       `/groups/${encodeURIComponent(id)}`,
  
//     join: (id: string) =>
//       `/groups/${encodeURIComponent(id)}/join`,
//   },

//   rooms: {
//       list: (gid: string) => `/groups/${encodeURIComponent(gid)}/rooms`, // CONFIRMED  GET  -> {"rooms": [...]}
//       create: (gid: string) => `/groups/${encodeURIComponent(gid)}/rooms`, // CONFIRMED  POST
//       detail: (gid: string, rid: string) =>
//       `/groups/${encodeURIComponent(gid)}/rooms/${encodeURIComponent(rid)}`, // CONFIRMED  GET (adds canEnter, myRoomRole)
//       ai: {
//         ask: "/ai/ask", // UNCONFIRMED: the route you'll add on the backend
//       },
//   },

//   ai: {
//     ask: "/ai/ask", 
//   },

// } as const;



