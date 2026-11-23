// frontend: src/lib/rooms/roomNotes.ts
import { notesApi, type Note } from "../api/notes";
import { describeError } from "../api/client";

/**
 * "My notes" for one room, saved through the existing Notes API.
 *
 * - One private note per person per room, found by its title, created on the
 *   first thing you type, then autosaved.
 * - "Share with the group" flips that note's visibility; the group's shared
 *   notes show underneath, read-only.
 * Nothing here is new on the backend: it only uses POST/GET /notes,
 * PATCH /notes/:id and GET /groups/:id/notes.
 */
export interface NotesHandle { flush(): void; destroy(): void; }

const SAVE_DELAY_MS = 900;

export function initRoomNotes(root: HTMLElement, ctx: { groupId: string; roomName: string }): NotesHandle {
  const $ = <T extends HTMLElement>(s: string) => root.querySelector<T>(s)!;
  const area = $<HTMLTextAreaElement>("[data-rm-notes-text]");
  const status = $("[data-rm-notes-status]");
  const share = $<HTMLInputElement>("[data-rm-notes-share]");
  const sharedBox = $("[data-rm-notes-shared]");

  const title = `Room notes: ${ctx.roomName}`.slice(0, 200);
  let note: Note | null = null;
  let timer: number | undefined;
  let saving: Promise<void> | null = null;
  let dirty = false;
  let loaded = false;

  const say = (t: string, bad = false) => { status.textContent = t; status.dataset.bad = bad ? "1" : ""; };
  const clock = () => new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  async function load() {
    say("Loading your notes…");
    try {
      const mine = await notesApi.listMine({ groupId: ctx.groupId, q: title, limit: 50 });
      note = mine.find((n) => n.title === title) ?? null;
      if (note) { area.value = note.body; share.checked = note.visibility === "group"; }
      area.disabled = share.disabled = false;
      loaded = true;
      say(note ? "Your notes for this room" : "Nothing yet. Start typing and it saves by itself.");
    } catch (err) {
      say(`Couldn't load your notes. ${describeError(err)}`, true);
      area.disabled = false; // still let them write; the first save will retry
      loaded = true;
    }
    void loadShared();
  }

  async function loadShared() {
    try {
      const list = (await notesApi.listShared(ctx.groupId)).filter((n) => !n.isMine);
      sharedBox.replaceChildren();
      if (!list.length) { sharedBox.hidden = true; return; }
      sharedBox.hidden = false;
      const h = document.createElement("h3"); h.textContent = "Shared with the group";
      sharedBox.append(h);
      for (const n of list.slice(0, 8)) {
        const d = document.createElement("details"); d.className = "rm-note";
        const s = document.createElement("summary");
        s.textContent = `${n.title || "Untitled"}${n.authorName ? ` · ${n.authorName}` : ""}`;
        const p = document.createElement("p"); p.textContent = n.body;
        d.append(s, p); sharedBox.append(d);
      }
    } catch { sharedBox.hidden = true; }
  }

  async function save(keepalive = false) {
    if (!loaded || !dirty) return;
    if (saving) { await saving; if (!dirty) return; }
    dirty = false;
    const body = area.value;
    const visibility = share.checked ? "group" : "private";
    say("Saving…");
    saving = (async () => {
      try {
        if (!note) {
          if (!body.trim()) { say("Nothing to save yet."); return; }
          note = await notesApi.create({ groupId: ctx.groupId, title, body, visibility });
        } else {
          await notesApi.update(note.id, { body, visibility }, keepalive);
        }
        say(`Saved at ${clock()}`);
        if (share.checked || visibility === "group") void loadShared();
      } catch (err) {
        dirty = true;
        say(`Couldn't save (${describeError(err)}). We'll try again.`, true);
        schedule(4000);
      } finally { saving = null; }
    })();
    await saving;
  }

  function schedule(ms = SAVE_DELAY_MS) {
    window.clearTimeout(timer);
    timer = window.setTimeout(() => void save(), ms);
  }

  const onInput = () => { dirty = true; say("Unsaved changes…"); schedule(); };
  const onShare = () => { dirty = true; void save(); };
  area.addEventListener("input", onInput);
  share.addEventListener("change", onShare);
  area.disabled = share.disabled = true;
  void load();

  return {
    flush() { window.clearTimeout(timer); void save(true); },
    destroy() { window.clearTimeout(timer); area.removeEventListener("input", onInput); share.removeEventListener("change", onShare); },
  };
}
