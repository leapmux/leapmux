/**
 * Header-only intrinsic-dimension sniffing for inline (base64) images.
 *
 * The chat renders MCP image results as `<img src="data:...">`. Without
 * intrinsic dimensions the row's height changes when the image decodes,
 * which forces the virtualizer to re-measure the row and re-anchor the
 * scroll position. Sniffing width/height from the first bytes of the
 * payload lets the renderer reserve the exact final box up front, so the
 * decode is layout-neutral.
 *
 * Design constraints:
 *
 *   - Never decode the full payload (it can be megabytes). Each parser
 *     reads from a small decoded prefix; JPEG and ISOBMFF get larger
 *     budgets because EXIF/ICC segments (JPEG) or a fat `meta` box (AVIF)
 *     can push the needed bytes tens of KB into the file.
 *   - Wrong dimensions are worse than none: a wrong answer would reserve a
 *     wrong box. Every parser returns null on anything it cannot prove.
 *     AVIF in particular carries one `ispe` per item (alpha plane,
 *     thumbnails, grid tiles all have their own), so the parser resolves
 *     the PRIMARY item via `pitm` + `ipma` associations instead of naively
 *     taking the first `ispe`.
 *   - Rotation changes the displayed axes: JPEG EXIF orientations 5-8 and
 *     AVIF `irot` angles 90°/270° both swap width/height, and browsers
 *     apply them by default (`image-orientation: from-image`; MIAF
 *     transform properties). The sniffer swaps accordingly.
 *   - The ISOBMFF path accepts HEIC-family brands too, but the chat only
 *     allowlists AVIF today — HEIC isn't decodable outside Safari, so those
 *     results render as a placeholder, never an `<img>`.
 */

import { base64ToUint8Array } from './base64'

export interface ImageDimensions {
  width: number
  height: number
}

/** Decoded-prefix budget for formats whose header sits at a fixed offset. */
const FIXED_HEADER_BUDGET_BYTES = 64
/** Decoded-prefix budget for JPEG marker scanning (EXIF/ICC can be huge). */
const JPEG_SCAN_BUDGET_BYTES = 256 * 1024
/** Decoded-prefix budget for ISOBMFF (AVIF/HEIF) box walking. */
const ISOBMFF_SCAN_BUDGET_BYTES = 64 * 1024
/** Dimensions above this are treated as corrupt headers, not real images. */
const MAX_SANE_DIMENSION_PX = 65535

/**
 * Sniff intrinsic dimensions from a `data:<mime>;base64,<payload>` URL.
 * Returns null for non-base64 data URLs, unsupported formats, and any
 * payload whose header cannot be parsed with certainty.
 */
export function sniffImageDimensionsFromDataUrl(dataUrl: string): ImageDimensions | null {
  if (!dataUrl.startsWith('data:'))
    return null
  const comma = dataUrl.indexOf(',')
  if (comma < 0)
    return null
  const meta = dataUrl.slice(5, comma)
  if (!meta.toLowerCase().endsWith(';base64'))
    return null
  return sniffImageDimensionsFromBase64(dataUrl.slice(comma + 1))
}

/** Sniff intrinsic dimensions from a bare base64 payload. */
export function sniffImageDimensionsFromBase64(base64: string): ImageDimensions | null {
  // Cheap fixed-offset formats first; fall through to the JPEG scan only
  // when the signature says JPEG (0xFF 0xD8), so non-JPEG payloads never
  // pay the larger decode budget.
  const head = decodeBase64Prefix(base64, FIXED_HEADER_BUDGET_BYTES)
  if (!head)
    return null
  const fixed = parsePng(head) ?? parseGif(head) ?? parseWebp(head)
  if (fixed)
    return sane(fixed)
  if (head.length >= 2 && head[0] === 0xFF && head[1] === 0xD8) {
    const jpegBytes = decodeUpToBudget(base64, head, JPEG_SCAN_BUDGET_BYTES)
    if (!jpegBytes)
      return null
    const jpeg = parseJpeg(jpegBytes)
    return jpeg ? sane(jpeg) : null
  }
  // ISOBMFF (AVIF/HEIF): an `ftyp` box type at offset 4.
  if (head.length >= 12 && head[4] === 0x66 && head[5] === 0x74 && head[6] === 0x79 && head[7] === 0x70) {
    const boxBytes = decodeUpToBudget(base64, head, ISOBMFF_SCAN_BUDGET_BYTES)
    if (!boxBytes)
      return null
    const isobmff = parseIsobmff(boxBytes)
    return isobmff ? sane(isobmff) : null
  }
  return null
}

/** Re-decode with a larger budget, reusing the head when the payload is short. */
function decodeUpToBudget(base64: string, head: Uint8Array, budgetBytes: number): Uint8Array | null {
  return base64.length > prefixCharsForBytes(FIXED_HEADER_BUDGET_BYTES)
    ? decodeBase64Prefix(base64, budgetBytes)
    : head
}

/** Reject zero/absurd dimensions produced by corrupt headers. */
function sane(dims: ImageDimensions): ImageDimensions | null {
  const { width, height } = dims
  if (!Number.isInteger(width) || !Number.isInteger(height))
    return null
  if (width <= 0 || height <= 0 || width > MAX_SANE_DIMENSION_PX || height > MAX_SANE_DIMENSION_PX)
    return null
  return dims
}

/** Base64 chars needed to cover `bytes` decoded bytes (4 chars → 3 bytes). */
function prefixCharsForBytes(bytes: number): number {
  return Math.ceil(bytes / 3) * 4
}

/**
 * Decode up to `maxBytes` from the front of a base64 string. Slices on a
 * 4-char boundary so the prefix is independently decodable, and strips a
 * trailing partial quantum rather than failing on it.
 */
function decodeBase64Prefix(base64: string, maxBytes: number): Uint8Array | null {
  let prefix = base64.slice(0, prefixCharsForBytes(maxBytes))
  // A whole-string slice may end mid-quantum (length not divisible by 4)
  // when the payload itself is shorter than the budget; drop the remainder.
  const partial = prefix.length % 4
  if (partial > 0)
    prefix = prefix.slice(0, prefix.length - partial)
  if (prefix.length === 0)
    return null
  try {
    // Shared decode (base64ToUint8Array): atob + per-char Uint8Array fill, single-sourced
    // so this header sniffer can't drift from the rest of the codebase's base64 handling.
    return base64ToUint8Array(prefix)
  }
  catch {
    // atob throws on a malformed 4-char quantum -- treat as unsniffable.
    return null
  }
}

function readU16BE(b: Uint8Array, at: number): number {
  return (b[at] << 8) | b[at + 1]
}

function readU16LE(b: Uint8Array, at: number): number {
  return b[at] | (b[at + 1] << 8)
}

function readU32LE(b: Uint8Array, at: number): number {
  return (b[at] | (b[at + 1] << 8) | (b[at + 2] << 16) | (b[at + 3] << 24)) >>> 0
}

function readU32BE(b: Uint8Array, at: number): number {
  // >>> 0 keeps the top bit unsigned.
  return ((b[at] << 24) | (b[at + 1] << 16) | (b[at + 2] << 8) | b[at + 3]) >>> 0
}

/** PNG: 8-byte signature, then the IHDR chunk holds width/height at 16/20. */
function parsePng(b: Uint8Array): ImageDimensions | null {
  if (b.length < 24)
    return null
  const SIG = [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]
  for (let i = 0; i < SIG.length; i++) {
    if (b[i] !== SIG[i])
      return null
  }
  // Bytes 12-15 must be the IHDR chunk type; a PNG's first chunk is always IHDR.
  if (b[12] !== 0x49 || b[13] !== 0x48 || b[14] !== 0x44 || b[15] !== 0x52)
    return null
  if (readU32BE(b, 8) !== 13)
    return null
  return { width: readU32BE(b, 16), height: readU32BE(b, 20) }
}

/** GIF: "GIF87a"/"GIF89a", then the logical screen descriptor (LE16 pair). */
function parseGif(b: Uint8Array): ImageDimensions | null {
  if (b.length < 10)
    return null
  if (b[0] !== 0x47 || b[1] !== 0x49 || b[2] !== 0x46 || b[3] !== 0x38)
    return null
  if ((b[4] !== 0x37 && b[4] !== 0x39) || b[5] !== 0x61)
    return null
  return { width: readU16LE(b, 6), height: readU16LE(b, 8) }
}

/**
 * WebP: RIFF container. The first chunk decides the flavor:
 *   - VP8X (extended): 24-bit LE canvas size minus one at 24/27.
 *   - VP8  (lossy): frame start code 9D 01 2A at 23, then LE14 pairs.
 *   - VP8L (lossless): signature 2F at 20, then a bit-packed LE14 pair.
 */
function parseWebp(b: Uint8Array): ImageDimensions | null {
  if (b.length < 20)
    return null
  // "RIFF" .... "WEBP"
  if (b[0] !== 0x52 || b[1] !== 0x49 || b[2] !== 0x46 || b[3] !== 0x46)
    return null
  if (b[8] !== 0x57 || b[9] !== 0x45 || b[10] !== 0x42 || b[11] !== 0x50)
    return null
  const chunk = String.fromCharCode(b[12], b[13], b[14], b[15])
  const chunkPayloadStart = 20
  const chunkPayloadEnd = chunkPayloadStart + readU32LE(b, 16)
  const canReadChunkBytes = (at: number, len: number) =>
    at >= chunkPayloadStart && at + len <= b.length && at + len <= chunkPayloadEnd
  if (chunk === 'VP8X') {
    if (!canReadChunkBytes(24, 6))
      return null
    const width = 1 + (b[24] | (b[25] << 8) | (b[26] << 16))
    const height = 1 + (b[27] | (b[28] << 8) | (b[29] << 16))
    return { width, height }
  }
  if (chunk === 'VP8 ') {
    if (!canReadChunkBytes(20, 10))
      return null
    if (b[23] !== 0x9D || b[24] !== 0x01 || b[25] !== 0x2A)
      return null
    return { width: readU16LE(b, 26) & 0x3FFF, height: readU16LE(b, 28) & 0x3FFF }
  }
  if (chunk === 'VP8L') {
    if (!canReadChunkBytes(20, 5))
      return null
    if (b[20] !== 0x2F)
      return null
    const bits = b[21] | (b[22] << 8) | (b[23] << 16) | (b[24] << 24)
    return { width: 1 + (bits & 0x3FFF), height: 1 + ((bits >> 14) & 0x3FFF) }
  }
  return null
}

/**
 * JPEG: walk the marker segments from SOI until a start-of-frame marker
 * yields the coded dimensions. EXIF orientations 5-8 (the 90° family) swap
 * the displayed axes, so an APP1 EXIF segment is parsed along the way.
 * Returns null when the scan budget runs out before a SOF — guessing here
 * would reserve a wrong box.
 */
function parseJpeg(b: Uint8Array): ImageDimensions | null {
  if (b.length < 4 || b[0] !== 0xFF || b[1] !== 0xD8)
    return null
  let orientation = 1
  let pos = 2
  while (pos + 4 <= b.length) {
    if (b[pos] !== 0xFF)
      return null
    // Skip fill bytes (padding 0xFF runs before a marker are legal).
    let markerAt = pos
    while (markerAt + 1 < b.length && b[markerAt + 1] === 0xFF)
      markerAt++
    if (markerAt + 1 >= b.length)
      return null
    const marker = b[markerAt + 1]
    // Standalone markers with no length field.
    if (marker === 0x01 || (marker >= 0xD0 && marker <= 0xD7)) {
      pos = markerAt + 2
      continue
    }
    // SOS (entropy-coded data follows) or EOI without a prior SOF: give up.
    if (marker === 0xDA || marker === 0xD9)
      return null
    if (markerAt + 4 > b.length)
      return null
    const segmentLen = readU16BE(b, markerAt + 2)
    if (segmentLen < 2)
      return null
    const segmentEnd = markerAt + 2 + segmentLen
    if (segmentEnd > b.length)
      return null
    const isSof = marker >= 0xC0 && marker <= 0xCF
      && marker !== 0xC4 && marker !== 0xC8 && marker !== 0xCC
    if (isSof) {
      if (markerAt + 9 > segmentEnd)
        return null
      const height = readU16BE(b, markerAt + 5)
      const width = readU16BE(b, markerAt + 7)
      return orientation >= 5 && orientation <= 8
        ? { width: height, height: width }
        : { width, height }
    }
    if (marker === 0xE1) {
      const payloadStart = markerAt + 4
      orientation = parseExifOrientation(b, payloadStart, segmentEnd) ?? orientation
    }
    pos = segmentEnd
  }
  return null
}

/** A parsed ISOBMFF box: `payload` skips the 8/16-byte header. */
interface IsobmffBox {
  type: string
  start: number
  payload: number
  end: number
}

/** Brands whose files use the HEIF image structure this parser understands. */
const ISOBMFF_IMAGE_BRANDS = new Set(['avif', 'avis', 'heic', 'heix', 'hevc', 'hevx', 'mif1', 'msf1', 'miaf'])

/**
 * Walk the child boxes of [start, end). Stops cleanly at the end of the
 * decoded prefix — boxes beyond it are simply not yielded, so lookups fail
 * closed (null) instead of misparsing truncated data.
 */
function walkBoxes(b: Uint8Array, start: number, end: number): IsobmffBox[] {
  const boxes: IsobmffBox[] = []
  const limit = Math.min(end, b.length)
  let pos = start
  while (pos + 8 <= limit) {
    const size = readU32BE(b, pos)
    const type = String.fromCharCode(b[pos + 4], b[pos + 5], b[pos + 6], b[pos + 7])
    let headerLen = 8
    let boxEnd: number
    if (size === 0) {
      // Extends to the end of the enclosing space.
      boxEnd = end
    }
    else if (size === 1) {
      // 64-bit largesize. Anything above 32 bits is beyond any sane image.
      if (pos + 16 > limit || readU32BE(b, pos + 8) !== 0)
        break
      headerLen = 16
      boxEnd = pos + readU32BE(b, pos + 12)
    }
    else {
      boxEnd = pos + size
    }
    if (boxEnd < pos + headerLen)
      break
    if (boxEnd > limit)
      break
    boxes.push({ type, start: pos, payload: pos + headerLen, end: boxEnd })
    if (boxEnd <= pos)
      break
    pos = boxEnd
  }
  return boxes
}

/**
 * AVIF/HEIF: resolve the PRIMARY item's `ispe` (spatial extents) through
 * `pitm` + `ipma`, applying an associated `irot` (90°/270° swap). Falls
 * back to the sole `ispe` when the file has exactly one — a file with one
 * sized item can't be describing anything else. An associated `clap`
 * (clean-aperture crop) changes the displayed size in ways this parser
 * doesn't model, so its presence bails.
 */
function parseIsobmff(b: Uint8Array): ImageDimensions | null {
  const topLevel = walkBoxes(b, 0, b.length)
  const ftyp = topLevel[0]
  if (!ftyp || ftyp.type !== 'ftyp' || ftyp.payload + 4 > b.length)
    return null
  const brands: string[] = []
  for (let at = ftyp.payload; at + 4 <= Math.min(ftyp.end, b.length); at += 4) {
    if (at === ftyp.payload + 4)
      continue // minor_version, not a brand
    brands.push(String.fromCharCode(b[at], b[at + 1], b[at + 2], b[at + 3]))
  }
  if (!brands.some(brand => ISOBMFF_IMAGE_BRANDS.has(brand)))
    return null

  const meta = topLevel.find(box => box.type === 'meta')
  if (!meta)
    return null
  if (meta.payload + 4 > meta.end)
    return null
  // meta is a FullBox: 4 bytes of version/flags precede its children.
  const metaChildren = walkBoxes(b, meta.payload + 4, meta.end)
  const iprp = metaChildren.find(box => box.type === 'iprp')
  if (!iprp)
    return null
  const iprpChildren = walkBoxes(b, iprp.payload, iprp.end)
  const ipco = iprpChildren.find(box => box.type === 'ipco')
  if (!ipco)
    return null
  // Property indices in ipma are 1-based positions in ipco.
  const properties = walkBoxes(b, ipco.payload, ipco.end)

  const readIspe = (box: IsobmffBox): ImageDimensions | null => {
    // FullBox: version/flags, then width/height as u32.
    if (box.payload + 12 > Math.min(box.end, b.length))
      return null
    return { width: readU32BE(b, box.payload + 4), height: readU32BE(b, box.payload + 8) }
  }
  const irotSwaps = (box: IsobmffBox): boolean | null => {
    if (box.payload + 1 > Math.min(box.end, b.length))
      return null
    return ((b[box.payload] & 0x03) & 1) === 1
  }

  const associated = resolvePrimaryItemProperties(b, metaChildren, iprp, properties)
  const candidates = associated
    // Fallback: no resolvable associations — trust a sole ispe.
    ?? (properties.filter(p => p.type === 'ispe').length === 1
      && properties.filter(p => p.type === 'irot').length <= 1
      ? properties.filter(p => p.type === 'ispe' || p.type === 'irot' || p.type === 'clap')
      : null)
  if (!candidates)
    return null
  if (candidates.some(p => p.type === 'clap'))
    return null
  const ispe = candidates.find(p => p.type === 'ispe')
  if (!ispe)
    return null
  const dims = readIspe(ispe)
  if (!dims)
    return null
  const irot = candidates.find(p => p.type === 'irot')
  if (!irot)
    return dims
  const swaps = irotSwaps(irot)
  if (swaps === null)
    return null
  return swaps ? { width: dims.height, height: dims.width } : dims
}

/**
 * The ipco properties associated with the pitm primary item, resolved
 * through every ipma box. Null when pitm/ipma are missing or truncated.
 */
function resolvePrimaryItemProperties(
  b: Uint8Array,
  metaChildren: IsobmffBox[],
  iprp: IsobmffBox,
  properties: IsobmffBox[],
): IsobmffBox[] | null {
  const pitm = metaChildren.find(box => box.type === 'pitm')
  if (!pitm || pitm.payload + 6 > Math.min(pitm.end, b.length))
    return null
  const pitmVersion = b[pitm.payload]
  if (pitmVersion >= 1 && pitm.payload + 8 > Math.min(pitm.end, b.length))
    return null
  const primaryId = pitmVersion === 0 ? readU16BE(b, pitm.payload + 4) : readU32BE(b, pitm.payload + 4)

  for (const ipma of walkBoxes(b, iprp.payload, iprp.end)) {
    if (ipma.type !== 'ipma')
      continue
    const ipmaEnd = Math.min(ipma.end, b.length)
    if (ipma.payload + 8 > ipmaEnd)
      return null
    const version = b[ipma.payload]
    const flags = (b[ipma.payload + 1] << 16) | (b[ipma.payload + 2] << 8) | b[ipma.payload + 3]
    const wideIndices = (flags & 1) === 1
    const entryCount = readU32BE(b, ipma.payload + 4)
    let at = ipma.payload + 8
    for (let entry = 0; entry < entryCount; entry++) {
      const idLen = version < 1 ? 2 : 4
      if (at + idLen + 1 > ipmaEnd)
        return null
      const itemId = version < 1 ? readU16BE(b, at) : readU32BE(b, at)
      at += idLen
      const associationCount = b[at]
      at += 1
      const indices: number[] = []
      for (let assoc = 0; assoc < associationCount; assoc++) {
        const assocLen = wideIndices ? 2 : 1
        if (at + assocLen > ipmaEnd)
          return null
        indices.push(wideIndices ? readU16BE(b, at) & 0x7FFF : b[at] & 0x7F)
        at += assocLen
      }
      if (itemId === primaryId) {
        const resolved: IsobmffBox[] = []
        for (const index of indices) {
          // Index 0 means "no property"; valid indices are 1-based.
          if (index === 0)
            continue
          if (index > properties.length)
            return null
          resolved.push(properties[index - 1])
        }
        return resolved
      }
    }
  }
  return null
}

/**
 * Extract the Orientation tag (0x0112) from an APP1 EXIF payload. Returns
 * null when the payload isn't EXIF or the tag is absent/unreadable — the
 * caller keeps the default orientation 1, matching how browsers treat
 * images without a usable tag.
 */
function parseExifOrientation(b: Uint8Array, start: number, end: number): number | null {
  // "Exif\0\0" preamble.
  if (start + 6 > end)
    return null
  if (b[start] !== 0x45 || b[start + 1] !== 0x78 || b[start + 2] !== 0x69 || b[start + 3] !== 0x66 || b[start + 4] !== 0x00 || b[start + 5] !== 0x00)
    return null
  const tiff = start + 6
  if (tiff + 8 > end)
    return null
  const little = b[tiff] === 0x49 && b[tiff + 1] === 0x49
  const big = b[tiff] === 0x4D && b[tiff + 1] === 0x4D
  if (!little && !big)
    return null
  const u16 = (at: number) => little ? readU16LE(b, at) : readU16BE(b, at)
  const u32 = (at: number) => little
    ? (b[at] | (b[at + 1] << 8) | (b[at + 2] << 16) | (b[at + 3] << 24)) >>> 0
    : readU32BE(b, at)
  if (u16(tiff + 2) !== 42)
    return null
  const ifdOffset = u32(tiff + 4)
  const ifd = tiff + ifdOffset
  if (ifd + 2 > end)
    return null
  const entryCount = u16(ifd)
  for (let i = 0; i < entryCount; i++) {
    const entry = ifd + 2 + i * 12
    if (entry + 12 > end)
      return null
    if (u16(entry) !== 0x0112)
      continue
    // Orientation is a SHORT (type 3) with count 1; the value lives inline
    // in the first two bytes of the 4-byte value field.
    if (u16(entry + 2) !== 3)
      return null
    const value = u16(entry + 8)
    return value >= 1 && value <= 8 ? value : null
  }
  return null
}
