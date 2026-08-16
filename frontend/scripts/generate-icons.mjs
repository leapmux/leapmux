#!/usr/bin/env node

// Converts LeapMux SVG icons to web icon assets:
//   - leapmux-icon.ico (48x48 ICO, from rounded SVG)
//   - leapmux-icon.svg (copy of rounded SVG for modern browsers)
//   - leapmux-icon-192.png (192x192 PNG, from rounded SVG)
//   - leapmux-icon-512.png (512x512 PNG, from rounded SVG)
//   - leapmux-icon-maskable-512.png (512x512 PNG, from rounded SVG)
//   - leapmux-icon-square-apple-touch.png (180x180 PNG, from square SVG)
//
// Usage: node generate-icons.mjs <rounded-svg> <square-svg> <public-dir>
//
// Requires @resvg/resvg-js (installed with frontend dependencies).

import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { buildIco } from '../../scripts/build-ico.mjs'
import { renderPng } from '../../scripts/render-icon.mjs'

const [roundedSvgPath, squareSvgPath, publicDir] = process.argv.slice(2)
if (!roundedSvgPath || !squareSvgPath || !publicDir) {
  console.error('Usage: generate-icons.mjs <rounded-svg> <square-svg> <public-dir>')
  process.exit(1)
}

const roundedSvg = readFileSync(roundedSvgPath)
const squareSvg = readFileSync(squareSvgPath)

// Ensure the icons output directory exists before the first write.
mkdirSync(join(publicDir, 'icons'), { recursive: true })

// Copy the rounded SVG to public dir for modern browsers.
copyFileSync(roundedSvgPath, join(publicDir, 'icons', 'leapmux-icon.svg'))

// Generate a favicon ICO from the rounded SVG.
const ico48Png = renderPng(roundedSvg, 48)
writeFileSync(join(publicDir, 'icons', 'leapmux-icon.ico'), buildIco(ico48Png, 48))

// Generate rounded web icons and square Apple touch icon.
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-192.png'), renderPng(roundedSvg, 192, { opaqueCorners: false }))
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-512.png'), renderPng(roundedSvg, 512, { opaqueCorners: false }))
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-maskable-512.png'), renderPng(roundedSvg, 512, { opaqueCorners: false }))
writeFileSync(join(publicDir, 'icons', 'leapmux-icon-square-apple-touch.png'), renderPng(squareSvg, 180, { opaqueCorners: true }))

console.log('Generated web icons: leapmux-icon.ico, leapmux-icon.svg, leapmux-icon-192.png, leapmux-icon-512.png, leapmux-icon-maskable-512.png, leapmux-icon-square-apple-touch.png')
