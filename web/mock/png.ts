// A minimal PNG writer for the development mock's icons and screenshots
// (truecolour, no alpha), and two painters: an app icon (a tile with a glyph)
// and a screenshot-like picture of filex with the app at work. Mock data
// only: nothing here reaches the bundle.
import { deflateSync } from 'node:zlib';

const CRC_TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

function crc32(buf: Uint8Array): number {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type: string, data: Uint8Array): Buffer {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type, 'ascii'), Buffer.from(data)]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(td));
  return Buffer.concat([len, td, crc]);
}

export type Rgb = [number, number, number];

/** A width x height image whose pixel colour is `paint(x, y)`. */
export function png(width: number, height: number, paint: (x: number, y: number) => Rgb): Buffer {
  const raw = Buffer.alloc((width * 3 + 1) * height);
  let o = 0;
  for (let y = 0; y < height; y++) {
    raw[o++] = 0;
    for (let x = 0; x < width; x++) {
      const [r, g, b] = paint(x, y);
      raw[o++] = r;
      raw[o++] = g;
      raw[o++] = b;
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  ihdr[10] = 0;
  ihdr[11] = 0;
  ihdr[12] = 0;
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw)),
    chunk('IEND', new Uint8Array()),
  ]);
}

// ---------------------------------------------------------------- shapes
const mix = (a: Rgb, b: Rgb, t: number): Rgb => [0, 1, 2].map((i) => Math.round(a[i] + (b[i] - a[i]) * t)) as Rgb;
const WHITE: Rgb = [255, 255, 255];
const INK: Rgb = [31, 41, 55];

function inRect(u: number, v: number, x0: number, y0: number, x1: number, y1: number, r = 0): boolean {
  if (u < x0 || u > x1 || v < y0 || v > y1) return false;
  if (r <= 0) return true;
  const cx = Math.max(x0 + r, Math.min(u, x1 - r));
  const cy = Math.max(y0 + r, Math.min(v, y1 - r));
  return (u - cx) ** 2 + (v - cy) ** 2 <= r * r;
}
function segDist(u: number, v: number, x0: number, y0: number, x1: number, y1: number): number {
  const dx = x1 - x0;
  const dy = y1 - y0;
  const t = Math.max(0, Math.min(1, ((u - x0) * dx + (v - y0) * dy) / (dx * dx + dy * dy || 1)));
  return Math.hypot(u - x0 - t * dx, v - y0 - t * dy);
}

export type Glyph = 'sign' | 'convert' | 'text' | 'diagram' | 'markdown' | 'redact' | 'photo' | 'board' | 'share' | 'notes';

/** What the glyph paints at (u, v) in [0, 1]: white, a shade of the tile, or nothing. */
function glyph(g: Glyph, u: number, v: number, base: Rgb): Rgb | null {
  const dark = mix(base, INK, 0.45);
  const doc = () => inRect(u, v, 0.3, 0.2, 0.7, 0.8, 0.05);
  switch (g) {
    case 'sign':
      if (Math.abs(v - (0.62 + 0.05 * Math.sin(u * 22))) < 0.025 && u > 0.36 && u < 0.64) return dark;
      if (doc()) return inRect(u, v, 0.37, 0.3, 0.63, 0.34) || inRect(u, v, 0.37, 0.4, 0.58, 0.44) ? mix(base, WHITE, 0.55) : WHITE;
      return null;
    case 'convert': {
      const d = Math.hypot(u - 0.5, v - 0.5);
      const a = Math.atan2(v - 0.5, u - 0.5);
      const gap = Math.abs(Math.sin(a)) < 0.25;
      if (d > 0.2 && d < 0.27 && !gap) return WHITE;
      if (Math.abs(u - 0.735) + Math.abs(v - 0.4) * 1.4 < 0.1 && v < 0.5) return WHITE;
      if (Math.abs(u - 0.265) + Math.abs(v - 0.6) * 1.4 < 0.1 && v > 0.5) return WHITE;
      return null;
    }
    case 'text':
      if (inRect(u, v, 0.27, 0.24, 0.73, 0.34, 0.02) || inRect(u, v, 0.45, 0.24, 0.55, 0.76, 0.02)) return WHITE;
      return null;
    case 'diagram':
      if (segDist(u, v, 0.33, 0.33, 0.67, 0.33) < 0.02 || segDist(u, v, 0.33, 0.33, 0.5, 0.68) < 0.02) return mix(base, WHITE, 0.6);
      if (inRect(u, v, 0.2, 0.22, 0.44, 0.44, 0.05) || inRect(u, v, 0.56, 0.22, 0.8, 0.44, 0.05) || inRect(u, v, 0.38, 0.58, 0.62, 0.8, 0.05)) return WHITE;
      return null;
    case 'markdown':
      if (inRect(u, v, 0.18, 0.3, 0.82, 0.7, 0.06) && !inRect(u, v, 0.22, 0.34, 0.78, 0.66, 0.03)) return WHITE;
      if (segDist(u, v, 0.3, 0.6, 0.3, 0.42) < 0.03 || segDist(u, v, 0.3, 0.42, 0.38, 0.52) < 0.03 || segDist(u, v, 0.38, 0.52, 0.46, 0.42) < 0.03 || segDist(u, v, 0.46, 0.42, 0.46, 0.6) < 0.03)
        return WHITE;
      if (segDist(u, v, 0.62, 0.4, 0.62, 0.58) < 0.03 || (Math.abs(u - 0.62) < 0.6 - v && v > 0.5 && v < 0.62)) return WHITE;
      return null;
    case 'redact':
      if (doc()) return inRect(u, v, 0.36, 0.3, 0.64, 0.37) || inRect(u, v, 0.36, 0.5, 0.56, 0.57) ? INK : inRect(u, v, 0.36, 0.41, 0.6, 0.45) ? mix(base, WHITE, 0.5) : WHITE;
      return null;
    case 'photo':
      if (inRect(u, v, 0.2, 0.26, 0.8, 0.74, 0.06)) {
        if (Math.hypot(u - 0.36, v - 0.4) < 0.06) return mix(base, WHITE, 0.4);
        if (v > 0.74 - (0.26 - Math.abs(u - 0.58) * 0.9) && v > 0.5) return mix(base, INK, 0.2);
        return WHITE;
      }
      return null;
    case 'board':
      for (const [x0, h] of [
        [0.2, 0.5],
        [0.42, 0.36],
        [0.64, 0.44],
      ] as const)
        if (inRect(u, v, x0, 0.24, x0 + 0.16, 0.24 + h, 0.03)) return WHITE;
      return null;
    case 'share':
      if (segDist(u, v, 0.33, 0.5, 0.66, 0.32) < 0.025 || segDist(u, v, 0.33, 0.5, 0.66, 0.68) < 0.025) return WHITE;
      if (Math.hypot(u - 0.33, v - 0.5) < 0.08 || Math.hypot(u - 0.66, v - 0.32) < 0.08 || Math.hypot(u - 0.66, v - 0.68) < 0.08) return WHITE;
      return null;
    case 'notes':
      if (inRect(u, v, 0.28, 0.22, 0.72, 0.78, 0.05)) return inRect(u, v, 0.35, 0.34, 0.65, 0.37) || inRect(u, v, 0.35, 0.46, 0.6, 0.49) || inRect(u, v, 0.35, 0.58, 0.55, 0.61) ? mix(base, WHITE, 0.4) : WHITE;
      return null;
  }
}

/** An app icon: a full square in `base` with a soft diagonal light and a
 *  white glyph (the store rounds the corners), drawn 2x2 supersampled. */
export function appIcon(base: Rgb, g: Glyph, size = 128): Buffer {
  const hi = mix(base, WHITE, 0.18);
  const lo = mix(base, INK, 0.18);
  return png(size, size, (x, y) => {
    let r = 0;
    let gg = 0;
    let b = 0;
    for (const [ox, oy] of [
      [0.25, 0.25],
      [0.75, 0.25],
      [0.25, 0.75],
      [0.75, 0.75],
    ]) {
      const u = (x + ox) / size;
      const v = (y + oy) / size;
      const c = glyph(g, u, v, base) ?? mix(hi, lo, (u + v) / 2);
      r += c[0];
      gg += c[1];
      b += c[2];
    }
    return [Math.round(r / 4), Math.round(gg / 4), Math.round(b / 4)];
  });
}

export type Layout = 'explorer' | 'document' | 'editor' | 'diagram' | 'photos' | 'dialog' | 'board';

const CANVAS: Rgb = [246, 247, 250];
const LINE: Rgb = [226, 230, 236];
const BAR: Rgb = [214, 220, 230];
const SIDE: Rgb = [240, 242, 246];

/** A picture of filex with an app at work: the title bar in the app's
 *  colour, filex's side bar, and the app's own content. */
export function uiShot(accent: Rgb, layout: Layout, w = 960, h = 600): Buffer {
  const soft = mix(accent, WHITE, 0.86);
  const top = 44;
  const side = 176;
  const tones: Rgb[] = [accent, [22, 163, 74], [234, 88, 12], [124, 58, 237], [13, 148, 136], [220, 38, 38]];
  return png(w, h, (x, y) => {
    if (y < top) {
      if (inRect(x, y, 16, 14, 32, 30, 4)) return WHITE;
      if (inRect(x, y, 44, 18, 140, 26, 4)) return mix(accent, WHITE, 0.55);
      if (inRect(x, y, w - 230, 10, w - 20, 34, 8)) return mix(accent, WHITE, 0.22);
      return accent;
    }
    if (x < side) {
      for (let i = 0; i < 7; i++) {
        const yy = top + 24 + i * 34;
        if (inRect(x, y, 12, yy, side - 12, yy + 26, 6)) {
          if (i === 1) return inRect(x, y, 22, yy + 9, 34, yy + 17, 2) ? accent : inRect(x, y, 42, yy + 9, 124 - i * 4, yy + 17, 3) ? mix(accent, INK, 0.2) : soft;
          return inRect(x, y, 22, yy + 9, 34, yy + 17, 2) ? BAR : inRect(x, y, 42, yy + 9, 110 + (i % 3) * 14, yy + 17, 3) ? BAR : SIDE;
        }
      }
      return x === side - 1 ? LINE : SIDE;
    }
    const cx = x - side;
    const cy = y - top;
    const cw = w - side;
    const ch = h - top;
    switch (layout) {
      case 'explorer': {
        if (cy < 48) return cy === 47 ? LINE : inRect(cx, cy, 20, 16, 220, 32, 6) ? CANVAS : WHITE;
        const row = Math.floor((cy - 48) / 44);
        const ry = 48 + row * 44;
        const selected = row === 2;
        if (inRect(cx, cy, 20, ry + 12, 40, ry + 32, 4)) return tones[row % tones.length];
        if (inRect(cx, cy, 56, ry + 17, 260 - (row % 4) * 30, ry + 27, 4)) return selected ? mix(accent, INK, 0.25) : BAR;
        if (inRect(cx, cy, cw - 260, ry + 17, cw - 180, ry + 27, 4) || inRect(cx, cy, cw - 140, ry + 17, cw - 40, ry + 27, 4)) return LINE;
        if (cy === ry + 43) return LINE;
        return selected ? soft : WHITE;
      }
      case 'document': {
        const px0 = cw / 2 - 210;
        const px1 = cw / 2 + 210;
        if (inRect(cx, cy, px0, 24, px1, ch + 40)) {
          const lx = cx - px0;
          const ly = cy - 24;
          if (ly > 40 && ly < 56 && lx > 40 && lx < 260) return INK;
          for (let i = 0; i < 9; i++) if (ly > 84 + i * 26 && ly < 92 + i * 26 && lx > 40 && lx < 380 - (i % 3) * 50) return BAR;
          if (inRect(lx, ly, 40, 340, 250, 420, 6)) {
            if (Math.abs(ly - (385 + 12 * Math.sin(lx / 13))) < 2.4 && lx > 60 && lx < 230) return INK;
            return lx < 42 || lx > 248 || ly < 342 || ly > 418 ? accent : mix(accent, WHITE, 0.92);
          }
          return WHITE;
        }
        if (inRect(cx, cy, cw - 150, ch - 64, cw - 24, ch - 24, 8)) return accent;
        return CANVAS;
      }
      case 'editor': {
        const mid = Math.floor(cw / 2);
        if (cx === mid) return LINE;
        if (cx < mid) {
          const line = Math.floor((cy - 20) / 22);
          if (cy < 20 || line > 22) return [250, 250, 252];
          const ly = 20 + line * 22;
          if (inRect(cx, cy, 14, ly + 6, 30, ly + 14, 2)) return LINE;
          const indent = [0, 16, 16, 32, 32, 16, 0, 0, 16, 32, 16, 0][line % 12];
          const len = 80 + ((line * 37) % 200);
          if (inRect(cx, cy, 44 + indent, ly + 6, 44 + indent + len * 0.3, ly + 14, 3)) return tones[(line + 3) % tones.length];
          if (inRect(cx, cy, 44 + indent + len * 0.3 + 8, ly + 6, 44 + indent + len, ly + 14, 3)) return BAR;
          return [250, 250, 252];
        }
        const px = cx - mid;
        if (inRect(px, cy, 32, 28, 300, 46, 4)) return INK;
        for (let i = 0; i < 6; i++) if (inRect(px, cy, 32, 70 + i * 24, 360 - (i % 3) * 60, 80 + i * 24, 4)) return BAR;
        if (inRect(px, cy, 32, 230, cw - mid - 32, 360, 8)) return inRect(px, cy, 34, 232, cw - mid - 34, 358, 6) ? soft : accent;
        return WHITE;
      }
      case 'diagram': {
        if (cx % 24 === 0 && cy % 24 === 0) return BAR;
        const boxes: [number, number, number, number, Rgb][] = [
          [80, 70, 260, 150, accent],
          [400, 70, 580, 150, tones[1]],
          [240, 260, 420, 340, tones[2]],
          [560, 300, 740, 380, tones[3]],
        ];
        if (segDist(cx, cy, 260, 110, 400, 110) < 2 || segDist(cx, cy, 170, 150, 330, 260) < 2 || segDist(cx, cy, 420, 300, 560, 340) < 2) return INK;
        for (const [x0, y0, x1, y1, c] of boxes) if (inRect(cx, cy, x0, y0, x1, y1, 12)) return inRect(cx, cy, x0 + 3, y0 + 3, x1 - 3, y1 - 3, 10) ? mix(c, WHITE, 0.85) : c;
        return WHITE;
      }
      case 'photos': {
        const tile = 150;
        const gap = 14;
        const col = Math.floor((cx - 20) / (tile + gap));
        const row = Math.floor((cy - 20) / (tile + gap));
        const tx = cx - 20 - col * (tile + gap);
        const ty = cy - 20 - row * (tile + gap);
        if (cx < 20 || cy < 20 || tx >= tile || ty >= tile) return WHITE;
        const c = tones[(row * 5 + col) % tones.length];
        const sky = mix(c, WHITE, 0.55);
        if (ty > tile * 0.62 - Math.abs(tx - tile * 0.6) * 0.5) return mix(c, INK, 0.25);
        if (Math.hypot(tx - tile * 0.3, ty - tile * 0.3) < 14) return mix(sky, WHITE, 0.7);
        return sky;
      }
      case 'dialog': {
        const dx0 = cw / 2 - 220;
        const dy0 = ch / 2 - 150;
        if (inRect(cx, cy, dx0, dy0, dx0 + 440, dy0 + 300, 12)) {
          const lx = cx - dx0;
          const ly = cy - dy0;
          if (inRect(lx, ly, 28, 28, 230, 44, 4)) return INK;
          for (let i = 0; i < 3; i++) {
            const by = 80 + i * 56;
            if (inRect(lx, ly, 28, by, 412, by + 42, 8)) {
              if (i === 0) return inRect(lx, ly, 30, by + 2, 410, by + 40, 6) ? soft : accent;
              return inRect(lx, ly, 30, by + 2, 410, by + 40, 6) ? WHITE : LINE;
            }
          }
          if (inRect(lx, ly, 300, 254, 412, 284, 8)) return accent;
          return WHITE;
        }
        return mix(CANVAS, INK, 0.12);
      }
      case 'board': {
        for (let col = 0; col < 3; col++) {
          const x0 = 20 + col * 250;
          if (cx >= x0 && cx < x0 + 230) {
            if (inRect(cx, cy, x0, 20, x0 + 230, ch - 20, 10)) {
              if (inRect(cx, cy, x0 + 14, 34, x0 + 120, 46, 4)) return INK;
              for (let i = 0; i < 4 - col; i++) {
                const y0 = 64 + i * 96;
                if (inRect(cx, cy, x0 + 12, y0, x0 + 218, y0 + 84, 8)) {
                  if (inRect(cx, cy, x0 + 24, y0 + 14, x0 + 70, y0 + 22, 3)) return tones[(col + i) % tones.length];
                  if (inRect(cx, cy, x0 + 24, y0 + 34, x0 + 190 - i * 20, y0 + 42, 3)) return BAR;
                  return WHITE;
                }
              }
              return CANVAS;
            }
          }
        }
        return WHITE;
      }
    }
  });
}
