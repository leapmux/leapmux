import { describe, expect, it } from 'vitest'
import {
  ZOOM_MAX,
  ZOOM_MIN,
  ZOOM_STEP,
  zoomIn,
  zoomLabel,
  zoomOut,
} from '~/components/fileviewer/ImageToolbar'

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
  it('increases numeric scale by ZOOM_STEP', () => {
    expect(zoomIn(1)).toBe(1 + ZOOM_STEP)
    expect(zoomIn(0.5)).toBe(0.5 + ZOOM_STEP)
    expect(zoomIn(2)).toBe(2 + ZOOM_STEP)
  })

  it('treats fit as scale 1 when no fitScale provided', () => {
    expect(zoomIn('fit')).toBe(1 + ZOOM_STEP)
  })

  it('uses fitScale as base when provided for fit mode', () => {
    expect(zoomIn('fit', 0.5)).toBe(0.5 + ZOOM_STEP)
    expect(zoomIn('fit', 2)).toBe(2 + ZOOM_STEP)
  })

  it('ignores fitScale for non-fit modes', () => {
    expect(zoomIn('actual', 0.5)).toBe(1 + ZOOM_STEP)
    expect(zoomIn(1.5, 0.5)).toBe(1.5 + ZOOM_STEP)
  })

  it('treats actual as scale 1 and increases', () => {
    expect(zoomIn('actual')).toBe(1 + ZOOM_STEP)
  })

  it('clamps at ZOOM_MAX', () => {
    expect(zoomIn(ZOOM_MAX)).toBe(ZOOM_MAX)
    expect(zoomIn(ZOOM_MAX - 0.1)).toBe(ZOOM_MAX)
  })
})

describe('zoomOut', () => {
  it('decreases numeric scale by ZOOM_STEP', () => {
    expect(zoomOut(1)).toBe(1 - ZOOM_STEP)
    expect(zoomOut(1.5)).toBe(1.5 - ZOOM_STEP)
    expect(zoomOut(2)).toBe(2 - ZOOM_STEP)
  })

  it('treats fit as scale 1 when no fitScale provided', () => {
    expect(zoomOut('fit')).toBe(1 - ZOOM_STEP)
  })

  it('uses fitScale as base when provided for fit mode', () => {
    expect(zoomOut('fit', 0.5)).toBe(ZOOM_MIN)
    expect(zoomOut('fit', 2)).toBe(2 - ZOOM_STEP)
  })

  it('ignores fitScale for non-fit modes', () => {
    expect(zoomOut('actual', 0.5)).toBe(1 - ZOOM_STEP)
    expect(zoomOut(1.5, 0.5)).toBe(1.5 - ZOOM_STEP)
  })

  it('treats actual as scale 1 and decreases', () => {
    expect(zoomOut('actual')).toBe(1 - ZOOM_STEP)
  })

  it('clamps at ZOOM_MIN', () => {
    expect(zoomOut(ZOOM_MIN)).toBe(ZOOM_MIN)
    expect(zoomOut(ZOOM_MIN + 0.1)).toBe(ZOOM_MIN)
  })
})
