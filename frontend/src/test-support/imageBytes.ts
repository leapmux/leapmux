/**
 * Byte builders for image files, in the shapes that the header sniffer of
 * `~/lib/imageDimensions` reads. A test builds the bytes of an image here rather than
 * checking in a binary fixture.
 */

/** The char codes of `s`, one byte for each character. */
export function chars(s: string): number[] {
  return [...s].map(c => c.charCodeAt(0))
}

/** The two bytes of `v`, big-endian. */
export function u16be(v: number): number[] {
  return [(v >>> 8) & 0xFF, v & 0xFF]
}

/** The four bytes of `v`, big-endian. */
export function u32be(v: number): number[] {
  return [
    (v >>> 24) & 0xFF,
    (v >>> 16) & 0xFF,
    (v >>> 8) & 0xFF,
    v & 0xFF,
  ]
}

/** SOI + optional leading segments + SOF0 carrying the coded dimensions. */
export function jpegBytes(width: number, height: number, leading: number[] = []): number[] {
  return [
    0xFF,
    0xD8,
    ...leading,
    0xFF,
    0xC0,
    ...u16be(17),
    0x08,
    ...u16be(height),
    ...u16be(width),
    0x03,
    0x01,
    0x11,
    0x00,
    0x02,
    0x11,
    0x01,
    0x03,
    0x11,
    0x01,
  ]
}

/** One ISOBMFF box of `type` that holds `contents`. */
export function box(type: string, ...contents: number[][]): number[] {
  const payload = contents.flat()
  return [
    ...u32be(8 + payload.length),
    ...chars(type),
    ...payload,
  ]
}

/** One ISOBMFF full box: a box whose payload starts with a version and flags. */
export function fullBox(type: string, version: number, flags: number, ...contents: number[][]): number[] {
  return box(type, [
    version,
    (flags >>> 16) & 0xFF,
    (flags >>> 8) & 0xFF,
    flags & 0xFF,
  ], ...contents)
}

/** The `ispe` box of an image: its width and its height. */
export function ispeBox(width: number, height: number): number[] {
  return fullBox('ispe', 0, 0, u32be(width), u32be(height))
}
