#!/usr/bin/env node

import { inflateSync } from 'node:zlib'
import { readFileSync } from 'node:fs'
import process from 'node:process'

const opaqueFiles = []
const transparentFiles = []

for (let i = 2; i < process.argv.length; i += 1) {
  const arg = process.argv[i]
  if (arg === '--opaque') {
    opaqueFiles.push(process.argv[++i])
  } else if (arg === '--transparent') {
    transparentFiles.push(process.argv[++i])
  } else {
    console.error(`Unknown argument: ${arg}`)
    process.exit(1)
  }
}

if (opaqueFiles.length === 0 && transparentFiles.length === 0) {
  console.error('Usage: node scripts/validate-icons.mjs [--opaque <png>]... [--transparent <png>]...')
  process.exit(1)
}

for (const file of opaqueFiles) {
  assertCornerAlpha(file, true)
}

for (const file of transparentFiles) {
  assertCornerAlpha(file, false)
}

console.log('Validated icon corner alpha successfully')

function assertCornerAlpha(filePath, shouldBeOpaque) {
  const { width, height, pixels } = decodeRgbaPng(readFileSync(filePath))
  const corners = [
    [0, 0],
    [width - 1, 0],
    [0, height - 1],
    [width - 1, height - 1],
  ]

  for (const [x, y] of corners) {
    const alpha = pixels[(y * width + x) * 4 + 3]
    const ok = shouldBeOpaque ? alpha === 255 : alpha === 0
    if (!ok) {
      const expected = shouldBeOpaque ? 'opaque' : 'transparent'
      throw new Error(`${filePath} has alpha=${alpha} at ${x},${y}; expected ${expected} corners`)
    }
  }
}

function decodeRgbaPng(buffer) {
  const signature = '89504e470d0a1a0a'
  if (buffer.subarray(0, 8).toString('hex') !== signature) {
    throw new Error('Invalid PNG signature')
  }

  let offset = 8
  let width = 0
  let height = 0
  let bitDepth = 0
  let colorType = 0
  let interlace = 0
  const idatChunks = []

  while (offset < buffer.length) {
    const length = buffer.readUInt32BE(offset)
    offset += 4
    const type = buffer.subarray(offset, offset + 4).toString('ascii')
    offset += 4
    const data = buffer.subarray(offset, offset + length)
    offset += length
    offset += 4 // crc

    if (type === 'IHDR') {
      width = data.readUInt32BE(0)
      height = data.readUInt32BE(4)
      bitDepth = data[8]
      colorType = data[9]
      interlace = data[12]
    } else if (type === 'IDAT') {
      idatChunks.push(data)
    } else if (type === 'IEND') {
      break
    }
  }

  if (bitDepth !== 8 || colorType !== 6 || interlace !== 0) {
    throw new Error(`Unsupported PNG format: bitDepth=${bitDepth}, colorType=${colorType}, interlace=${interlace}`)
  }

  const stride = width * 4
  const inflated = inflateSync(Buffer.concat(idatChunks))
  const pixels = Buffer.alloc(width * height * 4)
  let srcOffset = 0

  for (let y = 0; y < height; y += 1) {
    const filter = inflated[srcOffset]
    srcOffset += 1
    const row = inflated.subarray(srcOffset, srcOffset + stride)
    srcOffset += stride
    const decoded = unfilterRow(filter, row, pixels, y, stride)
    decoded.copy(pixels, y * stride)
  }

  return { width, height, pixels }
}

function unfilterRow(filter, row, pixels, y, stride) {
  const out = Buffer.alloc(stride)
  switch (filter) {
    case 0:
      row.copy(out)
      return out
    case 1:
      for (let i = 0; i < stride; i += 1) {
        const left = i >= 4 ? out[i - 4] : 0
        out[i] = (row[i] + left) & 0xFF
      }
      return out
    case 2:
      for (let i = 0; i < stride; i += 1) {
        const up = y > 0 ? pixels[(y - 1) * stride + i] : 0
        out[i] = (row[i] + up) & 0xFF
      }
      return out
    case 3:
      for (let i = 0; i < stride; i += 1) {
        const left = i >= 4 ? out[i - 4] : 0
        const up = y > 0 ? pixels[(y - 1) * stride + i] : 0
        out[i] = (row[i] + Math.floor((left + up) / 2)) & 0xFF
      }
      return out
    case 4:
      for (let i = 0; i < stride; i += 1) {
        const left = i >= 4 ? out[i - 4] : 0
        const up = y > 0 ? pixels[(y - 1) * stride + i] : 0
        const upLeft = y > 0 && i >= 4 ? pixels[(y - 1) * stride + i - 4] : 0
        out[i] = (row[i] + paeth(left, up, upLeft)) & 0xFF
      }
      return out
    default:
      throw new Error(`Unsupported PNG filter: ${filter}`)
  }
}

function paeth(a, b, c) {
  const p = a + b - c
  const pa = Math.abs(p - a)
  const pb = Math.abs(p - b)
  const pc = Math.abs(p - c)
  if (pa <= pb && pa <= pc) return a
  if (pb <= pc) return b
  return c
}
