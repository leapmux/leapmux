/**
 * A minimal PNG (signature + IHDR only) declaring the given dimensions.
 *
 * Enough for the header sniffer in `~/lib/imageDimensions`, which reads the
 * first 24 bytes and never decodes pixels. Tests that need a sniffable image
 * use this rather than checking in a binary fixture.
 */
export function pngBase64(width: number, height: number): string {
  const u32 = (v: number) => [(v >>> 24) & 0xFF, (v >>> 16) & 0xFF, (v >>> 8) & 0xFF, v & 0xFF]
  const bytes = [
    0x89,
    0x50,
    0x4E,
    0x47,
    0x0D,
    0x0A,
    0x1A,
    0x0A,
    ...u32(13),
    0x49,
    0x48,
    0x44,
    0x52,
    ...u32(width),
    ...u32(height),
  ]
  return btoa(String.fromCharCode(...bytes))
}
