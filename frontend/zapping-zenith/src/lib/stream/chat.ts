// frontend: src/lib/stream/chat.ts
import type { RealtimeClient } from "../realtime/client";

export interface ChatMessage {
  roomId: string;
  text: string;
  userId: string;
  sentAt: string;
}

export interface ChatHandle {
  destroy(): void;
}

/**
 * Live room chat over the realtime hub. Messages are delivered to whoever is in
 * the room right now; they are not stored yet (persistent chat is its own stage).
 * `labelOf` turns a user id into the name shown above a message.
 */
export function initChat(
  realtime: RealtimeClient,
  roomId: string,
  meUserId: string,
  logEl: HTMLElement,
  formEl: HTMLFormElement,
  inputEl: HTMLTextAreaElement,
  labelOf: (userId: string) => string = (u) => (u === meUserId ? "You" : "Member"),
): ChatHandle {
  const off = realtime.on("chat.msg", (env) => {
    const m = env.payload as ChatMessage | undefined;
    if (!m || m.roomId !== roomId) return;
    logEl.querySelector(".rm-chat__empty")?.remove();
    appendMessage(logEl, m, meUserId, labelOf(m.userId));
  });

  const onSubmit = (e: Event) => {
    e.preventDefault();
    const text = inputEl.value.trim();
    if (!text) return;
    if (realtime.send("chat.send", { roomId, text }) === null) return; // not connected: keep the text
    inputEl.value = "";
  };
  // Enter sends, Shift+Enter makes a new line.
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); formEl.requestSubmit(); }
  };
  formEl.addEventListener("submit", onSubmit);
  inputEl.addEventListener("keydown", onKey);

  return {
    destroy() {
      off();
      formEl.removeEventListener("submit", onSubmit);
      inputEl.removeEventListener("keydown", onKey);
    },
  };
}

function appendMessage(logEl: HTMLElement, m: ChatMessage, meUserId: string, who: string) {
  const row = document.createElement("div");
  row.className = "rm-chat__msg" + (m.userId === meUserId ? " rm-chat__msg--me" : "");

  const meta = document.createElement("div");
  meta.className = "rm-chat__meta";
  const name = document.createElement("span");
  name.textContent = who;
  const when = document.createElement("time");
  when.dateTime = m.sentAt;
  when.textContent = new Date(m.sentAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  meta.append(name, when);

  const body = document.createElement("p");
  body.className = "rm-chat__text";
  body.textContent = m.text; // textContent, never innerHTML: no XSS

  row.append(meta, body);
  const stick = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 40;
  logEl.append(row);
  if (stick) logEl.scrollTop = logEl.scrollHeight;
}
