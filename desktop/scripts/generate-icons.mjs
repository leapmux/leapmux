#!/usr/bin/env node

// Converts the LeapMux SVG icon to PNG (appicon) and ICO (Windows).
// Usage: node generate-icons.mjs <svg-path> <png-out> <ico-out>
//
// Requires @resvg/resvg-js (installed automatically when run via bunx).

import { readFileSync, writeFileSync } from 'node:fs';
import { Resvg } from '@resvg/resvg-js';

const [svgPath, pngOut, icoOut] = process.argv.slice(2);
if (!svgPath || !pngOut || !icoOut) {
  console.error('Usage: generate-icons.mjs <svg> <png-out> <ico-out>');
  process.exit(1);
}

const svg = readFileSync(svgPath);

// Render 1024x1024 PNG for appicon.
const appIcon = new Resvg(svg, { fitTo: { mode: 'width', value: 1024 } });
writeFileSync(pngOut, appIcon.render().asPng());

// Render 256x256 PNG, then wrap in ICO for Windows.
const icoIcon = new Resvg(svg, { fitTo: { mode: 'width', value: 256 } });
const icoPng = icoIcon.render().asPng();

// ICO format: header (6 bytes) + directory entry (16 bytes) + PNG data.
const header = Buffer.alloc(22);
header.writeUInt16LE(0, 0);              // reserved
header.writeUInt16LE(1, 2);              // type: ICO
header.writeUInt16LE(1, 4);              // image count
header[6] = 0;                           // width (0 = 256)
header[7] = 0;                           // height (0 = 256)
header[8] = 0;                           // color palette
header[9] = 0;                           // reserved
header.writeUInt16LE(1, 10);             // color planes
header.writeUInt16LE(32, 12);            // bits per pixel
header.writeUInt32LE(icoPng.length, 14); // image size
header.writeUInt32LE(22, 18);            // offset to image data

writeFileSync(icoOut, Buffer.concat([header, icoPng]));

console.log(`Generated ${pngOut} (1024x1024) and ${icoOut} (256x256 ICO)`);
