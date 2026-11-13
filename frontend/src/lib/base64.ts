/**
 * `Uint8Array.prototype.toBase64` and `Uint8Array.fromBase64`, where the engine has
 * them (Chrome 140, Firefox 133, Safari 18.2). The TypeScript `lib` of this project is
 * ES2023, which declares neither, and an older engine, such as the WebKitGTK of an
 * older Linux desktop, has neither.
 *
 * Native code encodes 5 MiB in about 2 ms. The portable code below takes about 10 ms.
 * The spread of each 32 KiB block into `String.fromCharCode` that it replaces took
 * 110 ms, which froze the page for each large image and each desktop relay frame.
 */
const nativeToBase64 = (Uint8Array.prototype as { toBase64?: (this: Uint8Array) => string }).toBase64
const nativeFromBase64 = (Uint8Array as { fromBase64?: (base64: string) => Uint8Array }).fromBase64

const BASE64_ALPHABET = new TextEncoder().encode('ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/')
const BASE64_PADDING = 0x3D // '='
const asciiDecoder = new TextDecoder()

/** Encode `bytes` without the native API: four ASCII bytes for each three input bytes. */
function encodeBase64Portably(bytes: Uint8Array): string {
  const length = bytes.length
  const out = new Uint8Array(Math.ceil(length / 3) * 4)
  let o = 0
  let i = 0
  for (; i + 2 < length; i += 3) {
    const triple = (bytes[i]! << 16) | (bytes[i + 1]! << 8) | bytes[i + 2]!
    out[o++] = BASE64_ALPHABET[triple >> 18]!
    out[o++] = BASE64_ALPHABET[(triple >> 12) & 63]!
    out[o++] = BASE64_ALPHABET[(triple >> 6) & 63]!
    out[o++] = BASE64_ALPHABET[triple & 63]!
  }
  if (i < length) {
    // One or two bytes remain: '=' pads their group to four characters.
    const hasSecond = i + 1 < length
    const second = hasSecond
      ? bytes[i + 1]!
      : 0
    const triple = (bytes[i]! << 16) | (second << 8)
    out[o++] = BASE64_ALPHABET[triple >> 18]!
    out[o++] = BASE64_ALPHABET[(triple >> 12) & 63]!
    out[o++] = hasSecond
      ? BASE64_ALPHABET[(triple >> 6) & 63]!
      : BASE64_PADDING
    out[o++] = BASE64_PADDING
  }
  return asciiDecoder.decode(out)
}

/** Decode `base64` without the native API. `atob` validates it the same way. */
function decodeBase64Portably(base64: string): Uint8Array {
  const binary = atob(base64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++)
    bytes[i] = binary.charCodeAt(i)
  return bytes
}

/** Encode a Uint8Array to a base64 string. */
export function uint8ArrayToBase64(bytes: Uint8Array): string {
  return nativeToBase64 !== undefined
    ? nativeToBase64.call(bytes)
    : encodeBase64Portably(bytes)
}

/**
 * Decode a base64 string to a Uint8Array.
 *
 * It skips ASCII whitespace and accepts missing padding, as `atob` does, and throws
 * on any other character outside the alphabet.
 */
export function base64ToUint8Array(base64: string): Uint8Array {
  return nativeFromBase64 !== undefined
    ? nativeFromBase64(base64)
    : decodeBase64Portably(base64)
}

/** Encode an ArrayBuffer, Uint8Array, or string to a base64 string. */
export function arrayBufferToBase64(buf: ArrayBuffer | Uint8Array | string | null | undefined): string {
  if (!buf)
    return ''
  if (typeof buf === 'string') {
    const bytes = new TextEncoder().encode(buf)
    return uint8ArrayToBase64(bytes)
  }
  const bytes = buf instanceof Uint8Array
    ? buf
    : new Uint8Array(buf)
  return uint8ArrayToBase64(bytes)
}

/** Decode a base64 string to an ArrayBuffer. */
export function base64ToArrayBuffer(b64: string): ArrayBuffer {
  if (!b64)
    return new ArrayBuffer(0)
  const bytes = base64ToUint8Array(b64)
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}
