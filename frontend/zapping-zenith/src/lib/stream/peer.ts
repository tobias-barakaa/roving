// frontend: src/lib/stream/peer.ts
import type { RealtimeClient } from "../realtime/client";
import type { LocalMedia } from "../media/localMedia";
import { iceServers } from "./iceServers";

export interface PeerOptions {
  remoteConnID: string;
  media: LocalMedia;
  /** true on the host's side: sends video + audio. Viewers only ever send audio. */
  isPublisher: boolean;
  realtime: RealtimeClient;
  roomId: string;
  /** Fires whenever the remote side adds a track. The stream is the same object each time. */
  onRemoteStream: (stream: MediaStream, track: MediaStreamTrack) => void;
  onStateChange: (state: RTCPeerConnectionState) => void;
}

/**
 * One RTCPeerConnection to one other tab.
 *
 * Why transceivers: the old version added the camera/mic tracks at the moment
 * the offer was made. If the host hadn't switched the camera on yet, the offer
 * carried no video and turning it on later changed nothing. Here the offer
 * always contains a video and an audio slot, and the tracks are plugged into
 * those slots with replaceTrack() whenever they appear, change or stop. No
 * renegotiation is needed for any of that.
 *
 * Only the host makes offers. The viewer answers and only ever sends audio.
 */
export class Peer {
  readonly pc: RTCPeerConnection;
  private readonly o: PeerOptions;
  private readonly remote = new MediaStream();
  private pendingIce: RTCIceCandidateInit[] = [];
  private senders: { audio?: RTCRtpSender; video?: RTCRtpSender } = {};
  private chain: Promise<unknown> = Promise.resolve();
  private closed = false;

  constructor(o: PeerOptions) {
    this.o = o;
    this.pc = new RTCPeerConnection({ iceServers: iceServers() });

    this.pc.onicecandidate = (e) => {
      if (e.candidate) this.signal("ice", e.candidate.toJSON());
    };
    this.pc.ontrack = (e) => {
      this.remote.addTrack(e.track);
      this.o.onRemoteStream(this.remote, e.track);
    };
    this.pc.onconnectionstatechange = () => {
      this.o.onStateChange(this.pc.connectionState);
      // The host retries a failed path by restarting ICE (viewers just answer).
      if (this.pc.connectionState === "failed" && this.o.isPublisher && !this.closed) {
        void this.queue(() => this.makeOffer(true));
      }
    };
  }

  /** Host only: open the connection to a viewer. */
  start(): Promise<void> {
    return this.queue(async () => {
      const stream = this.o.media.getStream();
      const opts = (direction: RTCRtpTransceiverDirection): RTCRtpTransceiverInit => ({ direction, streams: [stream] });
      this.senders.video = this.pc.addTransceiver("video", opts("sendonly")).sender;
      this.senders.audio = this.pc.addTransceiver("audio", opts("sendrecv")).sender;
      await this.syncTracks();
      await this.makeOffer(false);
    });
  }

  /** Feed one signalling message in. Messages are processed strictly in order. */
  handleSignal(kind: "offer" | "answer" | "ice", data: unknown): Promise<void> {
    return this.queue(async () => {
      if (kind === "offer") return this.onOffer(data as RTCSessionDescriptionInit);
      if (kind === "answer") {
        await this.pc.setRemoteDescription(data as RTCSessionDescriptionInit);
        return this.flushIce();
      }
      const cand = data as RTCIceCandidateInit;
      if (this.pc.remoteDescription) await this.pc.addIceCandidate(cand).catch(() => {});
      else this.pendingIce.push(cand);
    });
  }

  /**
   * Plug the current camera/microphone tracks into the senders.
   * Call it whenever LocalMedia changes (camera on/off, device switch, mic on).
   * Viewers have no video sender, so they cannot publish video even by mistake.
   */
  async syncTracks(): Promise<void> {
    if (this.closed) return;
    const pairs: ["audio" | "video", "camera" | "microphone"][] = [["audio", "microphone"], ["video", "camera"]];
    for (const [kind, source] of pairs) {
      const sender = this.senders[kind];
      if (!sender) continue;
      if (kind === "video" && !this.o.isPublisher) continue;
      const track = this.o.media.getTrack(source) ?? null;
      if (sender.track !== track) await sender.replaceTrack(track).catch(() => {});
    }
  }

  close() {
    this.closed = true;
    // Do NOT stop the senders' tracks: they are the person's own camera/mic,
    // shared by every peer connection. LocalMedia decides when they stop.
    this.pc.ontrack = this.pc.onicecandidate = this.pc.onconnectionstatechange = null;
    this.pc.close();
  }

  // ---- internals ----

  private queue<T>(fn: () => Promise<T>): Promise<T> {
    const run = this.chain.then(fn, fn);
    this.chain = run.catch((e) => console.warn("[stream] peer step failed", e));
    return run;
  }

  private async onOffer(offer: RTCSessionDescriptionInit) {
    await this.pc.setRemoteDescription(offer);
    // The transceivers now exist (created by the host's offer). Viewers send
    // audio and only receive video.
    for (const t of this.pc.getTransceivers()) {
      const kind = t.receiver.track.kind as "audio" | "video";
      if (kind === "audio") {
        t.direction = "sendrecv";
        this.senders.audio = t.sender;
      } else {
        t.direction = "recvonly";
      }
    }
    await this.syncTracks();
    await this.flushIce();
    const answer = await this.pc.createAnswer();
    await this.pc.setLocalDescription(answer);
    this.signal("answer", { type: answer.type, sdp: answer.sdp });
  }

  private async makeOffer(iceRestart: boolean) {
    const offer = await this.pc.createOffer(iceRestart ? { iceRestart: true } : undefined);
    await this.pc.setLocalDescription(offer);
    this.signal("offer", { type: offer.type, sdp: offer.sdp });
  }

  private async flushIce() {
    const queued = this.pendingIce;
    this.pendingIce = [];
    for (const c of queued) await this.pc.addIceCandidate(c).catch(() => {});
  }

  private signal(kind: "offer" | "answer" | "ice", data: unknown) {
    if (this.closed) return;
    this.o.realtime.send("stream.signal", { roomId: this.o.roomId, to: this.o.remoteConnID, kind, data });
  }
}
