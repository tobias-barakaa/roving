/**
 * Group workspace routes. Flip a flag to true when that page exists and the
 * matching nav tab and "View all" links turn into real links automatically.
 */
export const ready = {
  rooms: false,
  schedule: false,
  members: false,
  discussion: false,
  settings: false,
} as const;

export function groupPaths(id: string) {
  const base = `/groups/${encodeURIComponent(id)}`;
  return {
    overview: base,
    rooms: `${base}/rooms`,
    schedule: `${base}/sessions`,
    members: `${base}/members`,
    discussion: `${base}/chat`,
    settings: `${base}/settings`,
    room: (roomId: string) => `${base}/rooms/${encodeURIComponent(roomId)}`,
  };
}
