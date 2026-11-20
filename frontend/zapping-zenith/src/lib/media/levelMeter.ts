/**
 * Microphone level meter. Web Audio only, no DOM: the caller decides how to draw the 0..1 value.
 * Separate from localMedia.ts so the media lib stays about tracks, not audio analysis.
 */
export interface LevelMeter { stop(): void; }

export function startLevelMeter(stream: MediaStream, onLevel: (level: number) => void): LevelMeter | null {
  const Ctx: typeof AudioContext | undefined = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
  if (!Ctx || !stream.getAudioTracks().length) return null;

  let ctx: AudioContext;
  try { ctx = new Ctx(); } catch { return null; }
  const source = ctx.createMediaStreamSource(new MediaStream(stream.getAudioTracks()));
  const analyser = ctx.createAnalyser();
  analyser.fftSize = 512;
  source.connect(analyser); // never connected to ctx.destination: no echo of your own voice
  const data = new Uint8Array(analyser.fftSize);

  let raf = 0;
  let last = 0;
  const tick = (t: number) => {
    raf = requestAnimationFrame(tick);
    if (t - last < 50) return; // ~20 updates a second is plenty
    last = t;
    analyser.getByteTimeDomainData(data);
    let sum = 0;
    for (let i = 0; i < data.length; i++) { const v = (data[i] - 128) / 128; sum += v * v; }
    onLevel(Math.min(1, Math.sqrt(sum / data.length) * 3.2));
  };
  void ctx.resume().catch(() => {});
  raf = requestAnimationFrame(tick);

  return {
    stop() {
      cancelAnimationFrame(raf);
      try { source.disconnect(); } catch {}
      void ctx.close().catch(() => {});
      onLevel(0);
    },
  };
}