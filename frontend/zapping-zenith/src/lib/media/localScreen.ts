// frontend/src/lib/media/localScreen.ts
// The host's screen share. No DOM. Same shape as LocalMedia: one persistent stream, tracks come and go.

export type ScreenStatus = "off" | "starting" | "on" | "error";
export interface ScreenState { status: ScreenStatus; error?: string }
type Listener = (s: ScreenState) => void;

export class LocalScreen {
  private stream: MediaStream = typeof MediaStream !== "undefined" ? new MediaStream() : (undefined as unknown as MediaStream);
  private state: ScreenState = { status: "off" };
  private listeners = new Set<Listener>();
  private token = 0;

  getState() { return this.state; }
  getStream() { return this.stream; }
  getTrack(): MediaStreamTrack | undefined { return this.stream.getVideoTracks()[0]; }
  subscribe(fn: Listener) { this.listeners.add(fn); return () => this.listeners.delete(fn); }

  async start(): Promise<boolean> {
    if (typeof navigator === "undefined" || !navigator.mediaDevices?.getDisplayMedia) {
      this.patch({ status: "error", error: "This browser can't share a screen. Try Chrome, Edge or Firefox on a computer." });
      return false;
    }
    const mine = ++this.token;
    this.patch({ status: "starting", error: undefined });
    let got: MediaStream;
    try {
      // Capped on purpose: the host uploads one copy per viewer.
      got = await navigator.mediaDevices.getDisplayMedia({
        video: { frameRate: { ideal: 15, max: 20 }, width: { max: 1920 }, height: { max: 1080 } },
        audio: false,
      });
    } catch (err) {
      if (mine !== this.token) return false;
      const name = (err as { name?: string } | null)?.name;
      // Closing the picker is a choice, not a failure.
      if (name === "NotAllowedError" || name === "AbortError") this.patch({ status: "off", error: undefined });
      else this.patch({ status: "error", error: "Couldn't start screen sharing. Try again." });
      return false;
    }
    const track = got.getVideoTracks()[0];
    if (!track || mine !== this.token) { got.getTracks().forEach((t) => t.stop()); return false; }

    this.removeTrack();
    track.contentHint = "detail"; // favour sharp text over smooth motion
    this.stream.addTrack(track);
    track.addEventListener("ended", () => { if (this.getTrack() === track) this.stop(); }); // browser's own "Stop sharing"
    this.patch({ status: "on", error: undefined });
    return true;
  }

  stop() {
    this.token++;
    this.removeTrack();
    this.patch({ status: "off", error: undefined });
  }

  private removeTrack() {
    const t = this.getTrack();
    if (!t) return;
    t.stop();
    this.stream.removeTrack(t);
  }

  private patch(p: Partial<ScreenState>) {
    this.state = { ...this.state, ...p };
    this.listeners.forEach((fn) => fn(this.state));
  }
}