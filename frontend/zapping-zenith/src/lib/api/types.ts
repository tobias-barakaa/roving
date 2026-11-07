// src/lib/api/types.ts

/** A study group / community. Mirrors future `groups` API DTO. */
export interface StudyGroup {
  id: string;
  slug: string;
  name: string;
  subject: string;
  description: string;
  memberCount: number;
  activeNow?: number;
  coverColor: string; // used for visual identity block
  coverInitials: string;
}

/** A reading material entry. Mirrors future `reading` API DTO. */
export interface ReadingMaterial {
  id: string;
  slug: string;
  title: string;
  author?: string;
  subject: string;
  category: string; // e.g. "Classic", "Open Access"
  coverColor: string;
  coverInitials: string;
}

/** A scheduled session. Mirrors future `scheduling` API DTO. */
export interface StudySession {
  id: string;
  title: string;
  kind: "group" | "pomodoro" | "room";
  startsAt: string; // ISO8601
  durationMinutes: number;
  participants: number;
  capacity?: number;
  host: string;
}

/** A subject category used in the catalogue grid. */
export interface Category {
  id: string;
  label: string;
  count: number;
}