/** Encode a Uint8Array to a base64 string. */
export function uint8ArrayToBase64(bytes: Uint8Array): string {
  let binary = ''
  for (let i = 0; i < bytes.length; i++)
    binary += String.fromCharCode(bytes[i])
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
