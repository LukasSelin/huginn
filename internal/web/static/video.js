'use strict';

// VideoWriter encodes an animation drawn on a canvas as a WebM video, with the
// browser's own VP9 (or VP8) encoder. Frames are given with how long each is
// shown; a frame held long is repeated once a second so the video seeks well.
class VideoWriter {
  // open returns a VideoWriter for canvas, or null if the browser cannot
  // encode video at its size.
  static async open(canvas, fps) {
    if (typeof VideoEncoder === 'undefined') return null;
    const { width, height } = canvas;
    // About 0.1 bits a pixel a frame: crisp text, a few MB a minute at 1080p.
    const bitrate = Math.round(Math.min(16e6, Math.max(2e6, width * height * fps * 0.1)));
    for (const [codec, id] of [['vp09.00.41.08', 'V_VP9'], ['vp09.00.40.08', 'V_VP9'], ['vp8', 'V_VP8']]) {
      const config = { codec, width, height, bitrate, framerate: fps, latencyMode: 'quality' };
      try {
        const { supported } = await VideoEncoder.isConfigSupported(config);
        if (supported) return new VideoWriter(canvas, config, id);
      } catch {
        // Try the next.
      }
    }
    return null;
  }

  constructor(canvas, config, codecId) {
    this.canvas = canvas;
    this.codecId = codecId;
    this.chunks = []; // { data, ms, key }
    this.error = null;
    this.ms = 0; // when the next frame starts
    this.lastKey = -Infinity;
    this.enc = new VideoEncoder({
      output: (chunk) => {
        const data = new Uint8Array(chunk.byteLength);
        chunk.copyTo(data);
        this.chunks.push({ data, ms: Math.round(chunk.timestamp / 1000), key: chunk.type === 'key' });
      },
      error: (e) => { this.error = e; },
    });
    this.enc.configure(config);
  }

  // add encodes the canvas as it is now, shown for ms.
  async add(ms) {
    const end = this.ms + ms;
    do {
      if (this.error) throw this.error;
      const step = Math.min(1000, end - this.ms);
      const frame = new VideoFrame(this.canvas, { timestamp: Math.round(this.ms * 1000), duration: Math.round(step * 1000) });
      const key = this.ms - this.lastKey >= 2000;
      if (key) this.lastKey = this.ms;
      this.enc.encode(frame, { keyFrame: key });
      frame.close();
      this.ms += step;
      // Keep the encoder from falling far behind.
      while (this.enc.encodeQueueSize > 8) await new Promise((r) => setTimeout(r, 1));
    } while (end - this.ms > 0.5);
    this.ms = end;
  }

  // finish waits for the encoder and returns the video as a Blob.
  async finish() {
    await this.enc.flush();
    this.enc.close();
    if (this.error) throw this.error;
    this.chunks.sort((a, b) => a.ms - b.ms);
    return new Blob([webm(this.chunks, this.canvas.width, this.canvas.height, this.codecId, this.ms)], { type: 'video/webm' });
  }

  cancel() {
    if (this.enc.state !== 'closed') this.enc.close();
  }
}

// webm wraps encoded frames in a WebM file: a cluster from each key frame,
// with cues to seek by.
function webm(chunks, width, height, codecId, durationMs) {
  const info = ebml(0x1549a966, [
    ebml(0x2ad7b1, uintBytes(1e6)), // TimestampScale: ms
    ebml(0x4489, floatBytes(durationMs)),
    ebml(0x4d80, strBytes('huginn')),
    ebml(0x5741, strBytes('huginn')),
  ]);
  const tracks = ebml(0x1654ae6b, [
    ebml(0xae, [
      ebml(0xd7, uintBytes(1)), // TrackNumber
      ebml(0x73c5, uintBytes(1)), // TrackUID
      ebml(0x83, uintBytes(1)), // TrackType: video
      ebml(0x86, strBytes(codecId)),
      ebml(0xe0, [ebml(0xb0, uintBytes(width)), ebml(0xba, uintBytes(height))]),
    ]),
  ]);
  const clusters = [];
  let cluster = null;
  for (const c of chunks) {
    if (!cluster || c.key || c.ms - cluster.ms > 30000) {
      cluster = { ms: c.ms, key: c.key, blocks: [] };
      clusters.push(cluster);
    }
    const rel = c.ms - cluster.ms;
    const head = new Uint8Array([0x81, (rel >> 8) & 0xff, rel & 0xff, c.key ? 0x80 : 0]);
    cluster.blocks.push(ebml(0xa3, [head, c.data])); // SimpleBlock
  }
  // SeekHead has a fixed size, its positions written in 8 bytes.
  const seek = (id, pos) => ebml(0x4dbb, [ebml(0x53ab, id), ebml(0x53ac, uintBytes(pos, 8))]);
  const headSize = ebml(0x114d9b74, [seek(new Uint8Array(4), 0), seek(new Uint8Array(4), 0), seek(new Uint8Array(4), 0)]).length;
  let pos = headSize + info.length + tracks.length;
  const infoPos = headSize, tracksPos = headSize + info.length;
  const cues = [];
  const clusterBytes = clusters.map((cl) => {
    const b = ebml(0x1f43b675, [ebml(0xe7, uintBytes(cl.ms)), ...cl.blocks]);
    if (cl.key) {
      cues.push(ebml(0xbb, [ebml(0xb3, uintBytes(cl.ms)), ebml(0xb7, [ebml(0xf7, uintBytes(1)), ebml(0xf1, uintBytes(pos))])]));
    }
    pos += b.length;
    return b;
  });
  const cuesPos = pos;
  const head = ebml(0x114d9b74, [
    seek(idBytes(0x1549a966), infoPos),
    seek(idBytes(0x1654ae6b), tracksPos),
    seek(idBytes(0x1c53bb6b), cuesPos),
  ]);
  const header = ebml(0x1a45dfa3, [
    ebml(0x4286, uintBytes(1)),
    ebml(0x42f7, uintBytes(1)),
    ebml(0x42f2, uintBytes(4)),
    ebml(0x42f3, uintBytes(8)),
    ebml(0x4282, strBytes('webm')),
    ebml(0x4287, uintBytes(4)),
    ebml(0x4285, uintBytes(2)),
  ]);
  const segment = ebml(0x18538067, [head, info, tracks, ...clusterBytes, ebml(0x1c53bb6b, cues)]);
  return concatBytes([header, segment]);
}

// ebml encodes an element: its id, its size and its body, which is bytes or
// a list of encoded elements.
function ebml(id, body) {
  const data = body instanceof Uint8Array ? body : concatBytes(body);
  return concatBytes([idBytes(id), sizeBytes(data.length), data]);
}

function idBytes(id) {
  const out = [];
  for (let v = id; v > 0; v = Math.floor(v / 256)) out.unshift(v & 0xff);
  return new Uint8Array(out);
}

function sizeBytes(n) {
  let len = 1;
  while (n >= 2 ** (7 * len) - 1) len++;
  const out = uintBytes(n, len);
  out[0] |= 0x80 >> (len - 1);
  return out;
}

function uintBytes(n, len) {
  if (!len) for (len = 1; n >= 2 ** (8 * len); len++);
  const out = new Uint8Array(len);
  let v = n;
  for (let i = len - 1; i >= 0; i--) {
    out[i] = v % 256;
    v = Math.floor(v / 256);
  }
  return out;
}

function floatBytes(f) {
  const out = new Uint8Array(8);
  new DataView(out.buffer).setFloat64(0, f);
  return out;
}

function strBytes(s) {
  return new TextEncoder().encode(s);
}

function concatBytes(parts) {
  let n = 0;
  for (const p of parts) n += p.length;
  const out = new Uint8Array(n);
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}
