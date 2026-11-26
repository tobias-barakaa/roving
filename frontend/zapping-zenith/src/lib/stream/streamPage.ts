// frontend: src/lib/stream/streamPage.ts
import { Peer } from "./peer";
import type { RealtimeClient } from "../realtime/client";
import type { LocalMedia } from "../media/localMedia";
import type { RosterEntry, SignalPayload, StreamJoinedPayload, StreamRole, StreamStatePayload } from "./types";

export interface HostVideoState {
  /** Is the host in the room right now? */
  present: boolean;
  /** State of my connection to the host. */
  link: RTCPeerConnectionState | "none";
  /** Are video frames actually arriving? */
  live: boolean;
}

export interface StreamOptions {
  realtime: RealtimeClient;
  media: LocalMedia;
  roomId: string;
  groupId: string;
  /** Viewers: the host's picture goes here. */
  remoteVideo: HTMLVideoElement;
  /** Host: the viewers' voices are played through <audio> elements added here. */
  audioSink: HTMLElement;
  onRole(role: StreamRole): void;
  onRoster(r: { participants: RosterEntry[]; hostConnId: string; myConnId: string }): void;
  onHostVideo(s: HostVideoState): void;
  /** The browser refused to autoplay: show a "tap to play" button and call resumePlayback() from it. */
  onPlaybackBlocked(blocked: boolean): void;
  onProblem(message: string): void;
}

export interface StreamHandle {
  resumePlayback(): void;
  leave(): void;
}

export function initStream(o: StreamOptions): StreamHandle {
  const { realtime, media, roomId, groupId } = o;
  const peers = new Map<string, Peer>();
  const audioEls = new Map<string, HTMLAudioElement>();
  let myConnId = "";
  let role: StreamRole | null = null;
  let hostConnId = "";
  let hostPresent = false;
  let link: HostVideoState["link"] = "none";
  let live = false; // what the UI is told: the host's camera is on AND frames are arriving
  let hostCamera = false; // what the host announced
  let framesFlowing = false; // the remote video track is delivering
  let joinId: string | null = null;
  let left = false;

  const pushHost = () => {
    live = hostPresent && hostCamera && framesFlowing;
    o.onHostVideo({ present: hostPresent, link, live });
  };

  const tryPlay = (el: HTMLMediaElement) => {
    el.play().then(() => o.onPlaybackBlocked(false)).catch(() => o.onPlaybackBlocked(true));
  };

  function dropPeer(id: string) {
    peers.get(id)?.close();
    peers.delete(id);
    const a = audioEls.get(id);
    if (a) { a.srcObject = null; a.remove(); audioEls.delete(id); }
  }

  // function resetAll() {
  //   for (const id of [...peers.keys()]) dropPeer(id);
  //   o.remoteVideo.srcObject = null;
  //   hostPresent = false; link = "none"; hostCamera = false; framesFlowing = false; hostConnId = "";
  //   pushHost();
  // }


  function resetAll() {
    for (const id of [...peers.keys()]) dropPeer(id);
    o.remoteVideo.srcObject = null;
    hostPresent = false; link = "none"; hostCamera = false; framesFlowing = false; hostConnId = "";
    roleApplied = false;   // add this
    pushHost();
  }
  
  function makePeer(remoteId: string): Peer {
    const peer = new Peer({
      remoteConnID: remoteId,
      media,
      isPublisher: role === "host",
      realtime,
      roomId,
      onRemoteStream: (stream, track) => {
        if (role === "host") {
          // A viewer's voice.
          if (track.kind !== "audio") return;
          let a = audioEls.get(remoteId);
          if (!a) {
            a = document.createElement("audio");
            a.autoplay = true;
            a.dataset.peer = remoteId;
            o.audioSink.append(a);
            audioEls.set(remoteId, a);
          }
          if (a.srcObject !== stream) a.srcObject = stream;
          tryPlay(a);
          return;
        }
        // Viewer: this is the host.
        if (o.remoteVideo.srcObject !== stream) o.remoteVideo.srcObject = stream;
        tryPlay(o.remoteVideo);
        if (track.kind === "video") {
          framesFlowing = !track.muted;
          track.onunmute = () => { framesFlowing = true; pushHost(); };
          track.onmute = () => { framesFlowing = false; pushHost(); };
          pushHost();
        }
      },
      onStateChange: (st) => {
        if (role === "viewer" && remoteId === hostConnId) { link = st; pushHost(); }
      },
    });
    peers.set(remoteId, peer);
    return peer;
  }

  // The host tells the room when the camera goes on or off (the server only accepts this from the host).
  let announced: boolean | null = null;
  function announceCamera(force = false) {
    if (role !== "host") return;
    const on = media.getState().camera.status === "on";
    if (!force && on === announced) return;
    announced = on;
    realtime.send("stream.media", { roomId, camera: on });
  }

  /* ---- messages from the server ---- */

  // A new connection id means every old peer is stale (first connect, or after a reconnect).
  // const offHello = realtime.on("hello", (env) => {
  //   const p = env.payload as { connectionId?: string } | undefined;
  //   if (p?.connectionId) myConnId = p.connectionId;
  //   role = null;
  //   announced = null;
  //   resetAll();
  // });
  let roleApplied = false;
  
  // A new connection id means every old peer is stale (first connect, or after a reconnect).
  const offHello = realtime.on("hello", (env) => {
    const p = env.payload as { connectionId?: string } | undefined;
    if (!p?.connectionId) return;
    // Only reset on a *new* connection — a re-hello on the same socket must not
    // clobber a role that stream.joined already set.
    if (p.connectionId !== myConnId) {
      myConnId = p.connectionId;
      role = null;
      roleApplied = false;
      announced = null;
      resetAll();
    }
  });

  

  // const offJoined = realtime.on("stream.joined", (env) => {
  //   const p = env.payload as StreamJoinedPayload | undefined;
  //   if (!p || p.roomId !== roomId) return;
  //   role = p.role;
  //   myConnId = p.connectionId || myConnId;
  //   o.onRole(p.role);
  //   announceCamera(true);
  // });

  const offJoined = realtime.on("stream.joined", (env) => {
    const p = env.payload as StreamJoinedPayload | undefined;
    if (!p || p.roomId !== roomId) return;
    role = p.role;
    roleApplied = true;
    myConnId = p.connectionId || myConnId;
    o.onRole(p.role);
    announceCamera(true);
  });
  
  // const offState = realtime.on("stream.state", (env) => {
  //   const p = env.payload as StreamStatePayload | undefined;
  //   if (!p || p.roomId !== roomId || !role) return;
  //   hostConnId = p.hostConnId;
  //   hostCamera = !!p.hostCamera;
  //   const ids = new Set(p.participants.map((x) => x.connectionId));
  //   o.onRoster({ participants: p.participants, hostConnId, myConnId });

  //   // Forget peers that left.
  //   for (const id of [...peers.keys()]) if (!ids.has(id)) dropPeer(id);

  //   if (role === "host") {
  //     // The host opens one connection to every viewer.
  //     for (const e of p.participants) {
  //       if (e.connectionId === myConnId || peers.has(e.connectionId)) continue;
  //       void makePeer(e.connectionId).start();
  //     }
  //   } else {
  //     // A viewer only ever talks to the host.
  //     for (const id of [...peers.keys()]) if (id !== hostConnId) dropPeer(id);
  //     const hostGone = !hostConnId;
  //     hostPresent = !hostGone;
  //     if (hostGone) {
  //       o.remoteVideo.srcObject = null;
  //       link = "none"; framesFlowing = false;
  //     } else if (!peers.has(hostConnId)) {
  //       link = "connecting"; framesFlowing = false; // the host's offer is on its way
  //     }
  //     pushHost();
  //   }
  // });

  const offState = realtime.on("stream.state", (env) => {
    const p = env.payload as StreamStatePayload | undefined;
    if (!p || p.roomId !== roomId) return;
  
    hostConnId = p.hostConnId;
    hostCamera = !!p.hostCamera;
    const ids = new Set(p.participants.map((x) => x.connectionId));
    o.onRoster({ participants: p.participants, hostConnId, myConnId });
  
    // FALLBACK: if stream.joined was missed or raced, derive the role from the
    // roster the server just sent. This is the same data the server used to
    // decide, so it is authoritative.
    if (!roleApplied) {
      const me = p.participants.find((x) => x.connectionId === myConnId);
      if (me) {
        role = me.role;
        roleApplied = true;
        o.onRole(me.role);
        announceCamera(true);
      }
    }
  
    // Forget peers that left.
    for (const id of [...peers.keys()]) if (!ids.has(id)) dropPeer(id);
  
    if (role === "host") {
      for (const e of p.participants) {
        if (e.connectionId === myConnId || peers.has(e.connectionId)) continue;
        void makePeer(e.connectionId).start();
      }
    } else if (role === "viewer") {
      for (const id of [...peers.keys()]) if (id !== hostConnId) dropPeer(id);
      const hostGone = !hostConnId;
      hostPresent = !hostGone;
      if (hostGone) {
        o.remoteVideo.srcObject = null;
        link = "none"; framesFlowing = false;
      } else if (!peers.has(hostConnId)) {
        link = "connecting"; framesFlowing = false;
      }
      pushHost();
    }
  });
  
  const offSignal = realtime.on("stream.signal", (env) => {
    const p = env.payload as SignalPayload | undefined;
    if (!p?.from || p.roomId !== roomId || !role) return;
    let peer = peers.get(p.from);
    // Viewers create their end lazily, the moment the host's offer arrives.
    if (!peer && role === "viewer" && p.kind === "offer") peer = makePeer(p.from);
    void peer?.handleSignal(p.kind, p.data);
  });

  const offError = realtime.on("error", (env) => {
    if (joinId && env.id === joinId) o.onProblem("You can't join the live part of this room.");
  });

  // Join on every open: first connect and every reconnect.
  const join = () => { joinId = realtime.send("stream.join", { groupId, roomId }); };
  const offStatus = realtime.onStatus((s) => {
    if (s === "open") join();
  });

  // Camera on/off, device switch, mic on: plug the new tracks into every connection.
  const offMedia = media.subscribe(() => {
    for (const p of peers.values()) void p.syncTracks();
    announceCamera();
  });

  return {
    resumePlayback() {
      tryPlay(o.remoteVideo);
      audioEls.forEach((a) => tryPlay(a));
    },
    leave() {
      if (left) return;
      left = true;
      realtime.send("stream.leave", { roomId });
      offHello(); offJoined(); offState(); offSignal(); offError(); offStatus(); offMedia();
      resetAll();
    },
  };
}
