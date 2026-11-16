import { pngBytes } from './imageBytes'

/**
 * A minimal PNG (signature + IHDR only) declaring the given dimensions.
 *
 * Enough for the header sniffer in `~/lib/imageDimensions`, which reads the
 * first 24 bytes and never decodes pixels. Tests that need a sniffable image
 * use this rather than checking in a binary fixture.
 */
export function pngBase64(width: number, height: number): string {
  return btoa(String.fromCharCode(...pngBytes(width, height)))
}
