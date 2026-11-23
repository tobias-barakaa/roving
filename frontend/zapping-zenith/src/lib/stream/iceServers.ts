// frontend: src/lib/stream/iceServers.ts

/**
 * STUN/TURN servers for the peer connections.
 *
 * STUN alone connects people on ordinary home/office networks. Strict NATs,
 * mobile carriers and corporate firewalls need a TURN relay. Add one with an
 * env var, no code change:
 *
 *   PUBLIC_ICE_SERVERS='[{"urls":"stun:stun.l.google.com:19302"},
 *     {"urls":"turn:turn.example.com:3478","username":"user","credential":"secret"}]'
 *
 * Anything in PUBLIC_* is visible to every visitor, so use a dedicated TURN
 * account for this. Short-lived credentials minted by the backend are the
 * proper long-term answer (later stage).
 */
const DEFAULT: RTCIceServer[] = [{ urls: "stun:stun.l.google.com:19302" }];

export function iceServers(): RTCIceServer[] {
  const raw = import.meta.env.PUBLIC_ICE_SERVERS as string | undefined;
  if (!raw) return DEFAULT;
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) && parsed.length ? (parsed as RTCIceServer[]) : DEFAULT;
  } catch {
    console.warn("[stream] PUBLIC_ICE_SERVERS is not valid JSON; using the default STUN server");
    return DEFAULT;
  }
}
