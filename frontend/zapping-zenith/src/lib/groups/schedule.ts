/**
 * Adapter between the group overview and your scheduling API.
 *
 * The scheduling handlers/DTOs were not provided, so nothing is called here yet
 * and the overview shows an honest "not connected" state (no fake sessions).
 * To connect it: call your existing schedulingApi inside loadUpcomingSessions,
 * map each item to GroupSession, and return the array ([] when none).
 */
export interface GroupSession {
  id: string;
  title: string;
  startsAt: string; // ISO 8601
  endsAt?: string;
  timezone?: string; // IANA zone from the backend, e.g. "Africa/Nairobi"
  roomName?: string;
  status?: string;
}

export async function loadUpcomingSessions(_groupId: string): Promise<GroupSession[] | null> {
  return null; // null = not connected
}
