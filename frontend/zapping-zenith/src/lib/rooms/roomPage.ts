// frontend: src/lib/rooms/roomPage.ts
/**
 * Room page controller. Loads the room, decides which honest state to show, then runs the live room.
 *
 * Four different things, kept apart on purpose (code and wording):
 *   1. the persistent ROOM        -> `Room` from lib/api/room (a place that exists)
 *   2. a FOCUS SESSION            -> not built; never mentioned here
 *   3. the live STREAM (the call) -> lib/stream/*, peer-to-peer WebRTC, only while people are in the room
 *   4. the WebSocket CONNECTION   -> RealtimeClient; the badge in the People tab
 *
 * Who is the host is decided by the SERVER (stream.joined). Only the group owner publishes video;
 * everyone else has a microphone. This file only follows that decision to decide what to show.
 */

import { roomsApi, type Room } from "../api/room";
import { describeError } from "../api/client";
import { requireUser, displayNameOf } from "../auth/session";
import { getGroup } from "../groups/context";
import { LocalMedia, type DeviceList, type KindState, type LocalMediaState, type MediaFailure } from "../media/localMedia";
import { startLevelMeter, type LevelMeter } from "../media/levelMeter";
import { showState } from "../ui/state";
import { blockedCopy, viewForError, viewForRoom } from "./roomView";
import { RealtimeClient } from "../realtime/client";
import type { RealtimeStatus } from "../realtime/types";
import type { User } from "../api/auth";
import { initStream, type HostVideoState } from "../stream/streamPage";
import type { RosterEntry, StreamRole } from "../stream/types";
import { initChat } from "../stream/chat";
import { initRoomNotes } from "./roomNotes";

/* ------------------------------------------------------------ small helpers */

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text?: string) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

const initialsOf = (name: string) =>
  name.trim().split(/\s+/).slice(0, 2).map((w) => w.charAt(0).toUpperCase()).join("") || "?";

const pad = (n: number) => String(n).padStart(2, "0");
function formatElapsed(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  return h ? `${h}:${pad(m)}:${pad(sec)}` : `${pad(m)}:${pad(sec)}`;
}

/* ------------------------------------------------------------ connection badge */

const CONNECTION_UI: Record<RealtimeStatus, { label: string; cls: string; announce: boolean }> = {
  idle: { label: "Not connected yet", cls: "gx-badge--muted", announce: false },
  connecting: { label: "Connecting…", cls: "", announce: false },
  open: { label: "Connected", cls: "gx-badge--ok", announce: true },
  reconnecting: { label: "Reconnecting…", cls: "rm-badge--warn", announce: true },
  closed: { label: "Disconnected", cls: "gx-badge--muted", announce: true },
  unauthorized: { label: "Session ended", cls: "rm-badge--bad", announce: true },
};

/* ------------------------------------------------------------------- entry point */

export function initRoomPage() {
  const found = document.querySelector<HTMLElement>("[data-rm]");
  if (!found) return;
  const root: HTMLElement = found;

  const groupId = root.dataset.groupId!;
  const roomId = root.dataset.roomId!;
  const groupHref = root.dataset.groupHref!;
  const roomsHref = root.dataset.roomsHref!;

  const $ = <T extends HTMLElement>(s: string) => root.querySelector<T>(s)!;
  const stateBox = $("[data-rm-state]");
  const roomBox = $("[data-rm-room]");
  const blockedBox = $("[data-rm-blocked]");
  const enterBox = $("[data-rm-enterable]");
  const live = $("[data-rm-live]");
  let entered = false;

  const announce = (text: string) => { live.textContent = ""; window.setTimeout(() => { live.textContent = text; }, 30); };

  function showProblem(opts: { title: string; text: string; retry?: boolean }) {
    roomBox.hidden = true;
    stateBox.hidden = false;
    showState(stateBox, "error", { title: opts.title, text: opts.text, action: opts.retry ? { label: "Try again", onClick: load } : undefined });
    const row = el("div", "rm-state-actions");
    const back = el("a", "btn btn--ghost btn--sm", "Back to group");
    back.setAttribute("href", groupHref);
    row.append(back);
    stateBox.querySelector(".state")?.append(row);
  }

  async function load() {
    roomBox.hidden = true;
    stateBox.hidden = false;
    showState(stateBox, "loading", { title: "Opening the room…" });

    let user: User;
    try {
      user = await requireUser(); // 401 -> redirects to /login and never resolves
    } catch (err) {
      showProblem({ title: "Couldn't check your session", text: err instanceof Error ? err.message : describeError(err), retry: true });
      return;
    }

    let room: Room;
    try {
      room = await roomsApi.get(groupId, roomId);
    } catch (err) {
      const v = viewForError(err);
      if (v.kind === "missing") {
        showProblem({ title: "We couldn't open this room", text: "The link may be wrong, the room may have been removed, or you may not have access to this group." });
      } else {
        showProblem({ title: "Couldn't load this room", text: describeError(err), retry: true });
      }
      return;
    }

    let groupName = room.groupName;
    if (!groupName) groupName = await getGroup(groupId).then((g) => g.name).catch(() => undefined);
    fillHeader(room, groupName ?? "Group");
    stateBox.hidden = true;
    roomBox.hidden = false;

    const view = viewForRoom(room);
    if (view.kind === "blocked") {
      enterBox.hidden = true; // no camera or microphone for someone who can't enter
      const copy = blockedCopy[view.reason];
      blockedBox.replaceChildren();
      const card = el("section", "rm-blocked");
      card.setAttribute("aria-labelledby", "rm-blocked-title");
      const h = el("h2", "", copy.title); h.id = "rm-blocked-title";
      const back = el("a", "btn btn--ghost btn--sm", "Back to rooms");
      back.setAttribute("href", roomsHref);
      card.append(h, el("p", "", copy.text), back);
      blockedBox.append(card);
      blockedBox.hidden = false;
      announce(copy.title);
      return;
    }

    blockedBox.hidden = true;
    enterBox.hidden = false;
    if (!entered) {
      entered = true;
      enterRoom(user, room);
    }
  }

  function fillHeader(room: Room, groupName: string) {
    $("[data-rm-crumb-group]").textContent = groupName;
    $("[data-rm-crumb-room]").textContent = room.name;
    $("[data-rm-name]").textContent = room.name;
    document.title = `${room.name} — Rovingr`;

    const desc = $("[data-rm-desc]");
    desc.textContent = room.description ?? "";
    desc.hidden = !room.description;

    const badges = $("[data-rm-badges]");
    badges.replaceChildren();
    const restricted = room.access === "restricted";
    badges.append(el("span", `gx-badge ${restricted ? "gx-badge--muted" : "gx-badge--ok"}`, restricted ? "Restricted" : "Open to group"));
    if (typeof room.capacity === "number") badges.append(el("span", "gx-badge gx-badge--muted", `Up to ${room.capacity} people`));
    if (room.archivedAt) badges.append(el("span", "gx-badge gx-badge--muted", "Archived"));
  }

  /* ----------------------------------------------------------- the room itself */

  function enterRoom(user: User, room: Room) {
    const media = new LocalMedia();
    const myName = displayNameOf(user);
    const enteredAt = Date.now();
    let role: StreamRole | "pending" = "pending";
    let roster: RosterEntry[] = [];
    let myConnId = "";
    let hostState: HostVideoState = { present: false, link: "none", live: false };
    let wsStatus: RealtimeStatus = "idle";

    // Stage
    const tile = $("[data-rm-self]");
    const video = $<HTMLVideoElement>("[data-rm-video]");
    const cover = $("[data-rm-cover]");
    const camStartBtn = $<HTMLButtonElement>("[data-rm-cam-start]");
    const camStartLabel = $("[data-rm-cam-start-label]");
    const micStartBtns = Array.from(root.querySelectorAll<HTMLButtonElement>("[data-rm-mic-start]"));
    const micRow = $("[data-rm-mic-row]");
    const tileMuted = $("[data-rm-tile-muted]");
    const onAir = $("[data-rm-onair]");
    const camErrBox = $("[data-rm-cam-error]");
    const micErrBox = $("[data-rm-mic-error]");
    const problemBox = $("[data-rm-problem]");
    const meterBox = $("[data-rm-meterbox]");
    const meterBar = $("[data-rm-meter-bar]");
    const micState = $("[data-rm-mic-state]");
    const camPick = $("[data-rm-cam-pick]");
    const micPick = $("[data-rm-mic-pick]");
    const camSelect = $<HTMLSelectElement>("[data-rm-cam-select]");
    const micSelect = $<HTMLSelectElement>("[data-rm-mic-select]");
    const devicesHint = $("[data-rm-devices-hint]");
    const remoteVideo = $<HTMLVideoElement>("[data-rm-remote-video]");
    const remoteWrap = $("[data-rm-remote-wrap]");
    const remoteCover = $("[data-rm-remote-cover]");
    const remoteTitle = $("[data-rm-remote-title]");
    const remoteText = $("[data-rm-remote-text]");
    const playBtn = $<HTMLButtonElement>("[data-rm-play]");
    const audioSink = $("[data-rm-audio-sink]");
    const joiningTitle = $("[data-rm-joining-title]");
    const joiningText = $("[data-rm-joining-text]");

    // Session bar
    const clockEl = $("[data-rm-clock]");
    const secEl = $("[data-rm-seconds]");
    const dateEl = $("[data-rm-date]");
    const tzEl = $("[data-rm-tz]");
    const elapsedEl = $("[data-rm-elapsed]");
    const countEl = $("[data-rm-count]");
    const statusPill = $("[data-rm-status]");
    const statusText = $("[data-rm-status-text]");

    // Control bar
    const barCam = $<HTMLButtonElement>("[data-rm-bar-cam]");
    const barMic = $<HTMLButtonElement>("[data-rm-bar-mic]");
    const barCamState = $("[data-rm-bar-cam-state]");
    const barMicState = $("[data-rm-bar-mic-state]");
    const sideToggle = $<HTMLButtonElement>("[data-rm-side-toggle]");

    // Side panel
    const side = $("[data-rm-side]");
    const layout = $("[data-rm-layout]");
    const peopleList = $("[data-rm-people-list]");
    const connBadge = $("[data-rm-conn]");
    const connNote = $("[data-rm-conn-note]");

    root.querySelectorAll("[data-rm-initials]").forEach((n) => (n.textContent = initialsOf(myName)));

    /* ---- clock and time in room ---- */
    const tzName = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(new Date()).find((p) => p.type === "timeZoneName")?.value ?? "";
    const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
    const dateFmt = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long" });
    function tick() {
      const now = new Date();
      clockEl.textContent = timeFmt.format(now);
      clockEl.setAttribute("datetime", now.toISOString());
      secEl.textContent = `:${pad(now.getSeconds())}`;
      dateEl.textContent = dateFmt.format(now);
      tzEl.textContent = tzName;
      elapsedEl.textContent = formatElapsed(Date.now() - enteredAt);
    }
    tick();
    const clockTimer = window.setInterval(tick, 1000);

    /* ---- role: decided by the server ---- */
    function setRole(r: StreamRole) {
      role = r;
      enterBox.dataset.role = r;
      announce(r === "host" ? "You are the host of this room." : "You joined as a member. Only the host shares video.");
      // Whatever the person had on when the role arrived keeps working; nothing is switched on for them.
      renderStatus();
      renderHost();
      renderPeople();
    }

    /* ---- status pill ---- */
    function renderStatus() {
      let state = "connecting", text = "Connecting…";
      if (wsStatus === "reconnecting") { state = "warn"; text = "Reconnecting…"; }
      else if (wsStatus === "unauthorized") { state = "bad"; text = "Session ended"; }
      else if (role === "host") {
        const camOn = media.getState().camera.status === "on";
        state = camOn ? "live" : "idle"; text = camOn ? "On air" : "Camera off";
      } else if (role === "viewer") {
        if (!hostState.present) { state = "idle"; text = "Waiting for the host"; }
        else if (hostState.live) { state = "live"; text = "Live"; }
        else { state = "idle"; text = "Host is here"; }
      } else if (wsStatus === "open") { text = "Joining…"; }
      statusPill.dataset.state = state;
      statusText.textContent = text;
    }

    /* ---- viewer: the host's picture ---- */
    function renderHost() {
      if (role !== "viewer") return;
      const s = hostState;
      let title = "", text = "";
      if (!s.present) { title = "Waiting for the host"; text = "The group owner hasn't opened the room yet. Video starts by itself when they do."; }
      else if (s.link === "failed") { title = "Couldn't connect to the host"; text = "Your network may block direct video connections. Reload the page, or try another network."; }
      else if (s.link === "connected" && !s.live) { title = "The host's camera is off"; text = "You're connected. Video appears as soon as the host turns it on."; }
      else if (!s.live) { title = "Connecting to the host…"; }
      remoteCover.hidden = s.live;
      remoteWrap.classList.toggle("has-video", s.live);
      remoteTitle.textContent = title;
      remoteText.textContent = text;
      remoteText.hidden = !text;
    }

    /* ---- people ---- */
    const labelOf = (userId: string) => {
      if (userId === user.id) return "You";
      const e = roster.find((r) => r.userId === userId);
      return e?.role === "host" ? "Host" : "Member";
    };
    function renderPeople() {
      peopleList.replaceChildren();
      const list = [...roster].sort((a, b) => (a.connectionId === myConnId ? -1 : b.connectionId === myConnId ? 1 : a.role === "host" ? -1 : b.role === "host" ? 1 : 0));
      if (!list.length) {
        const li = el("li", "rm-person");
        li.append(el("span", "gx-avatar", initialsOf(myName)), el("span", "rm-person__body", `${myName} (you)`));
        peopleList.append(li);
      }
      for (const p of list) {
        const mine = p.connectionId === myConnId;
        const sameUser = p.userId === user.id;
        const li = el("li", "rm-person");
        const av = el("span", "gx-avatar", mine || sameUser ? initialsOf(myName) : p.role === "host" ? "H" : "M");
        av.setAttribute("aria-hidden", "true");
        const body = el("span", "rm-person__body");
        const name = el("span", "rm-person__name", mine ? `${myName} ` : sameUser ? `${myName} ` : p.role === "host" ? "Host " : "Member ");
        if (mine) name.append(el("span", "rm-person__you", "(you)"));
        else if (sameUser) name.append(el("span", "rm-person__you", "(your other tab)"));
        const status = el("span", "rm-person__status", p.role === "host" ? "Group owner · hosting" : "Member");
        if (mine) {
          const s = media.getState();
          const micOn = s.microphone.status === "on";
          status.textContent = p.role === "host"
            ? `Hosting · camera ${s.camera.status === "on" ? "on" : "off"} · mic ${micOn ? (s.microphone.muted ? "muted" : "on") : "off"}`
            : `Mic ${micOn ? (s.microphone.muted ? "muted" : "on") : "off"}`;
        }
        body.append(name, status);
        li.append(av, body);
        peopleList.append(li);
      }
      countEl.textContent = String(Math.max(1, roster.length));
    }

    /* ---- realtime connection + live stream ---- */
    const setConnectionStatus = (s: RealtimeStatus, detail?: string) => {
      wsStatus = s;
      const ui = CONNECTION_UI[s];
      connBadge.className = `gx-badge ${ui.cls}`.trim();
      connBadge.textContent = ui.label;
      connBadge.title = detail ?? "";
      connNote.hidden = s !== "unauthorized";
      if (ui.announce) announce(`Connection: ${ui.label.replace("…", "")}`);
      if (s === "reconnecting" || s === "unauthorized") {
        joiningTitle.textContent = s === "unauthorized" ? "Your session has ended" : "Reconnecting…";
      }
      renderStatus();
    };
    const realtime = new RealtimeClient();
    const offStatus = realtime.onStatus(setConnectionStatus);
    realtime.start();

    const stream = initStream({
      realtime, media, roomId, groupId, remoteVideo, audioSink,
      onRole: setRole,
      onRoster: (r) => { roster = r.participants; myConnId = r.myConnId; renderPeople(); },
      onHostVideo: (s) => { hostState = s; renderHost(); renderStatus(); },
      onPlaybackBlocked: (blocked) => { playBtn.hidden = !blocked; if (blocked) announce("Press the button to start video and sound."); },
      onProblem: (msg) => {
        joiningTitle.textContent = "Couldn't join the live room";
        joiningText.textContent = msg;
        problemBox.replaceChildren(Object.assign(el("div", "notice notice--error rm-error"), { textContent: msg }));
      },
    });
    playBtn.addEventListener("click", () => stream.resumePlayback());

    const chatLog = $("[data-rm-chat-log]");
    const chat = initChat(realtime, roomId, user.id, chatLog, $<HTMLFormElement>("[data-rm-chat-form]"), $<HTMLTextAreaElement>("[data-rm-chat-input]"), labelOf);
    const notes = initRoomNotes(root, { groupId, roomName: room.name });

    // The Ask AI form is inert on purpose.
    root.querySelector("[data-rm-ai-form]")?.addEventListener("submit", (e) => e.preventDefault());

    let torn = false;
    const teardown = () => {
      if (torn) return;
      torn = true;
      window.clearInterval(clockTimer);
      notes.flush();
      chat.destroy();
      stream.leave();
      offStatus(); // unsubscribe first so leaving doesn't announce "Disconnected"
      realtime.stop();
    };

    /* ---- microphone level meter ---- */
    let meter: LevelMeter | null = null;
    const stopMeter = () => { meter?.stop(); meter = null; meterBar.style.transform = "scaleX(0)"; };
    const startMeter = () => {
      stopMeter();
      meter = startLevelMeter(media.getStream(), (lvl) => { meterBar.style.transform = `scaleX(${lvl.toFixed(3)})`; });
    };

    /* ---- rendering local media ---- */
    let prev: LocalMediaState | null = null;
    const lastErr = new Map<string, MediaFailure | undefined>();

    function renderFailure(key: "camera" | "microphone", box: HTMLElement, f?: MediaFailure) {
      if (lastErr.get(key) === f) return;
      lastErr.set(key, f);
      box.replaceChildren();
      if (!f) return;
      const n = el("div", "notice notice--error rm-error");
      n.append(el("h3", "", f.title), el("p", "", f.message));
      if (f.steps.length) { const ul = el("ul"); f.steps.forEach((s) => ul.append(el("li", "", s))); n.append(ul); }
      box.append(n);
      announce(f.title);
    }

    function startLabel(k: KindState, idle: string, noun: string) {
      if (k.status === "starting") return { text: `Starting ${noun}…`, disabled: true };
      if (k.status === "error" && k.error && !k.error.retryable) return { text: `${noun[0].toUpperCase()}${noun.slice(1)} unavailable`, disabled: true };
      if (k.status === "error") return { text: "Try again", disabled: false };
      return { text: idle, disabled: false };
    }

    function renderDevices(list: DeviceList, s: LocalMediaState) {
      const fill = (sel: HTMLSelectElement, items: DeviceList["cameras"], current?: string) => {
        const sig = JSON.stringify(items);
        if (sel.dataset.sig !== sig) {
          sel.dataset.sig = sig;
          sel.replaceChildren(...items.map((d) => { const o = el("option", "", d.label); o.value = d.deviceId; return o; }));
        }
        if (current) sel.value = current;
      };
      fill(camSelect, list.cameras, s.camera.deviceId);
      fill(micSelect, list.microphones, s.microphone.deviceId);
      const showCam = role === "host" && s.camera.status === "on" && list.cameras.length > 0;
      const showMic = s.microphone.status === "on" && list.microphones.length > 0;
      camPick.hidden = !showCam;
      micPick.hidden = !showMic;
      devicesHint.hidden = showCam || showMic;
    }

    function render(s: LocalMediaState) {
      const { camera: cam, microphone: mic } = s;
      const camOn = cam.status === "on";
      const micOn = mic.status === "on";

      // Host self-view
      if (camOn && prev?.camera.status !== "on") {
        video.srcObject = media.getStream();
        void video.play().catch(() => {});
      }
      if (!camOn) video.srcObject = null;
      tile.classList.toggle("has-video", camOn);
      cover.hidden = camOn;
      onAir.hidden = !camOn;
      tileMuted.hidden = !(micOn && mic.muted);

      // Start buttons
      const cl = startLabel(cam, "Turn on camera", "camera");
      camStartLabel.textContent = cl.text;
      camStartBtn.disabled = cl.disabled;
      const ml = startLabel(mic, "Turn on microphone", "microphone");
      micStartBtns.forEach((b) => {
        b.querySelector("[data-rm-mic-start-label]")!.textContent = ml.text;
        b.disabled = ml.disabled;
        if (b.hasAttribute("data-rm-mic-in-cover")) b.hidden = micOn;
      });
      micRow.hidden = micOn || (role === "host" && !camOn); // the host's cover already has a mic button

      renderFailure("camera", camErrBox, cam.error);
      renderFailure("microphone", micErrBox, mic.error);
      meterBox.hidden = !micOn;
      micState.textContent = mic.status === "starting" ? "Starting…" : !micOn ? "Off" : mic.muted ? "Muted" : "On";
      if (micOn && prev?.microphone.status !== "on") startMeter();
      if (!micOn && meter) stopMeter();

      // Control bar
      barCam.dataset.state = cam.status;
      barCamState.textContent = { off: "Off", starting: "Starting…", on: "On", error: "Problem" }[cam.status];
      barCam.setAttribute("aria-label", camOn ? "Turn off camera" : cam.status === "starting" ? "Camera is starting" : "Turn on camera");
      const micBarState = micOn ? (mic.muted ? "muted" : "on") : mic.status;
      barMic.dataset.state = micBarState;
      barMicState.textContent = { off: "Off", starting: "Starting…", on: "On", muted: "Muted", error: "Problem" }[micBarState]!;
      barMic.setAttribute("aria-label", micOn ? (mic.muted ? "Unmute microphone" : "Mute microphone") : mic.status === "starting" ? "Microphone is starting" : "Turn on microphone");

      renderDevices(media.getDevices(), s);
      renderStatus();
      renderPeople();

      if (prev) {
        if (prev.camera.status !== "on" && camOn) announce("Camera on. Everyone in the room can see you.");
        if (prev.camera.status === "on" && !camOn && !cam.error) announce("Camera off");
        if (prev.microphone.status !== "on" && micOn) announce("Microphone on");
        if (prev.microphone.status === "on" && micOn && prev.microphone.muted !== mic.muted) announce(mic.muted ? "Microphone muted" : "Microphone unmuted");
        if (prev.microphone.status === "on" && !micOn && !mic.error) announce("Microphone off");
      }
      prev = s;
    }

    media.subscribe(render);
    media.subscribeDevices(() => renderDevices(media.getDevices(), media.getState()));
    render(media.getState());

    /* ---- actions (every getUserMedia call starts from a click: a user gesture) ---- */
    const startCamera = () => { if (role === "host" && media.getState().camera.status !== "starting") void media.startCamera(camSelect.value || undefined); };
    const startMic = () => { if (media.getState().microphone.status !== "starting") void media.startMicrophone(micSelect.value || undefined); };

    camStartBtn.addEventListener("click", startCamera);
    micStartBtns.forEach((b) => b.addEventListener("click", startMic));

    barCam.addEventListener("click", () => {
      const st = media.getState().camera.status;
      if (st === "on") media.stopCamera(); // really releases the camera: the light goes out
      else if (st !== "starting") startCamera();
    });
    barMic.addEventListener("click", () => {
      const m = media.getState().microphone;
      if (m.status === "on") media.setMicrophoneMuted(!m.muted);
      else if (m.status !== "starting") startMic();
    });

    camSelect.addEventListener("change", () => { void media.startCamera(camSelect.value); });
    micSelect.addEventListener("change", async () => {
      const wasMuted = media.getState().microphone.muted;
      const ok = await media.startMicrophone(micSelect.value);
      if (ok && wasMuted) media.setMicrophoneMuted(true);
      if (ok) startMeter();
    });

    /* ---- side panel: tabs + collapse ---- */
    const tabs = Array.from(root.querySelectorAll<HTMLButtonElement>("[data-rm-tab]"));
    const panes = Array.from(root.querySelectorAll<HTMLElement>("[data-rm-pane]"));
    function selectTab(name: string, focus = false) {
      tabs.forEach((t) => { const on = t.dataset.rmTab === name; t.setAttribute("aria-selected", String(on)); t.tabIndex = on ? 0 : -1; if (on && focus) t.focus(); });
      panes.forEach((p) => { p.hidden = p.dataset.rmPane !== name; });
    }
    tabs.forEach((t, i) => {
      t.addEventListener("click", () => selectTab(t.dataset.rmTab!));
      t.addEventListener("keydown", (e) => {
        const next = e.key === "ArrowRight" ? (i + 1) % tabs.length : e.key === "ArrowLeft" ? (i - 1 + tabs.length) % tabs.length : e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : -1;
        if (next < 0) return;
        e.preventDefault();
        selectTab(tabs[next].dataset.rmTab!, true);
      });
    });

    function setSide(open: boolean, focus?: "tab" | "toggle") {
      side.hidden = !open;
      layout.classList.toggle("is-side-hidden", !open);
      sideToggle.setAttribute("aria-expanded", String(open));
      sideToggle.setAttribute("aria-label", open ? "Hide side panel" : "Show side panel");
      if (focus === "tab") tabs.find((t) => t.getAttribute("aria-selected") === "true")?.focus();
      if (focus === "toggle") sideToggle.focus();
    }
    setSide(window.matchMedia("(min-width: 62rem)").matches);
    sideToggle.addEventListener("click", () => { const open = side.hidden; setSide(open, open ? "tab" : undefined); });
    $("[data-rm-side-close]").addEventListener("click", () => setSide(false, "toggle"));

    /* ---- if the server never answers the join, say so instead of spinning forever ---- */
    window.setTimeout(() => {
      if (role === "pending" && wsStatus === "open" && !problemBox.hasChildNodes()) {
        joiningTitle.textContent = "Still joining…";
        joiningText.textContent = "This is taking longer than usual. Reload the page if it doesn't start.";
      }
    }, 8000);

    /* ---- letting go of the camera: leave, page hide, navigation ---- */
    $("[data-rm-leave]").addEventListener("click", () => { media.stopAll(); teardown(); }); // the link then navigates
    window.addEventListener("pagehide", (e) => {
      media.stopAll(); // covers navigation, reload, tab close and back/forward cache
      teardown();
      if (!e.persisted) { stopMeter(); media.destroy(); }
    });
    window.addEventListener("pageshow", (e) => { if (e.persisted) location.reload(); });
    window.addEventListener("beforeunload", () => media.stopAll());
  }

  load();
}
