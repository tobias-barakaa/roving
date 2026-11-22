// frontend/src/lib/stream/chat.ts
import type { RealtimeClient } from "../realtime/client";

export interface ChatMessage {
  roomId: string;
  text: string;
  userId: string;
  sentAt: string;
}

export interface ChatHandle {
  send(text: string): void;
  destroy(): void;
}

export function initChat(
  realtime: RealtimeClient,
  roomId: string,
  meUserId: string,
  logEl: HTMLElement,
  formEl: HTMLFormElement,
  inputEl: HTMLTextAreaElement,  
): ChatHandle {
  const off = realtime.on("chat.msg", (env) => {
    const payload = env.payload as ChatMessage | undefined;
    if (!payload || payload.roomId !== roomId) return;
    appendMessage(logEl, payload, meUserId);
  });

  const onSubmit = (e: Event) => {
    e.preventDefault();
    const text = inputEl.value.trim();
    if (!text) return;
    const id = realtime.send("chat.send", { roomId, text });
    if (id === null) return; // socket not open; nothing queued — drop silently
    inputEl.value = "";
  };
  formEl.addEventListener("submit", onSubmit);

  return {
    send(text) {
      realtime.send("chat.send", { roomId, text });
    },
    destroy() {
      off();
      formEl.removeEventListener("submit", onSubmit);
    },
  };
}

function appendMessage(logEl: HTMLElement, m: ChatMessage, meUserId: string) {
  const row = document.createElement("div");
  row.className = "rm-chat__msg";
  if (m.userId === meUserId) row.classList.add("rm-chat__msg--me");

  const meta = document.createElement("div");
  meta.className = "rm-chat__meta";
  const who = document.createElement("span");
  who.textContent = m.userId === meUserId ? "You" : "Member";
  const when = document.createElement("time");
  when.dateTime = m.sentAt;
  when.textContent = new Date(m.sentAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  meta.append(who, when);

  const body = document.createElement("p");
  body.className = "rm-chat__text";
  body.textContent = m.text; // textContent, NOT innerHTML — prevents XSS

  row.append(meta, body);
  logEl.append(row);
  logEl.scrollTop = logEl.scrollHeight;
}