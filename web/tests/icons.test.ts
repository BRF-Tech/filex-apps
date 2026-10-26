// The tab icon: index.html names the favicon, the touch icon and the manifest,
// and every file it names is in public/ (the build copies public/ to the root
// the Go server serves; backend/internal/webui gives each its own type).
// Until 2026-10-06 there was none and /favicon.ico answered with the SPA.
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// vitest runs from web/ (vitest.config.ts).
const WEB = process.cwd();
const PUBLIC = join(WEB, 'public');
const html = readFileSync(join(WEB, 'index.html'), 'utf8');

function links(rel: string): string[] {
  const re = new RegExp(`<link\\s+rel="${rel}"[^>]*?href="([^"]+)"`, 'g');
  return [...html.matchAll(re)].map((m) => m[1]);
}

function file(href: string): Buffer {
  expect(href.startsWith('/'), `${href} is an absolute address`).toBe(true);
  const p = join(PUBLIC, href.slice(1));
  expect(existsSync(p), `${href} is in web/public`).toBe(true);
  return readFileSync(p);
}

// A PNG's width, height and colour type, from its IHDR chunk.
function png(b: Buffer): { w: number; h: number; alpha: boolean } {
  expect(b.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))).toBe(true);
  const colourType = b[25];
  return { w: b.readUInt32BE(16), h: b.readUInt32BE(20), alpha: colourType === 4 || colourType === 6 };
}

describe('the tab icon', () => {
  it('the head names an .ico for old browsers and the SVG for the rest', () => {
    expect(links('icon')).toEqual(['/favicon.ico', '/favicon.svg']);
    expect(html).toMatch(/<link rel="icon" href="\/favicon\.ico" sizes="32x32" \/>/);
    expect(html).toMatch(/<link rel="icon" href="\/favicon\.svg" type="image\/svg\+xml" \/>/);
  });

  it('favicon.ico is an icon file with the 16 and 32 pixel frames', () => {
    const b = file('/favicon.ico');
    expect(b.readUInt16LE(0)).toBe(0);
    expect(b.readUInt16LE(2)).toBe(1);
    const sizes = Array.from({ length: b.readUInt16LE(4) }, (_, i) => b[6 + 16 * i] || 256);
    expect(sizes).toEqual(expect.arrayContaining([16, 32]));
  });

  it('favicon.svg is an SVG with its own background, readable on a light or a dark tab', () => {
    const svg = file('/favicon.svg').toString('utf8');
    expect(svg).toContain('<svg xmlns="http://www.w3.org/2000/svg"');
    expect(svg).toMatch(/<rect width="32" height="32"[^>]*fill="#2f6ceb"/);
  });

  it('the touch icon is 180 pixels and opaque (iOS paints transparency black)', () => {
    const [href] = links('apple-touch-icon');
    expect(href).toBe('/apple-touch-icon.png');
    expect(png(file(href))).toEqual({ w: 180, h: 180, alpha: false });
  });

  it('the manifest names icons that exist, at the sizes they say', () => {
    const [href] = links('manifest');
    expect(href).toBe('/manifest.webmanifest');
    const manifest = JSON.parse(file(href).toString('utf8')) as {
      name: string;
      icons: { src: string; sizes: string; type: string }[];
    };
    expect(manifest.name).toBe('App store');
    expect(manifest.icons.length).toBeGreaterThan(0);
    for (const icon of manifest.icons) {
      expect(icon.type).toBe('image/png');
      const { w, h } = png(file(icon.src));
      expect(`${w}x${h}`).toBe(icon.sizes);
    }
  });
});
