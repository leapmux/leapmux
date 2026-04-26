import { describe, expect, it } from 'vitest'
import { heightToHeightClass, widthToSizeClass } from './useTileSize'

describe('widthToSizeClass', () => {
  it.each([
    [Infinity, 'full'],
    [1280, 'full'],
    [481, 'full'],
    [480, 'full'],
    [479, 'narrow'],
    [400, 'narrow'],
    [360, 'narrow'],
    [359, 'compact'],
    [300, 'compact'],
    [240, 'compact'],
    [239, 'minimal'],
    [200, 'minimal'],
    [172, 'minimal'],
    [171, 'micro'],
    [100, 'micro'],
    [0, 'micro'],
  ])('width %d → %s', (width, expected) => {
    expect(widthToSizeClass(width)).toBe(expected)
  })
})

describe('heightToHeightClass', () => {
  it.each([
    [Infinity, 'tall'],
    [720, 'tall'],
    [121, 'tall'],
    [120, 'tall'],
    [119, 'short'],
    [100, 'short'],
    [72, 'short'],
    [71, 'tiny'],
    [50, 'tiny'],
    [0, 'tiny'],
  ])('height %d → %s', (height, expected) => {
    expect(heightToHeightClass(height)).toBe(expected)
  })
})
