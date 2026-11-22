/**
 * Room page controller. Loads the room, decides which honest state to show, and runs the local
 * camera/microphone experience.
 *
 * Four different things, kept apart on purpose (code and wording):
 *   1. the persistent ROOM            -> `Room` from lib/api/rooms.ts (a place that exists)
 *   2. a FOCUS SESSION                -> not built; never mentioned in this page
 *   3. an active CALL                 -> not built (later: peer-to-peer WebRTC)
 *   4. the WebSocket CONNECTION       -> ConnectionStatus below (UI only; see startPresence)
 *
 * Authorization is the backend's. This file only reads `canEnter` to decide what to show.
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
import { initStream } from "../stream/streamPage";
import { initChat } from "../stream/chat";


/* ------------------------------------------------------------ small DOM helpers */

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text?: string) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

const initialsOf = (name: string) =>
  name.trim().split(/\s+/).slice(0, 2).map((w) => w.charAt(0).toUpperCase()).join("") || "?";

/* ------------------------------------------------------------ connection status (UI) */

/** What the People tab shows about the realtime (WebSocket) connection: RealtimeStatus from lib/realtime/types.ts. */
const CONNECTION_UI: Record<RealtimeStatus, { label: string; cls: string; announce: boolean }> = {
  idle: { label: "Not connected yet", cls: "gx-badge--muted", announce: false },
  connecting: { label: "Connecting…", cls: "", announce: false },
  open: { label: "Connected", cls: "gx-badge--ok", announce: true },
  reconnecting: { label: "Reconnecting…", cls: "rm-badge--warn", announce: true },
  closed: { label: "Disconnected", cls: "gx-badge--muted", announce: true },
  unauthorized: { label: "Session ended", cls: "rm-badge--bad", announce: true },
};

interface PresenceContext {
  groupId: string;
  roomId: string;
  user: User;
  /** The tab's realtime connection (stage 1). Already started; its status drives the badge. */
  realtime: RealtimeClient;
}

/**
 * EXTENSION POINT: presence (next stage). Intentionally empty.
 *
 * ctx.realtime is a live RealtimeClient, so this is where to:
 *   1. when realtime.status is "open" (use realtime.onStatus; it also fires after every reconnect),
 *      send `room.join` for ctx.roomId with realtime.send(...). send() returns null while the socket
 *      isn't open and queues nothing, so re-send on each "open";
 *   2. subscribe with realtime.on("room.state", ...) and render people into [data-rm-people-list]
 *      (me stays first);
 *   3. return a function that sends the leave message and unsubscribes.
 * The persistent room, the realtime connection and "being present" are three separate things.
 */
function startPresence(_ctx: PresenceContext): () => void {
  return () => {};
}

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
        showProblem({
          title: "We couldn't open this room",
          text: "The link may be wrong, the room may have been removed, or you may not have access to this group.",
        });
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
      // No camera controls for someone who can't enter.
      enterBox.hidden = true;
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
      enterRoom(user, room.myRoomRole === "host" ? "host" : "viewer");
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
    if (room.myRoomRole === "host") badges.append(el("span", "gx-badge", "You host this room"));
    if (typeof room.capacity === "number") badges.append(el("span", "gx-badge gx-badge--muted", `Up to ${room.capacity} people`));
    if (room.archivedAt) badges.append(el("span", "gx-badge gx-badge--muted", "Archived"));
  }

  /* ----------------------------------------------------------- the room itself */

  // function enterRoom(user: User) {
    function enterRoom(user: User, myRoomRole: "host" | "viewer") {
    const media = new LocalMedia();
    const myName = displayNameOf(user);

    // Stage
    const tile = $("[data-rm-self]");
    const video = $<HTMLVideoElement>("[data-rm-video]");
    const cover = $("[data-rm-cover]");
    const camStartBtn = $<HTMLButtonElement>("[data-rm-cam-start]");
    const camStartLabel = $("[data-rm-cam-start-label]");
    const micStartBtns = Array.from(root.querySelectorAll<HTMLButtonElement>("[data-rm-mic-start]"));
    const micRow = $("[data-rm-mic-row]");
    const tileMuted = $("[data-rm-tile-muted]");
    const camErrBox = $("[data-rm-cam-error]");
    const micErrBox = $("[data-rm-mic-error]");
    const meterBox = $("[data-rm-meterbox]");
    const meterBar = $("[data-rm-meter-bar]");
    const micState = $("[data-rm-mic-state]");
    const camPick = $("[data-rm-cam-pick]");
    const micPick = $("[data-rm-mic-pick]");
    const camSelect = $<HTMLSelectElement>("[data-rm-cam-select]");
    const micSelect = $<HTMLSelectElement>("[data-rm-mic-select]");
    const devicesHint = $("[data-rm-devices-hint]");

    // Control bar
    const barCam = $<HTMLButtonElement>("[data-rm-bar-cam]");
    const barMic = $<HTMLButtonElement>("[data-rm-bar-mic]");
    const barCamState = $("[data-rm-bar-cam-state]");
    const barMicState = $("[data-rm-bar-mic-state]");
    const sideToggle = $<HTMLButtonElement>("[data-rm-side-toggle]");

    // Side panel
    const side = $("[data-rm-side]");
    const layout = $("[data-rm-layout]");
    const meStatus = $("[data-rm-me-status]");
    const connBadge = $("[data-rm-conn]");

    root.querySelectorAll("[data-rm-initials], [data-rm-me-initials]").forEach((n) => (n.textContent = initialsOf(myName)));
    $("[data-rm-me-name]").textContent = myName;

    /* ---- realtime connection (stage 1 RealtimeClient). Status only; presence is a later stage. ---- */
    const connNote = $("[data-rm-conn-note]");
    const setConnectionStatus = (s: RealtimeStatus, detail?: string) => {
      const ui = CONNECTION_UI[s];
      connBadge.className = `gx-badge ${ui.cls}`.trim();
      connBadge.textContent = ui.label;
      connBadge.title = detail ?? "";
      connNote.hidden = s !== "unauthorized";
      if (ui.announce) announce(`Realtime connection: ${ui.label.replace("…", "")}`);
    };
    // const realtime = new RealtimeClient();
    // const offStatus = realtime.onStatus(setConnectionStatus);
    // realtime.start();
    // const stopPresence = startPresence({ groupId, roomId, user, realtime });
    // let realtimeStopped = false;
    // const stopRealtime = () => {
    //   if (realtimeStopped) return;
    //   realtimeStopped = true;
    //   stopPresence();
    //   offStatus(); // unsubscribe first so leaving doesn't announce "Disconnected"
    //   realtime.stop();
    // };
    // const realtime = new RealtimeClient();
    // const offStatus = realtime.onStatus(setConnectionStatus);
    // realtime.start();
    // const stopPresence = startPresence({ groupId, roomId, user, realtime });
    // let realtimeStopped = false;
    // const stopRealtime = () => {
    //   if (realtimeStopped) return;
    //   realtimeStopped = true;
    //   stopPresence();
    //   offStatus();
    //   realtime.stop();
    // };

    const realtime = new RealtimeClient();
    const offStatus = realtime.onStatus(setConnectionStatus);
    realtime.start();
    
    // Stream: join the room's WebRTC stream.
    const remoteVideoEl = root.querySelector<HTMLVideoElement>("[data-rm-remote-video]");
    const streamHandle = remoteVideoEl
      ? initStream(realtime, media, roomId, groupId, myRoomRole, remoteVideoEl)
      : null;
    
    // Chat: send and receive messages through the same realtime socket.
    const chatLog = root.querySelector<HTMLElement>("[data-rm-chat-log]");
    const chatForm = root.querySelector<HTMLFormElement>("[data-rm-chat-form]");
    const chatInput = root.querySelector<HTMLTextAreaElement>("[data-rm-chat-input]");
    const chat = (chatLog && chatForm && chatInput)
      ? initChat(realtime, roomId, user.id, chatLog, chatForm, chatInput)
      : null;
    
    let realtimeStopped = false;
    const stopRealtime = () => {
      if (realtimeStopped) return;
      realtimeStopped = true;
      chat?.destroy();
      streamHandle?.leave();
      offStatus();
      realtime.stop();
    };
      
    /* ---- microphone level meter ---- */
    let meter: LevelMeter | null = null;
    const stopMeter = () => { meter?.stop(); meter = null; meterBar.style.transform = "scaleX(0)"; };
    const startMeter = () => {
      stopMeter();
      meter = startLevelMeter(media.getStream(), (lvl) => { meterBar.style.transform = `scaleX(${lvl.toFixed(3)})`; });
    };

    /* ---- rendering ---- */
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
      const showCam = s.camera.status === "on" && list.cameras.length > 0;
      const showMic = s.microphone.status === "on" && list.microphones.length > 0;
      camPick.hidden = !showCam;
      micPick.hidden = !showMic;
      devicesHint.hidden = showCam || showMic;
    }

    function render(s: LocalMediaState) {
      const { camera: cam, microphone: mic } = s;
      const camOn = cam.status === "on";
      const micOn = mic.status === "on";

      // Self-view
      if (camOn && prev?.camera.status !== "on") {
        video.srcObject = media.getStream();
        void video.play().catch(() => {});
      }
      if (!camOn) video.srcObject = null;
      tile.classList.toggle("has-video", camOn);
      cover.hidden = camOn;
      tileMuted.hidden = !(micOn && mic.muted);

      // Start buttons
      const cl = startLabel(cam, "Turn on camera", "camera");
      camStartLabel.textContent = cl.text;
      camStartBtn.disabled = cl.disabled;
      const ml = startLabel(mic, "Turn on microphone", "microphone");
      micStartBtns.forEach((b) => {
        b.querySelector("[data-rm-mic-start-label]")!.textContent = ml.text;
        b.disabled = ml.disabled;
        // The cover shows its own mic button while the camera is off; the row takes over once the camera is on.
        if (b.hasAttribute("data-rm-mic-in-cover")) b.hidden = micOn;
      });
      micRow.hidden = !(camOn && !micOn);

      // Errors, meter, mic state text
      renderFailure("camera", camErrBox, cam.error);
      renderFailure("microphone", micErrBox, mic.error);
      meterBox.hidden = !micOn;
      micState.textContent = mic.status === "starting" ? "Starting…" : !micOn ? "Off" : mic.muted ? "Muted" : "On";
      if (micOn && prev?.microphone.status !== "on") startMeter();
      if (!micOn && meter) stopMeter();

      // Control bar
      const camBarState = cam.status; // off | starting | on | error
      barCam.dataset.state = camBarState;
      barCamState.textContent = { off: "Off", starting: "Starting…", on: "On", error: "Problem" }[camBarState];
      barCam.setAttribute("aria-label", camOn ? "Turn off camera" : cam.status === "starting" ? "Camera is starting" : "Turn on camera");
      const micBarState = micOn ? (mic.muted ? "muted" : "on") : mic.status;
      barMic.dataset.state = micBarState;
      barMicState.textContent = { off: "Off", starting: "Starting…", on: "On", muted: "Muted", error: "Problem" }[micBarState]!;
      barMic.setAttribute("aria-label", micOn ? (mic.muted ? "Unmute microphone" : "Mute microphone") : mic.status === "starting" ? "Microphone is starting" : "Turn on microphone");

      // People tab: me
      meStatus.textContent = `Camera ${camOn ? "on" : "off"} · Microphone ${micOn ? (mic.muted ? "muted" : "on") : "off"}`;

      renderDevices(media.getDevices(), s);

      // Screen-reader announcements for real transitions only
      if (prev) {
        if (prev.camera.status !== "on" && camOn) announce("Camera on");
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
    const startCamera = () => { if (media.getState().camera.status !== "starting") void media.startCamera(camSelect.value || undefined); };
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
      if (ok) startMeter(); // the track changed, so the analyser must follow it
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
      sideToggle.setAttribute("aria-label", open ? "Hide People & chat panel" : "Show People & chat panel");
      if (focus === "tab") tabs.find((t) => t.getAttribute("aria-selected") === "true")?.focus();
      if (focus === "toggle") sideToggle.focus();
    }
    // Side by side on desktop (open), stacked on mobile (starts collapsed so the stage and controls stay in reach).
    setSide(window.matchMedia("(min-width: 62rem)").matches);
    sideToggle.addEventListener("click", () => { const open = side.hidden; setSide(open, open ? "tab" : undefined); });
    $("[data-rm-side-close]").addEventListener("click", () => setSide(false, "toggle"));

    /* ---- chat composer is inert for now ---- */
    // EXTENSION POINT: persistent chat. Load history into [data-rm-chat-log], enable the
    // <fieldset> in the form and send through the chat API. Until then nothing is sent.
    // $("[data-rm-chat-form]").addEventListener("submit", (e) => e.preventDefault());

    // EXTENSION POINT: WebRTC signalling (later stage).
    // The call will be peer-to-peer. Reuse `media.getStream()` / `media.getTrack("camera" | "microphone")`
    // for the outgoing tracks, listen with `media.subscribe()` to replaceTrack() when the device changes,
    // and add one tile per remote peer inside [data-rm-remote-grid]. Signalling goes over the realtime hub.
    // Don't start a call from this page load: a call is separate from the room existing.

    /* ---- letting go of the camera: leave, page hide, navigation ---- */
    $("[data-rm-leave]").addEventListener("click", () => { media.stopAll(); stopRealtime(); }); // the link then navigates
    window.addEventListener("pagehide", (e) => {
      media.stopAll(); // also covers navigation, reload, tab close and back/forward cache
      stopRealtime();
      if (!e.persisted) { stopMeter(); media.destroy(); }
    });
    // Restored from the back/forward cache: camera and connection were released, so start from a clean page.
    window.addEventListener("pageshow", (e) => { if (e.persisted) location.reload(); });
    window.addEventListener("beforeunload", () => media.stopAll());
  }

  load();
}

