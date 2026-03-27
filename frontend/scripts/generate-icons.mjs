#!/usr/bin/env node

// Converts LeapMux SVG icons to web icon assets:
//   - leapmux-icon-corners.ico (48x48 ICO, from rounded SVG)
//   - leapmux-icon-corners.svg (copy of rounded SVG for modern browsers)
//   - leapmux-icon-corners-192.png (192x192 PNG, from rounded SVG)
//   - leapmux-icon-corners-512.png (512x512 PNG, from rounded SVG)
//   - leapmux-icon-corners-maskable-512.png (512x512 PNG, from rounded SVG)
//   - leapmux-icon-apple-touch.png (180x180 PNG, from square SVG)
//
// Usage: node generate-icons.mjs <rounded-svg> <square-svg> <public-dir>
//
// Requires @resvg/resvg-js (installed with frontend dependencies).

import { Buffer } from 'node:buffer'
import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { Resvg } from '@resvg/resvg-js'

const [roundedSvgPath, squareSvgPath, publicDir] = process.argv.slice(2)
if (!roundedSvgPath || !squareSvgPath || !publicDir) {
  console.error('Usage: generate-icons.mjs <rounded-svg> <square-svg> <public-dir>')
  process.exit(1)
}

const roundedSvg = readFileSync(roundedSvgPath)
const squareSvg = readFileSync(squareSvgPath)

function renderPng(svgData, size) {
  const resvg = new Resvg(svgData, { fitTo: { mode: 'width', value: size } })
  return resvg.render().asPng()
}

function buildIco(pngData, size) {
  // ICO format: header (6 bytes) + directory entry (16 bytes) + PNG data.
  const header = Buffer.alloc(22)
  header.writeUInt16LE(0, 0) // reserved
  header.writeUInt16LE(1, 2) // type: ICO
  header.writeUInt16LE(1, 4) // image count
  header[6] = size < 256 ? size : 0 // width (0 = 256)
  header[7] = size < 256 ? size : 0 // height (0 = 256)
  header[8] = 0 // color palette
  header[9] = 0 // reserved
  header.writeUInt16LE(1, 10) // color planes
  header.writeUInt16LE(32, 12) // bits per pixel
  header.writeUInt32LE(pngData.length, 14) // image size
  header.writeUInt32LE(22, 18) // offset to image data
  return Buffer.concat([header, pngData])
}

// Copy the rounded SVG to public dir for modern browsers.
rmSync(join(publicDir, 'favicon.ico'), { force: true })
rmSync(join(publicDir, 'favicon.svg'), { force: true })
copyFileSync(roundedSvgPath, join(publicDir, 'leapmux-icon-corners.svg'))

// Generate a favicon ICO from the rounded SVG.
const ico48Png = renderPng(roundedSvg, 48)
writeFileSync(join(publicDir, 'leapmux-icon-corners.ico'), buildIco(ico48Png, 48))

// Ensure the icons output directory exists.
mkdirSync(join(publicDir, 'icons'), { recursive: true })
rmSync(join(publicDir, 'icons', 'icon-192.png'), { force: true })
rmSync(join(publicDir, 'icons', 'icon-512.png'), { force: true })
rmSync(join(publicDir, 'icons', 'icon-512-maskable.png'), { force: true })
rmSync(join(publicDir, 'icons', 'apple-touch-icon.png'), { force: true })

// Generate rounded web icons and square Apple touch icon.
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-corners-192.png'), renderPng(roundedSvg, 192))
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-corners-512.png'), renderPng(roundedSvg, 512))
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-corners-maskable-512.png'), renderPng(roundedSvg, 512))
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-apple-touch.png'), renderPng(squareSvg, 180))

console.log('Generated web icons: leapmux-icon-corners.ico, leapmux-icon-corners.svg, leapmux-icon-corners-192.png, leapmux-icon-corners-512.png, leapmux-icon-corners-maskable-512.png, leapmux-icon-apple-touch.png')
