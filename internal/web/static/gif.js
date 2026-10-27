'use strict';

// GifWriter encodes an animation drawn on a canvas as a looping GIF. Each
// frame gets its own palette of the colours it uses most, and only the box
// that changed since the frame before is written; a frame that changes
// nothing lengthens the one before it instead. A map that mostly holds still
// so makes a small file.
class GifWriter {
  constructor(width, height) {
    this.w = width;
    this.h = height;
    this.out = new ByteOut();
    this.prev = null; // the last frame's pixels, RGBA
    this.pending = null; // the last frame, written once its delay is known
    const o = this.out;
    o.str('GIF89a');
    o.u16(width);
    o.u16(height);
    o.bytes(0, 0, 0); // no global palette
    // Loop forever.
    o.bytes(0x21, 0xff, 11);
    o.str('NETSCAPE2.0');
    o.bytes(3, 1, 0, 0, 0);
  }

  // add appends a frame of RGBA pixels, this.w × this.h, shown for ms.
  add(px, ms) {
    const box = this.prev ? changed(this.prev, px, this.w, this.h) : { x0: 0, y0: 0, x1: this.w - 1, y1: this.h - 1 };
    if (!box) {
      if (this.pending) this.pending.ms += ms;
      return;
    }
    this.flush();
    const { x0, y0, x1, y1 } = box;
    const w = x1 - x0 + 1, h = y1 - y0 + 1;
    const rgb = new Uint8Array(w * h * 3);
    for (let y = 0; y < h; y++) {
      for (let x = 0; x < w; x++) {
        const i = ((y0 + y) * this.w + x0 + x) * 4, j = (y * w + x) * 3;
        rgb[j] = px[i];
        rgb[j + 1] = px[i + 1];
        rgb[j + 2] = px[i + 2];
      }
    }
    this.pending = { x0, y0, w, h, rgb, ms };
    this.prev = px.slice();
  }

  flush() {
    const f = this.pending;
    if (!f) return;
    this.pending = null;
    const o = this.out;
    const { palette, index } = quantize(f.rgb);
    // Graphic control: leave the frame in place, no transparency.
    o.bytes(0x21, 0xf9, 4, 0x04);
    o.u16(Math.min(65535, Math.max(2, Math.round(f.ms / 10))));
    o.bytes(0, 0);
    o.bytes(0x2c);
    o.u16(f.x0);
    o.u16(f.y0);
    o.u16(f.w);
    o.u16(f.h);
    o.bytes(0x87); // a local palette of 256
    o.raw(palette);
    o.bytes(8);
    const data = lzw(index, 8);
    for (let i = 0; i < data.length; i += 255) {
      const n = Math.min(255, data.length - i);
      o.bytes(n);
      o.raw(data.subarray(i, i + n));
    }
    o.bytes(0);
  }

  // finish returns the GIF.
  finish() {
    this.flush();
    this.out.bytes(0x3b);
    return new Blob([this.out.done()], { type: 'image/gif' });
  }
}

// changed returns the box of pixels that differ between a and b, or null.
function changed(a, b, w, h) {
  let x0 = w, y0 = h, x1 = -1, y1 = -1;
  for (let y = 0; y < h; y++) {
    const row = y * w * 4;
    for (let x = 0; x < w; x++) {
      const i = row + x * 4;
      if (a[i] !== b[i] || a[i + 1] !== b[i + 1] || a[i + 2] !== b[i + 2]) {
        if (x < x0) x0 = x;
        if (x > x1) x1 = x;
        if (y < y0) y0 = y;
        y1 = y;
      }
    }
  }
  return x1 < 0 ? null : { x0, y0, x1, y1 };
}

// quantize picks a palette of 256 for RGB pixels: the most used of their
// colours at 5 bits a channel, each the mean of the pixels it stands for,
// and maps every pixel to the nearest of them.
function quantize(rgb) {
  const n = rgb.length / 3;
  const count = new Uint32Array(32768), sr = new Uint32Array(32768), sg = new Uint32Array(32768), sb = new Uint32Array(32768);
  const keys = new Uint16Array(n);
  for (let i = 0; i < n; i++) {
    const r = rgb[3 * i], g = rgb[3 * i + 1], b = rgb[3 * i + 2];
    const k = ((r >> 3) << 10) | ((g >> 3) << 5) | (b >> 3);
    keys[i] = k;
    count[k]++;
    sr[k] += r;
    sg[k] += g;
    sb[k] += b;
  }
  const used = [];
  for (let k = 0; k < 32768; k++) if (count[k]) used.push(k);
  used.sort((p, q) => count[q] - count[p]);
  const mean = (k) => [Math.round(sr[k] / count[k]), Math.round(sg[k] / count[k]), Math.round(sb[k] / count[k])];
  const top = used.slice(0, 256).map(mean);
  const palette = new Uint8Array(768);
  top.forEach((c, i) => palette.set(c, 3 * i));
  const map = new Uint8Array(32768);
  used.forEach((k, i) => {
    if (i < 256) {
      map[k] = i;
      return;
    }
    const [r, g, b] = mean(k);
    let best = 0, bestD = Infinity;
    for (let j = 0; j < top.length; j++) {
      const dr = r - top[j][0], dg = g - top[j][1], db = b - top[j][2];
      const d = 2 * dr * dr + 4 * dg * dg + 3 * db * db;
      if (d < bestD) { bestD = d; best = j; }
    }
    map[k] = best;
  });
  const index = new Uint8Array(n);
  for (let i = 0; i < n; i++) index[i] = map[keys[i]];
  return { palette, index };
}

// lzw compresses palette indices as GIF image data, before it is cut into
// blocks. The code table starts over once it is full.
function lzw(index, minSize) {
  const out = new ByteOut();
  const clear = 1 << minSize, eoi = clear + 1;
  let size = minSize + 1, next = eoi + 1;
  const dict = new Map();
  let acc = 0, bits = 0;
  const emit = (code) => {
    acc |= code << bits;
    bits += size;
    while (bits >= 8) {
      out.bytes(acc & 0xff);
      acc >>>= 8;
      bits -= 8;
    }
  };
  emit(clear);
  let prefix = index[0];
  for (let i = 1; i < index.length; i++) {
    const k = index[i];
    const key = (prefix << 8) | k;
    const c = dict.get(key);
    if (c !== undefined) {
      prefix = c;
      continue;
    }
    emit(prefix);
    if (next < 4096) {
      dict.set(key, next++);
      if (next > 1 << size && size < 12) size++;
    } else {
      emit(clear);
      dict.clear();
      size = minSize + 1;
      next = eoi + 1;
    }
    prefix = k;
  }
  emit(prefix);
  emit(eoi);
  if (bits > 0) out.bytes(acc & 0xff);
  return out.done();
}

// ByteOut is a growing buffer of bytes.
class ByteOut {
  constructor() {
    this.buf = new Uint8Array(1 << 16);
    this.n = 0;
  }

  room(k) {
    if (this.n + k <= this.buf.length) return;
    const b = new Uint8Array(Math.max(2 * this.buf.length, this.n + k));
    b.set(this.buf.subarray(0, this.n));
    this.buf = b;
  }

  bytes(...bs) {
    this.room(bs.length);
    for (const b of bs) this.buf[this.n++] = b;
  }

  raw(a) {
    this.room(a.length);
    this.buf.set(a, this.n);
    this.n += a.length;
  }

  u16(v) { this.bytes(v & 0xff, (v >> 8) & 0xff); }

  str(s) { for (let i = 0; i < s.length; i++) this.bytes(s.charCodeAt(i)); }

  done() { return this.buf.subarray(0, this.n); }
}
