#!/usr/bin/env node
//
// Generates a .DS_Store file for DMG styling.
//
// Usage: generate-dsstore.mjs <output-path>
//        [--bg-image <path>] [--bg-color <r,g,b>]
//        [--icon-size <px>]
//        [--window-pos <x,y>] [--window-size <w,h>]
//        [--icon <name,x,y>]...
//
// Example:
//   generate-dsstore.mjs out/.DS_Store \
//     --bg-image "/Volumes/MyApp 1.0/.background/bg@2x.png" \
//     --icon-size 128 \
//     --window-pos 100,100 --window-size 540,360 \
//     --icon "MyApp.app,130,150" \
//     --icon "Applications,410,150"
//
// There is no text-size option, although a DMG window does have a label size:
// the ds-store package writes the settings through its Helper API, which offers
// setIconSize, setIconPos, setWindowPos, setWindowSize, and the two background
// setters, and no setter for the label size.

import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const frontendDir = resolve(__dirname, '../../../frontend')
const require = createRequire(resolve(frontendDir, 'package.json'))
const DSStore = require('ds-store')

const args = process.argv.slice(2)
if (args.length < 1) {
  console.error('Usage: generate-dsstore.mjs <output-path> [options]')
  process.exit(1)
}

const outputPath = args[0]

// Defaults
let bgImage = null
let bgColor = null
let iconSize = 128
let windowPos = [100, 100]
let windowSize = [540, 360]
const icons = []

// Parse options
for (let i = 1; i < args.length; i++) {
  switch (args[i]) {
    case '--bg-image':
      bgImage = args[++i]
      break
    case '--bg-color':
      bgColor = args[++i].split(',').map(Number)
      break
    case '--icon-size':
      iconSize = Number(args[++i])
      break
    case '--window-pos':
      windowPos = args[++i].split(',').map(Number)
      break
    case '--window-size':
      windowSize = args[++i].split(',').map(Number)
      break
    case '--icon': {
      const parts = args[++i].split(',')
      icons.push({ name: parts[0], x: Number(parts[1]), y: Number(parts[2]) })
      break
    }
    default:
      console.error(`Unknown option: ${args[i]}`)
      process.exit(1)
  }
}

const store = new DSStore()

store.setIconSize(iconSize)
store.setWindowPos(windowPos[0], windowPos[1])
store.setWindowSize(windowSize[0], windowSize[1])
store.vSrn(1)

if (bgImage) {
  store.setBackgroundPath(bgImage)
}
else if (bgColor) {
  store.setBackgroundColor(bgColor[0], bgColor[1], bgColor[2])
}
else {
  // Default: warm sand color
  store.setBackgroundColor(0.961, 0.945, 0.922)
}

for (const icon of icons) {
  store.setIconPos(icon.name, icon.x, icon.y)
}

store.write(outputPath, (err) => {
  if (err) {
    console.error('Error writing .DS_Store:', err.message)
    process.exit(1)
  }
  console.log(`Generated: ${outputPath}`)
})
