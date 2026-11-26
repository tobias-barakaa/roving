// frontend/src/lib/stream/iceServers.ts
export function iceServers(): RTCIceServer[] {
  const servers: RTCIceServer[] = [
    { urls: ["stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"] },
  ];

  // Optional TURN from env (for restrictive NATs / symmetric NAT / corporate networks):
  const turnUrl = import.meta.env.PUBLIC_TURN_URL as string | undefined;
  const turnUser = import.meta.env.PUBLIC_TURN_USER as string | undefined;
  const turnCred = import.meta.env.PUBLIC_TURN_CRED as string | undefined;
  if (turnUrl && turnUser && turnCred) {
    servers.push({ urls: turnUrl, username: turnUser, credential: turnCred });
  }
  return servers;
}