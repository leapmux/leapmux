import { describe, expect, it, vi } from 'vitest'
import { ContentCompression } from '~/generated/leapmux/v1/agent_pb'

// Mock fzstd before importing the module under test
vi.mock('fzstd', () => ({
  decompress: vi.fn((input: Uint8Array) => {
    // Simple mock: return the input prefixed with a marker byte
    const result = new Uint8Array(input.length)
    result.set(input)
    return result
  }),
}))

// Import after mock is set up
const { decompressContent, decompressContentToString } = await import('~/lib/decompress')
const { decompress: mockFzstdDecompress } = await import('fzstd')

describe('decompressContent', () => {
  it('should return same Uint8Array for NONE compression', () => {
    const input = new Uint8Array([72, 101, 108, 108, 111])
    const result = decompressContent(input, ContentCompression.NONE)
    expect(result).toBe(input)
  })

  it('should return null for UNSPECIFIED compression', () => {
    const input = new Uint8Array([1, 2, 3])
    const result = decompressContent(input, ContentCompression.UNSPECIFIED)
    expect(result).toBeNull()
  })

  it('should return null for an invalid/unknown compression value', () => {
    const input = new Uint8Array([1, 2, 3])
    const result = decompressContent(input, 999 as ContentCompression)
    expect(result).toBeNull()
  })

  it('should call fzstd decompress for ZSTD compression', () => {
    const input = new Uint8Array([10, 20, 30])
    const result = decompressContent(input, ContentCompression.ZSTD)
    expect(mockFzstdDecompress).toHaveBeenCalledWith(input)
    expect(result).toBeInstanceOf(Uint8Array)
  })
})

describe('decompressContentToString', () => {
  it('should decode UTF-8 correctly for NONE compression', () => {
    const encoder = new TextEncoder()
    const input = encoder.encode('Hello, world!')
    const result = decompressContentToString(input, ContentCompression.NONE)
    expect(result).toBe('Hello, world!')
  })

  it('should return null for UNSPECIFIED compression', () => {
    const input = new Uint8Array([1, 2, 3])
    const result = decompressContentToString(input, ContentCompression.UNSPECIFIED)
    expect(result).toBeNull()
  })

  it('should return decoded string for ZSTD compression', () => {
    // The mock returns the same bytes, so decoding should give us the original string
    const encoder = new TextEncoder()
    const input = encoder.encode('compressed data')
    const result = decompressContentToString(input, ContentCompression.ZSTD)
    expect(mockFzstdDecompress).toHaveBeenCalledWith(input)
    expect(result).toBe('compressed data')
  })
})
