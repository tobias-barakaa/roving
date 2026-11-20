/**
 * LocalMedia: the person's own camera and microphone.
 *
 * Owns getUserMedia, device listing, track start/stop and error mapping.
 * No DOM code (no elements, no document), so the later WebRTC stage can reuse
 * it: hand `getStream()` / the individual tracks to a peer connection and
 * listen with `subscribe()` to swap tracks when the person changes device.
 *
 * Camera and microphone are requested SEPARATELY so that refusing one never
 * blocks the other.
 *
 *  - Camera "off" really stops the video track (releases the device, so the
 *    camera light goes out) and "on" asks the browser again.
 *  - Microphone "mute" keeps the track open but sets track.enabled = false.
 *
 * Not related to: the persistent room, a focus session, an active call or the
 * WebSocket connection. This only manages local tracks.
 */

export type MediaKind = "camera" | "microphone";
export type DeviceStatus = "off" | "starting" | "on" | "error";

export type MediaFailureKind =
  | "unsupported"
  | "insecure"
  | "denied"
  | "not-found"
  | "busy"
  | "overconstrained"
  | "unplugged"
  | "unknown";

export interface MediaFailure {
  kind: MediaFailureKind;
  title: string;
  message: string;
  /** Plain-language recovery steps, shown as a list. */
  steps: string[];
  /** True when pressing "Try again" can plausibly work. */
  retryable: boolean;
}

export interface KindState {
  status: DeviceStatus;
  /** Microphone only: true while muted. Always false for the camera. */
  muted: boolean;
  deviceId?: string;
  error?: MediaFailure;
}

export interface LocalMediaState {
  camera: KindState;
  microphone: KindState;
}

export interface DeviceInfo { deviceId: string; label: string; }
export interface DeviceList { cameras: DeviceInfo[]; microphones: DeviceInfo[]; }

type Listener = (state: LocalMediaState) => void;
type DeviceListener = (devices: DeviceList) => void;

/* ---------------------------------------------------------------- support */

export function isSecureEnough(): boolean {
  return typeof window !== "undefined" ? window.isSecureContext : false;
}

export function isMediaSupported(): boolean {
  return typeof navigator !== "undefined" && !!navigator.mediaDevices && typeof navigator.mediaDevices.getUserMedia === "function";
}

/* ---------------------------------------------------------- error mapping */

const what = (k: MediaKind) => (k === "camera" ? "camera" : "microphone");

const REENABLE_STEPS = [
  "Chrome or Edge: click the lock (or tune) icon left of the address bar, set Camera and Microphone to Allow, then reload.",
  "Firefox: click the permissions icon left of the address bar, remove the blocked camera or microphone entry, then try again.",
  "Safari: open Safari ▸ Settings for This Website and set Camera and Microphone to Allow.",
  "On a phone, also check that your browser is allowed to use the camera and microphone in the system Settings.",
];

export function mapMediaError(err: unknown, kind: MediaKind): MediaFailure {
  const n = what(kind);
  const name = (err as { name?: string } | null)?.name ?? "";

  switch (name) {
    case "NotAllowedError":
    case "PermissionDeniedError":
    case "SecurityError":
      return {
        kind: "denied",
        title: `Rovingr can't use your ${n}`,
        message: `Access to your ${n} was blocked. This happens if you chose "Block", or the browser or device blocks it for this site.`,
        steps: REENABLE_STEPS,
        retryable: true,
      };
    case "NotFoundError":
    case "DevicesNotFoundError":
      return {
        kind: "not-found",
        title: `No ${n} found`,
        message: `We couldn't find a ${n} on this device.`,
        steps: [`Plug in or switch on a ${n}, then press Try again.`, "You can still stay in the room without it."],
        retryable: true,
      };
    case "NotReadableError":
    case "TrackStartError":
    case "AbortError":
      return {
        kind: "busy",
        title: `Your ${n} is busy`,
        message: `Another app, window or browser tab may be using your ${n}, or it failed to start.`,
        steps: [`Close other apps or tabs that use your ${n} (video calls, recorders), then press Try again.`, "If that doesn't help, unplug and replug it, or restart the browser."],
        retryable: true,
      };
    case "OverconstrainedError":
    case "ConstraintNotSatisfiedError":
      return {
        kind: "overconstrained",
        title: `That ${n} isn't available`,
        message: `The ${n} you picked can't be used right now.`,
        steps: [`Choose a different ${n} from the list, or press Try again.`],
        retryable: true,
      };
    default:
      return {
        kind: "unknown",
        title: `Couldn't start your ${n}`,
        message: `Something unexpected happened while starting your ${n}.`,
        steps: ["Press Try again. If it keeps happening, reload the page."],
        retryable: true,
      };
  }
}

function unsupportedFailure(kind: MediaKind): MediaFailure {
  const n = what(kind);
  if (!isSecureEnough()) {
    return {
      kind: "insecure",
      title: `Your ${n} needs a secure connection`,
      message: "Browsers only allow camera and microphone access on https:// pages (or on localhost).",
      steps: ["Open Rovingr using an https:// address, or use http://localhost during development."],
      retryable: false,
    };
  }
  return {
    kind: "unsupported",
    title: "This browser can't use a camera or microphone",
    message: "It doesn't support the browser feature that Rovingr rooms need.",
    steps: ["Try a recent version of Chrome, Edge, Firefox or Safari.", "You can still stay in the room and read the side panel."],
    retryable: false,
  };
}

/* ------------------------------------------------------------------ class */

const idle = (): KindState => ({ status: "off", muted: false });

export class LocalMedia {
  private stream: MediaStream = typeof MediaStream !== "undefined" ? new MediaStream() : (undefined as unknown as MediaStream);
  private state: LocalMediaState = { camera: idle(), microphone: idle() };
  private listeners = new Set<Listener>();
  private deviceListeners = new Set<DeviceListener>();
  /** Bumped on every start/stop so a slow getUserMedia can't resurrect a stopped track. */
  private tokens: Record<MediaKind, number> = { camera: 0, microphone: 0 };
  private devices: DeviceList = { cameras: [], microphones: [] };
  private onDeviceChange = () => { void this.refreshDevices(); };

  constructor() {
    if (isMediaSupported()) navigator.mediaDevices.addEventListener("devicechange", this.onDeviceChange);
  }

  /* ---- reading ---- */
  getState(): LocalMediaState { return this.state; }
  /** One persistent stream: tracks are added/removed, so a <video srcObject> never needs re-assigning. */
  getStream(): MediaStream { return this.stream; }
  getTrack(kind: MediaKind): MediaStreamTrack | undefined {
    return (kind === "camera" ? this.stream.getVideoTracks() : this.stream.getAudioTracks())[0];
  }
  getDevices(): DeviceList { return this.devices; }

  subscribe(fn: Listener): () => void { this.listeners.add(fn); return () => this.listeners.delete(fn); }
  subscribeDevices(fn: DeviceListener): () => void { this.deviceListeners.add(fn); return () => this.deviceListeners.delete(fn); }

  /* ---- starting ---- */
  startCamera(deviceId?: string) { return this.start("camera", deviceId); }
  startMicrophone(deviceId?: string) { return this.start("microphone", deviceId); }

  private async start(kind: MediaKind, deviceId?: string): Promise<boolean> {
    const token = ++this.tokens[kind];
    if (!isMediaSupported()) {
      this.patch(kind, { status: "error", error: unsupportedFailure(kind) });
      return false;
    }
    // Switching device while already on: keep showing the live track instead of flashing "starting".
    if (this.getTrack(kind)) this.patch(kind, { error: undefined });
    else this.patch(kind, { status: "starting", error: undefined });

    const base: MediaTrackConstraints = kind === "camera"
      ? { width: { ideal: 1280 }, height: { ideal: 720 }, facingMode: "user" }
      : { echoCancellation: true, noiseSuppression: true, autoGainControl: true };
    const ask = (exact?: string) => {
      const c: MediaTrackConstraints = exact ? { ...base, deviceId: { exact } } : base;
      return navigator.mediaDevices.getUserMedia(kind === "camera" ? { video: c } : { audio: c });
    };

    let got: MediaStream;
    try {
      try {
        got = await ask(deviceId);
      } catch (e) {
        // A remembered/picked device that's gone: fall back to the default one once.
        if (deviceId && e instanceof DOMException && (e.name === "OverconstrainedError" || e.name === "NotFoundError")) got = await ask();
        else throw e;
      }
    } catch (err) {
      // If an older track is still running (failed device switch), it stays "on" and only the error is shown.
      if (token === this.tokens[kind]) this.patch(kind, { status: this.getTrack(kind) ? "on" : "error", error: mapMediaError(err, kind) });
      return false;
    }

    const track = (kind === "camera" ? got.getVideoTracks() : got.getAudioTracks())[0];
    if (!track || token !== this.tokens[kind]) {
      // Stopped (or superseded) while the permission prompt was open: release immediately.
      got.getTracks().forEach((t) => t.stop());
      if (!track && token === this.tokens[kind]) this.patch(kind, { status: "error", error: mapMediaError({ name: "NotFoundError" }, kind) });
      return false;
    }

    this.removeTrack(kind); // replacing a device
    this.stream.addTrack(track);
    track.addEventListener("ended", () => {
      // Unplugged, or revoked from the browser's own controls.
      if (this.getTrack(kind) !== track) return;
      this.stream.removeTrack(track);
      this.patch(kind, {
        status: "error", muted: false, deviceId: undefined,
        error: {
          kind: "unplugged",
          title: `Your ${what(kind)} stopped`,
          message: `Your ${what(kind)} was disconnected, or access was turned off in the browser.`,
          steps: [`Check it is connected, then press Try again.`],
          retryable: true,
        },
      });
    });

    const settings = track.getSettings();
    this.patch(kind, { status: "on", muted: false, deviceId: settings.deviceId ?? deviceId, error: undefined });
    void this.refreshDevices(); // labels are only available after permission is granted
    return true;
  }

  /* ---- stopping / muting ---- */
  stopCamera() { this.stop("camera"); }
  stopMicrophone() { this.stop("microphone"); }

  private stop(kind: MediaKind) {
    this.tokens[kind]++;
    this.removeTrack(kind);
    this.patch(kind, { status: "off", muted: false, deviceId: undefined, error: undefined });
  }

  /** Stops every track. Safe to call repeatedly (leave, pagehide, navigation). */
  stopAll() { this.stop("camera"); this.stop("microphone"); }

  setMicrophoneMuted(muted: boolean) {
    const t = this.getTrack("microphone");
    if (!t) return;
    t.enabled = !muted;
    this.patch("microphone", { muted });
  }

  private removeTrack(kind: MediaKind) {
    const t = this.getTrack(kind);
    if (!t) return;
    t.stop();
    this.stream.removeTrack(t);
  }

  /* ---- devices ---- */
  async refreshDevices(): Promise<DeviceList> {
    if (!isMediaSupported() || !navigator.mediaDevices.enumerateDevices) return this.devices;
    try {
      const all = await navigator.mediaDevices.enumerateDevices();
      const pick = (k: MediaDeviceKind, fallback: string): DeviceInfo[] =>
        all.filter((d) => d.kind === k && d.deviceId).map((d, i) => ({ deviceId: d.deviceId, label: d.label || `${fallback} ${i + 1}` }));
      this.devices = { cameras: pick("videoinput", "Camera"), microphones: pick("audioinput", "Microphone") };
      this.deviceListeners.forEach((fn) => fn(this.devices));
    } catch { /* listing is a nicety; ignore */ }
    return this.devices;
  }

  /** Release everything and stop listening. Call once when the page is torn down. */
  destroy() {
    this.stopAll();
    if (isMediaSupported()) navigator.mediaDevices.removeEventListener("devicechange", this.onDeviceChange);
    this.listeners.clear();
    this.deviceListeners.clear();
  }

  private patch(kind: MediaKind, p: Partial<KindState>) {
    this.state = { ...this.state, [kind]: { ...this.state[kind], ...p } };
    this.listeners.forEach((fn) => fn(this.state));
  }
}