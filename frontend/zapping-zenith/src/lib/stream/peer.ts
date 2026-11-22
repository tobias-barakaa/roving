// frontend/src/lib/stream/peer.ts
import type { RealtimeClient } from "../realtime/client";
import type { LocalMedia } from "../media/localMedia";

export interface PeerOptions {
  remoteConnID: string;
  media: LocalMedia;
  isPublisher: boolean;
  realtime: RealtimeClient;
  roomId: string;
  onRemoteStream: (stream: MediaStream) => void;
  onStateChange: (state: RTCPeerConnectionState) => void;
}

export class Peer {
  readonly pc: RTCPeerConnection;
  private readonly opts: PeerOptions;
  private pendingIce: RTCIceCandidateInit[] = [];
  /** senders keyed by kind so replaceTrack() on device change finds them. */
  private readonly senders = new Map<"audio" | "video", RTCRtpSender>();

  constructor(opts: PeerOptions) {
    this.opts = opts;
    this.pc = new RTCPeerConnection({
      iceServers: [{ urls: "stun:stun.l.google.com:19302" }],
    });

    this.pc.onicecandidate = (e) => {
      if (e.candidate) this.sendSignal("ice", e.candidate.toJSON());
    };
    this.pc.ontrack = (e) => {
      const [stream] = e.streams;
      if (stream) this.opts.onRemoteStream(stream);
    };
    this.pc.onconnectionstatechange = () => {
      this.opts.onStateChange(this.pc.connectionState);
    };
  }

  async createOffer() {
    this.addLocalTracks();
    const offer = await this.pc.createOffer();
    await this.pc.setLocalDescription(offer);
    this.sendSignal("offer", { type: offer.type, sdp: offer.sdp });
  }

  async handleOffer(sdp: RTCSessionDescriptionInit) {
    this.addLocalTracks();
    await this.pc.setRemoteDescription(sdp);
    await this.flushIce();
    const answer = await this.pc.createAnswer();
    await this.pc.setLocalDescription(answer);
    this.sendSignal("answer", { type: answer.type, sdp: answer.sdp });
  }

  async handleAnswer(sdp: RTCSessionDescriptionInit) {
    await this.pc.setRemoteDescription(sdp);
    await this.flushIce();
  }

  async handleIce(candidate: RTCIceCandidateInit) {
    if (this.pc.remoteDescription) {
      await this.pc.addIceCandidate(candidate).catch(() => {});
    } else {
      this.pendingIce.push(candidate);
    }
  }

  /**
   * Call this when the user switches camera or microphone mid-call.
   * It swaps the outgoing track without renegotiating the whole connection.
   */
  async replaceLocalTrack(kind: "audio" | "video", track: MediaStreamTrack | null) {
    const sender = this.senders.get(kind);
    if (!sender) return;
    await sender.replaceTrack(track);
  }

  close() {
    this.pc.getSenders().forEach((s) => s.track?.stop());
    this.pc.close();
  }

  private addLocalTracks() {
    const stream = this.opts.media.getStream();
    if (!stream) return;
    for (const track of stream.getTracks()) {
      // Publisher rule: viewers only add audio.
      if (!this.opts.isPublisher && track.kind !== "audio") continue;
      const sender = this.pc.addTrack(track, stream);
      this.senders.set(track.kind as "audio" | "video", sender);
    }
  }

  private async flushIce() {
    const queued = this.pendingIce;
    this.pendingIce = [];
    for (const c of queued) await this.pc.addIceCandidate(c).catch(() => {});
  }

  private sendSignal(kind: "offer" | "answer" | "ice", data: unknown) {
    this.opts.realtime.send("stream.signal", {
      roomId: this.opts.roomId,
      to: this.opts.remoteConnID,
      kind,
      data,
    });
  }
}