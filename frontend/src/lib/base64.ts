/** Encode a Uint8Array to a base64 string. */
export function uint8ArrayToBase64(bytes: Uint8Array): string {
  const CHUNK = 0x8000
  let binary = ''
  for (let i = 0; i < bytes.length; i += CHUNK)
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK))
  return btoa(binary)
}

/** Decode a base64 string to a Uint8Array. */
export function base64ToUint8Array(base64: string): Uint8Array {
  const binary = atob(base64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++)
    bytes[i] = binary.charCodeAt(i)
  return bytes
}

/** Encode an ArrayBuffer, Uint8Array, or string to a base64 string. */
export function arrayBufferToBase64(buf: ArrayBuffer | Uint8Array | string | null | undefined): string {
  if (!buf)
    return ''
  if (typeof buf === 'string') {
    const bytes = new TextEncoder().encode(buf)
    return uint8ArrayToBase64(bytes)
  }
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  return uint8ArrayToBase64(bytes)
}

/** Decode a base64 string to an ArrayBuffer. */
export function base64ToArrayBuffer(b64: string): ArrayBuffer {
  if (!b64)
    return new ArrayBuffer(0)
  const bytes = base64ToUint8Array(b64)
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}
