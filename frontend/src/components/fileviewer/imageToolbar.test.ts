import { describe, expect, it } from 'vitest'
import {
  zoomIn,
  zoomLabel,
  zoomOut,
} from '~/components/fileviewer/ImageToolbar'

// Pin the intended numeric values of the production constants. If the
// constants change, both the production code AND these literals should
// be updated in lockstep; importing the constants and computing the
// expected value would silently follow whatever the production value is.
//
// ZOOM_STEP = 0.25
// ZOOM_MIN  = 0.25
// ZOOM_MAX  = 5

describe('zoomLabel', () => {
  it('returns "Fit" for fit mode without fitScale', () => {
    expect(zoomLabel('fit')).toBe('Fit')
    expect(zoomLabel('fit', null)).toBe('Fit')
    expect(zoomLabel('fit', undefined)).toBe('Fit')
  })

  it('returns just the percentage for fit mode with fitScale', () => {
    expect(zoomLabel('fit', 0.123)).toBe('12.3%')
    expect(zoomLabel('fit', 0.5)).toBe('50%')
    expect(zoomLabel('fit', 1)).toBe('100%')
    expect(zoomLabel('fit', 0.456)).toBe('45.6%')
    expect(zoomLabel('fit', 2.5)).toBe('250%')
  })

  it('returns "100%" for actual mode', () => {
    expect(zoomLabel('actual')).toBe('100%')
  })

  it('returns percentage for numeric scale', () => {
    expect(zoomLabel(0.5)).toBe('50%')
    expect(zoomLabel(1)).toBe('100%')
    expect(zoomLabel(1.5)).toBe('150%')
    expect(zoomLabel(2)).toBe('200%')
    expect(zoomLabel(0.25)).toBe('25%')
  })

  it('rounds to nearest integer percentage', () => {
    expect(zoomLabel(0.333)).toBe('33%')
    expect(zoomLabel(1.666)).toBe('167%')
  })

  it('ignores fitScale for non-fit modes', () => {
    expect(zoomLabel('actual', 0.5)).toBe('100%')
    expect(zoomLabel(1.5, 0.5)).toBe('150%')
  })
})

describe('zoomIn', () => {
  it('increases numeric scale by 0.25', () => {
    expect(zoomIn(1)).toBe(1.25)
    expect(zoomIn(0.5)).toBe(0.75)
    expect(zoomIn(2)).toBe(2.25)
  })

  it('treats fit as scale 1 when no fitScale provided', () => {
    expect(zoomIn('fit')).toBe(1.25)
  })

  it('uses fitScale as base when provided for fit mode', () => {
    expect(zoomIn('fit', 0.5)).toBe(0.75)
    expect(zoomIn('fit', 2)).toBe(2.25)
  })

  it('ignores fitScale for non-fit modes', () => {
    expect(zoomIn('actual', 0.5)).toBe(1.25)
    expect(zoomIn(1.5, 0.5)).toBe(1.75)
  })

  it('treats actual as scale 1 and increases', () => {
    expect(zoomIn('actual')).toBe(1.25)
  })

  it('clamps at 5x', () => {
    expect(zoomIn(5)).toBe(5)
    expect(zoomIn(4.9)).toBe(5)
  })

  it('rounds away binary float drift introduced by the step', () => {
    // 0.1 + 0.25 = 0.35000000000000003 without rounding.
    expect(zoomIn(0.1)).toBe(0.35)
  })
})

describe('zoomOut', () => {
  it('decreases numeric scale by 0.25', () => {
    expect(zoomOut(1)).toBe(0.75)
    expect(zoomOut(1.5)).toBe(1.25)
    expect(zoomOut(2)).toBe(1.75)
  })

  it('treats fit as scale 1 when no fitScale provided', () => {
    expect(zoomOut('fit')).toBe(0.75)
  })

  it('uses fitScale as base when provided for fit mode', () => {
    expect(zoomOut('fit', 0.5)).toBe(0.25)
    expect(zoomOut('fit', 2)).toBe(1.75)
  })

  it('ignores fitScale for non-fit modes', () => {
    expect(zoomOut('actual', 0.5)).toBe(0.75)
    expect(zoomOut(1.5, 0.5)).toBe(1.25)
  })

  it('treats actual as scale 1 and decreases', () => {
    expect(zoomOut('actual')).toBe(0.75)
  })

  it('clamps at 0.25x', () => {
    expect(zoomOut(0.25)).toBe(0.25)
    expect(zoomOut(0.35)).toBe(0.25)
  })
})
