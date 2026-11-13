import { describe, expect, it, vi } from 'vitest'

type Base64Module = typeof import('./base64')

/**
 * Import the module afresh. Without the native API, the module evaluates while
 * `toBase64` and `fromBase64` are absent, as on an engine that lacks them, and keeps
 * its portable code after they come back.
 */
async function loadBase64(native: boolean): Promise<Base64Module> {
  vi.resetModules()
  if (native)
    return import('./base64')
  const prototype = Uint8Array.prototype as { toBase64?: unknown }
  const constructor = Uint8Array as { fromBase64?: unknown }
  const toBase64 = Object.getOwnPropertyDescriptor(prototype, 'toBase64')
  const fromBase64 = Object.getOwnPropertyDescriptor(constructor, 'fromBase64')
  delete prototype.toBase64
  delete constructor.fromBase64
  try {
    return await import('./base64')
  }
  finally {
    if (toBase64 !== undefined)
      Object.defineProperty(prototype, 'toBase64', toBase64)
    if (fromBase64 !== undefined)
      Object.defineProperty(constructor, 'fromBase64', fromBase64)
  }
}

const bytesOf = (text: string) => new TextEncoder().encode(text)

/**
 * The byte values, as plain numbers. jsdom's TextEncoder returns a Uint8Array of
 * another realm, and `toEqual` refuses to match two typed arrays across realms.
 */
const valuesOf = (bytes: Uint8Array) => Array.from(bytes)

/** The test vectors of RFC 4648, section 10. */
const RFC_4648_VECTORS: Array<[string, string]> = [
  ['', ''],
  ['f', 'Zg=='],
  ['fo', 'Zm8='],
  ['foo', 'Zm9v'],
  ['foob', 'Zm9vYg=='],
  ['fooba', 'Zm9vYmE='],
  ['foobar', 'Zm9vYmFy'],
]

function decodeError(decode: (base64: string) => Uint8Array, base64: string): unknown {
  try {
    decode(base64)
  }
  catch (error) {
    return error
  }
  return undefined
}

describe.each([
  { variant: 'the native API', native: true, invalidCharacterError: 'SyntaxError' },
  { variant: 'the portable code', native: false, invalidCharacterError: 'InvalidCharacterError' },
])('base64 with $variant', ({ native, invalidCharacterError }) => {
  it.each(RFC_4648_VECTORS)('encodes %j as %j', async (text, base64) => {
    const { uint8ArrayToBase64 } = await loadBase64(native)
    expect(uint8ArrayToBase64(bytesOf(text))).toBe(base64)
  })

  it.each(RFC_4648_VECTORS)('decodes %j from %j', async (text, base64) => {
    const { base64ToUint8Array } = await loadBase64(native)
    expect(valuesOf(base64ToUint8Array(base64))).toEqual(valuesOf(bytesOf(text)))
  })

  it('round-trips every byte value, at every length modulo three', async () => {
    const { base64ToUint8Array, uint8ArrayToBase64 } = await loadBase64(native)
    const all = Uint8Array.from({ length: 256 }, (_, i) => i)
    for (const length of [254, 255, 256]) {
      const bytes = all.subarray(0, length)
      expect(valuesOf(base64ToUint8Array(uint8ArrayToBase64(bytes)))).toEqual(valuesOf(bytes))
    }
  })

  it('encodes a subarray, not the whole buffer under it', async () => {
    const { uint8ArrayToBase64 } = await loadBase64(native)
    expect(uint8ArrayToBase64(bytesOf('xfoox').subarray(1, 4))).toBe('Zm9v')
  })

  it('agrees with the other implementation on a large input', async () => {
    const { uint8ArrayToBase64 } = await loadBase64(native)
    const other = await loadBase64(!native)
    const bytes = Uint8Array.from({ length: 1024 * 1024 + 1 }, (_, i) => (i * 2654435761) >>> 24)
    expect(uint8ArrayToBase64(bytes)).toBe(other.uint8ArrayToBase64(bytes))
  })

  it('accepts missing padding and skips ASCII whitespace', async () => {
    const { base64ToUint8Array } = await loadBase64(native)
    expect(valuesOf(base64ToUint8Array('Zg'))).toEqual(valuesOf(bytesOf('f')))
    expect(valuesOf(base64ToUint8Array(' Zm9v\nYmFy\t'))).toEqual(valuesOf(bytesOf('foobar')))
  })

  it('refuses a character outside the alphabet', async () => {
    const { base64ToUint8Array } = await loadBase64(native)
    expect(decodeError(base64ToUint8Array, 'Zm9v*')).toMatchObject({ name: invalidCharacterError })
    expect(decodeError(base64ToUint8Array, 'Zm9v-_')).toMatchObject({ name: invalidCharacterError })
  })

  it('converts between an ArrayBuffer and base64', async () => {
    const { arrayBufferToBase64, base64ToArrayBuffer } = await loadBase64(native)
    const buffer = base64ToArrayBuffer('Zm9vYmFy')
    expect(buffer.byteLength).toBe(6)
    expect(valuesOf(new Uint8Array(buffer))).toEqual(valuesOf(bytesOf('foobar')))
    expect(arrayBufferToBase64(buffer)).toBe('Zm9vYmFy')
    expect(arrayBufferToBase64('foobar')).toBe('Zm9vYmFy')
    expect(arrayBufferToBase64(null)).toBe('')
    expect(base64ToArrayBuffer('').byteLength).toBe(0)
  })
})
