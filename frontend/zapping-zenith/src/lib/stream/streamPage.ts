// frontend/src/lib/stream/streamPage.ts
import { Peer } from "./peer";
import type { RealtimeClient } from "../realtime/client";
import type { LocalMedia } from "../media/localMedia";
import type { StreamStatePayload, SignalPayload } from "./types";

export interface StreamHandle {
  leave(): void;
}

export function initStream(
  realtime: RealtimeClient,
  media: LocalMedia,
  roomId: string,
  groupId: string,
  myRole: "host" | "viewer",
  remoteVideoEl: HTMLVideoElement,
): StreamHandle {
  const peers = new Map<string, Peer>();
  let myConnId = "";

  // Your RealtimeClient.on hands you the ENVELOPE, not the payload.
  // Envelope shape (from lib/realtime/types.ts): { type, id?, payload }.
  const offHello = realtime.on("hello", (env) => {
    const p = env.payload as { connectionId?: string } | undefined;
    if (p?.connectionId) myConnId = p.connectionId;
  });

  const offState = realtime.on("stream.state", (env) => {
    const payload = env.payload as StreamStatePayload | undefined;
    if (!payload) return;
    const roster = payload.participants;

    // Create peers for anyone new.
    for (const entry of roster) {
      if (entry.connectionId === myConnId) continue;
      if (peers.has(entry.connectionId)) continue;

      const peer = new Peer({
        remoteConnID: entry.connectionId,
        media,
        isPublisher: myRole === "host",
        realtime,
        roomId,
        onRemoteStream: (s) => {
          if (entry.role === "host") {
            remoteVideoEl.srcObject = s;
            remoteVideoEl.parentElement?.removeAttribute("hidden");
          }
        },
        onStateChange: (st) => {
          console.debug("[stream] peer", entry.connectionId, st);
        },
      });
      peers.set(entry.connectionId, peer);

      // Only the host initiates. Viewers wait for the offer.
      if (myRole === "host") {
        void peer.createOffer();
      }
    }

    // Tear down peers who left.
    for (const [id, peer] of peers) {
      if (!roster.some((r) => r.connectionId === id)) {
        peer.close();
        peers.delete(id);
      }
    }
  });

  const offSignal = realtime.on("stream.signal", async (env) => {
    const payload = env.payload as SignalPayload | undefined;
    if (!payload?.from) return;
    const peer = peers.get(payload.from);
    if (!peer) return;
    switch (payload.kind) {
      case "offer":
        return peer.handleOffer(payload.data as RTCSessionDescriptionInit);
      case "answer":
        return peer.handleAnswer(payload.data as RTCSessionDescriptionInit);
      case "ice":
        return peer.handleIce(payload.data as RTCIceCandidateInit);
    }
  });

  // Re-join on EVERY open (initial connect + every reconnect). Your own
  // comment in roomPage.ts says exactly this: send() returns null while
  // the socket isn't open and queues nothing.
  const offStatus = realtime.onStatus((status) => {
    if (status === "open") {
      realtime.send("stream.join", { groupId, roomId });
    }
  });

  // If already open at the time initStream is called, join immediately too.
  if (realtime.status === "open") {
    realtime.send("stream.join", { groupId, roomId });
  }

  return {
    // leave() {
    //   realtime.send("stream.leave", { roomId });
    //   for (const p of peers.values()) p.close();
    //   peers.clear();
    //   offHello();
    //   offState();
    //   offSignal();
    //   offStatus();
    // },

    leave() {
      realtime.send("stream.leave", { roomId });
      for (const p of peers.values()) p.close();
      peers.clear();
      remoteVideoEl.srcObject = null;
      remoteVideoEl.parentElement?.setAttribute("hidden", "");   // hides on leave 
      offHello(); offState(); offSignal(); offStatus();
    },
  };
}


