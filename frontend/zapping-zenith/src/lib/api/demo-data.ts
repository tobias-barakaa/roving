// src/lib/api/demo-data.ts
// NOTE: This is DEMONSTRATION DATA ONLY. It will be replaced by real API
// calls (groups, reading, scheduling) during a later integration phase.

import type {
  Category,
  ReadingMaterial,
  StudyGroup,
  StudySession,
} from "./types";

export const DEMO_CATEGORIES: Category[] = [
  { id: "technology", label: "Technology", count: 128 },
  { id: "science", label: "Science", count: 96 },
  { id: "mathematics", label: "Mathematics", count: 74 },
  { id: "literature", label: "Literature", count: 152 },
  { id: "business", label: "Business", count: 88 },
  { id: "philosophy", label: "Philosophy", count: 61 },
  { id: "psychology", label: "Psychology", count: 69 },
  { id: "history", label: "History", count: 103 },
  { id: "languages", label: "Languages", count: 47 },
  { id: "arts", label: "Arts", count: 54 },
  { id: "education", label: "Education", count: 39 },
  { id: "health", label: "Health", count: 72 },
];

export const DEMO_GROUPS: StudyGroup[] = [
  {
    id: "g1",
    slug: "distributed-systems",
    name: "Distributed Systems Reading Group",
    subject: "Technology",
    description:
      "Weekly deep dives into consensus, replication and fault-tolerant design.",
    memberCount: 214,
    activeNow: 12,
    coverColor: "#3a5a8c",
    coverInitials: "DS",
  },
  {
    id: "g2",
    slug: "quantum-curious",
    name: "Quantum Curious",
    subject: "Science",
    description:
      "A friendly entry point to quantum mechanics for the mathematically inclined.",
    memberCount: 178,
    activeNow: 5,
    coverColor: "#4a3f7a",
    coverInitials: "QC",
  },
  {
    id: "g3",
    slug: "proof-and-puzzle",
    name: "Proof & Puzzle",
    subject: "Mathematics",
    description:
      "Olympiad-style problem solving, proofs and recreational math each weekend.",
    memberCount: 342,
    activeNow: 21,
    coverColor: "#6b4a2a",
    coverInitials: "PP",
  },
  {
    id: "g4",
    slug: "slow-reading-classics",
    name: "Slow Reading Classics",
    subject: "Literature",
    description:
      "One book a month, read slowly and discussed with care. No spoilers, no rush.",
    memberCount: 509,
    activeNow: 8,
    coverColor: "#5a2f3f",
    coverInitials: "SR",
  },
  {
    id: "g5",
    slug: "founders-library",
    name: "The Founder's Library",
    subject: "Business",
    description:
      "Shared reading on strategy, product and the craft of building companies.",
    memberCount: 267,
    activeNow: 14,
    coverColor: "#2f5a4a",
    coverInitials: "FL",
  },
  {
    id: "g6",
    slug: "ethics-seminar",
    name: "Evening Ethics Seminar",
    subject: "Philosophy",
    description:
      "Monthly seminar on applied ethics — from AI to medicine to civic life.",
    memberCount: 121,
    activeNow: 3,
    coverColor: "#3f4a6b",
    coverInitials: "EE",
  },
];

export const DEMO_READING: ReadingMaterial[] = [
  { id: "r1", slug: "structure-of-scientific-revolutions", title: "The Structure of Scientific Revolutions", author: "Thomas S. Kuhn", subject: "Science", category: "Classic", coverColor: "#4a5a2a", coverInitials: "SK" },
  { id: "r2", slug: "thinking-fast-and-slow", title: "Thinking, Fast and Slow", author: "Daniel Kahneman", subject: "Psychology", category: "Popular", coverColor: "#5a3a2a", coverInitials: "TK" },
  { id: "r3", slug: "meditations", title: "Meditations", author: "Marcus Aurelius", subject: "Philosophy", category: "Classic", coverColor: "#2a4a5a", coverInitials: "MA" },
  { id: "r4", slug: "designing-data-intensive-applications", title: "Designing Data-Intensive Applications", author: "Martin Kleppmann", subject: "Technology", category: "Technical", coverColor: "#3a2a5a", coverInitials: "DK" },
  { id: "r5", slug: "the-art-of-war", title: "The Art of War", author: "Sun Tzu", subject: "History", category: "Classic", coverColor: "#5a2a2a", coverInitials: "SZ" },
  { id: "r6", slug: "godel-escher-bach", title: "Gödel, Escher, Bach", author: "Douglas Hofstadter", subject: "Mathematics", category: "Classic", coverColor: "#2a5a3a", coverInitials: "GE" },
  { id: "r7", slug: "principles-of-economics", title: "Principles of Economics", author: "Alfred Marshall", subject: "Business", category: "Open Access", coverColor: "#4a3a5a", coverInitials: "PE" },
  { id: "r8", slug: "the-language-instinct", title: "The Language Instinct", author: "Steven Pinker", subject: "Languages", category: "Popular", coverColor: "#5a4a2a", coverInitials: "LI" },
];

export const DEMO_SESSIONS: StudySession[] = [
  {
    id: "s1",
    title: "Consensus & Replication",
    kind: "group",
    startsAt: "2026-03-02T18:00:00Z",
    durationMinutes: 90,
    participants: 12,
    capacity: 25,
    host: "Distributed Systems Reading Group",
  },
  {
    id: "s2",
    title: "Deep Focus Pomodoro",
    kind: "pomodoro",
    startsAt: "2026-03-02T20:00:00Z",
    durationMinutes: 25,
    participants: 34,
    host: "Open focus room",
  },
  {
    id: "s3",
    title: "Olympiad Problem Set",
    kind: "group",
    startsAt: "2026-03-03T09:00:00Z",
    durationMinutes: 60,
    participants: 8,
    capacity: 15,
    host: "Proof & Puzzle",
  },
  {
    id: "s4",
    title: "Silent Study Room",
    kind: "room",
    startsAt: "2026-03-03T14:00:00Z",
    durationMinutes: 120,
    participants: 21,
    host: "Collaborative room",
  },
];