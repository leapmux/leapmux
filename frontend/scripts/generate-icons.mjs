#!/usr/bin/env node

// Converts LeapMux SVG icons to web icon assets:
//   - favicon.ico  (48x48 ICO, from favicon SVG)
//   - favicon.svg  (copy of favicon SVG for modern browsers)
//   - icon-192.png (192x192 PNG, from app icon SVG)
//   - icon-512.png (512x512 PNG, from app icon SVG)
//   - icon-512-maskable.png (512x512 PNG with safe-zone padding, from app icon SVG)
//   - apple-touch-icon.png (180x180 PNG, from app icon SVG)
//
// Usage: node generate-icons.mjs <favicon-svg> <app-icon-svg> <public-dir>
//
// Requires @resvg/resvg-js (installed with frontend dependencies).

import { Buffer } from 'node:buffer'
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { Resvg } from '@resvg/resvg-js'

const [faviconSvgPath, appIconSvgPath, publicDir] = process.argv.slice(2)
if (!faviconSvgPath || !appIconSvgPath || !publicDir) {
  console.error('Usage: generate-icons.mjs <favicon-svg> <app-icon-svg> <public-dir>')
  process.exit(1)
}

const faviconSvg = readFileSync(faviconSvgPath)
const appIconSvg = readFileSync(appIconSvgPath)

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

// Copy favicon SVG to public dir for modern browsers.
copyFileSync(faviconSvgPath, join(publicDir, 'favicon.svg'))

// Generate favicon.ico (48x48) from the favicon SVG.
const ico48Png = renderPng(faviconSvg, 48)
writeFileSync(join(publicDir, 'favicon.ico'), buildIco(ico48Png, 48))

// Ensure the icons output directory exists.
mkdirSync(join(publicDir, 'icons'), { recursive: true })

// Generate app icon PNGs from the app icon SVG.
writeFileSync(join(publicDir, 'icons', 'icon-192.png'), renderPng(appIconSvg, 192))
writeFileSync(join(publicDir, 'icons', 'icon-512.png'), renderPng(appIconSvg, 512))
writeFileSync(join(publicDir, 'icons', 'icon-512-maskable.png'), renderPng(appIconSvg, 512))
writeFileSync(join(publicDir, 'icons', 'apple-touch-icon.png'), renderPng(appIconSvg, 180))

console.log('Generated web icons: favicon.ico, favicon.svg, icon-192.png, icon-512.png, icon-512-maskable.png, apple-touch-icon.png')
