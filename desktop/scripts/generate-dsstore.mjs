#!/usr/bin/env node
//
// Generates a .DS_Store file for DMG styling.
//
// Usage: generate-dsstore.mjs <volume-path> <output-path>
//        [--bg-image <path>] [--bg-color <r,g,b>]
//        [--icon-size <px>] [--text-size <px>]
//        [--window-pos <x,y>] [--window-size <w,h>]
//        [--icon <name,x,y>]...
//
// Example:
//   generate-dsstore.mjs "/Volumes/MyApp 1.0" out/.DS_Store \
//     --bg-image "/Volumes/MyApp 1.0/.background/bg@2x.png" \
//     --icon-size 128 --text-size 14 \
//     --window-pos 100,100 --window-size 540,360 \
//     --icon "MyApp.app,130,150" \
//     --icon "Applications,410,150"

import { createRequire } from 'node:module';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const frontendDir = resolve(__dirname, '../../frontend');
const require = createRequire(resolve(frontendDir, 'package.json'));
const DSStore = require('ds-store');

const args = process.argv.slice(2);
if (args.length < 2) {
  console.error('Usage: generate-dsstore.mjs <volume-path> <output-path> [options]');
  process.exit(1);
}

const volumePath = args[0];
const outputPath = args[1];

// Defaults
let bgImage = null;
let bgColor = null;
let iconSize = 128;
let textSize = 14;
let windowPos = [100, 100];
let windowSize = [540, 360];
const icons = [];

// Parse options
for (let i = 2; i < args.length; i++) {
  switch (args[i]) {
    case '--bg-image':
      bgImage = args[++i];
      break;
    case '--bg-color':
      bgColor = args[++i].split(',').map(Number);
      break;
    case '--icon-size':
      iconSize = Number(args[++i]);
      break;
    case '--text-size':
      textSize = Number(args[++i]);
      break;
    case '--window-pos':
      windowPos = args[++i].split(',').map(Number);
      break;
    case '--window-size':
      windowSize = args[++i].split(',').map(Number);
      break;
    case '--icon': {
      const parts = args[++i].split(',');
      icons.push({ name: parts[0], x: Number(parts[1]), y: Number(parts[2]) });
      break;
    }
    default:
      console.error(`Unknown option: ${args[i]}`);
      process.exit(1);
  }
}

const store = new DSStore();

store.setIconSize(iconSize);
store.setWindowPos(windowPos[0], windowPos[1]);
store.setWindowSize(windowSize[0], windowSize[1]);
store.vSrn(1);

if (bgImage) {
  store.setBackgroundPath(bgImage);
} else if (bgColor) {
  store.setBackgroundColor(bgColor[0], bgColor[1], bgColor[2]);
} else {
  // Default: warm sand color
  store.setBackgroundColor(0.961, 0.945, 0.922);
}

for (const icon of icons) {
  store.setIconPos(icon.name, icon.x, icon.y);
}

store.write(outputPath, (err) => {
  if (err) {
    console.error('Error writing .DS_Store:', err.message);
    process.exit(1);
  }
  console.log(`Generated: ${outputPath}`);
});
