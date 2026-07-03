/**
 * FNV-1a 32-bit digest, hex-encoded. Not cryptographic — used to shrink
 * long cache keys (virtualizer height keys, render-cache inputs) where a
 * collision merely degrades to the pre-cache behavior rather than
 * corrupting anything user-visible.
 */
export function fnv1a32Hex(s: string): string {
  let hash = 0x811C9DC5
  for (let i = 0; i < s.length; i++) {
    hash ^= s.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193)
  }
  return (hash >>> 0).toString(16).padStart(8, '0')
}
