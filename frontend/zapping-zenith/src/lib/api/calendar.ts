/**
 * Calendar integration is NOT connected yet. Events will be derived from the
 * existing scheduled study sessions and recurring series in the Go backend.
 * No separate calendar storage is introduced.
 */
export interface CalendarEvent {
  id: string;
  title: string;
  startsAt: string; // ISO 8601
  endsAt?: string;
  groupId?: string;
  groupName?: string;
}

export type CalendarView = "month" | "week" | "day";

export const calendarStatus = { connected: false } as const;

/** Resolves to null until the scheduling endpoints are wired in. */
export async function getUpcomingSessions(): Promise<CalendarEvent[] | null> {
  return null;
}
