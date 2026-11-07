import { describe, expect, it } from 'vitest'
import { claudeImagesFromToolResult } from './image'

function image(dimensions?: unknown) {
  return claudeImagesFromToolResult({
    toolUseResult: {
      type: 'image',
      file: {
        base64: 'aGk=',
        type: 'image/png',
        ...(dimensions === undefined
          ? {}
          : { dimensions }),
      },
    },
    blockImages: [],
  })[0]
}

describe('claudeImagesFromToolResult', () => {
  it('accepts current image payloads without dimensions', () => {
    expect(image()).toEqual({ data: 'aGk=', mimeType: 'image/png' })
  })

  it('prefers the dimensions of the transmitted image', () => {
    expect(image({
      displayWidth: 20,
      displayHeight: 10,
      originalWidth: 200,
      originalHeight: 100,
    })?.dimensions).toEqual({ width: 20, height: 10 })
    expect(image({ originalWidth: 200, originalHeight: 100 })?.dimensions).toEqual({ width: 200, height: 100 })
  })

  it.each([
    { width: 20, height: 10 },
    { displayWidth: 0, displayHeight: 10 },
    { displayWidth: -1, displayHeight: 10 },
    { displayWidth: '20', displayHeight: 10 },
  ])('omits dimensions outside the current image shape: %j', (dimensions) => {
    expect(image(dimensions)).not.toHaveProperty('dimensions')
  })

  it('preserves the order of multiple image blocks', () => {
    const blocks = [
      { data: 'first' },
      { data: 'second' },
    ]
    expect(claudeImagesFromToolResult({
      toolUseResult: {
        type: 'image',
        file: { base64: 'structured' },
      },
      blockImages: blocks,
    })).toEqual(blocks)
  })
})
