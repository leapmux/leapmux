import { Buffer } from 'node:buffer'

// Wraps raw PNG data in a single-image ICO container.
// ICO format: header (6 bytes) + directory entry (16 bytes) + PNG data.
export function buildIco(pngData, size) {
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
