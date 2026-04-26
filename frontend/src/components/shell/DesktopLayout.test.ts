import type { AutoCollapseInput } from './DesktopLayout'
import { describe, expect, it } from 'vitest'
import { decideAutoCollapse } from './DesktopLayout'

function input(overrides: Partial<AutoCollapseInput> = {}): AutoCollapseInput {
  return {
    viewportWidth: 1280,
    leftCollapsed: false,
    rightCollapsed: false,
    leftWidth: 250,
    rightWidth: 250,
    autoCollapsedLeft: false,
    autoCollapsedRight: false,
    leftWidthBeforeCollapse: 250,
    rightWidthBeforeCollapse: 250,
    ...overrides,
  }
}

describe('decideAutoCollapse', () => {
  it('is a no-op when sidebars fit comfortably within half the viewport', () => {
    // 250 + 250 = 500 < 1280 / 2 = 640
    expect(decideAutoCollapse(input())).toEqual({})
  })

  it('auto-collapses both sidebars when total exceeds half the viewport', () => {
    // 400 + 400 = 800 > 1024 / 2 = 512
    const decision = decideAutoCollapse(input({
      viewportWidth: 1024,
      leftWidth: 400,
      rightWidth: 400,
    }))
    expect(decision).toEqual({ collapseLeft: true, collapseRight: true })
  })

  it('only auto-collapses uncollapsed sides', () => {
    // Left already collapsed; right needs to be collapsed
    const decision = decideAutoCollapse(input({
      viewportWidth: 600,
      leftCollapsed: true,
      leftWidth: 250,
      rightCollapsed: false,
      rightWidth: 400,
    }))
    expect(decision).toEqual({ collapseRight: true })
    expect(decision.collapseLeft).toBeUndefined()
  })

  it('does not auto-collapse when both sides are already collapsed', () => {
    const decision = decideAutoCollapse(input({
      viewportWidth: 400,
      leftCollapsed: true,
      rightCollapsed: true,
    }))
    expect(decision).toEqual({})
  })

  it('auto-expands a sidebar that was previously auto-collapsed and now fits', () => {
    // Viewport widened to 1280 (half = 640); restoring 250+250 = 500 fits.
    const decision = decideAutoCollapse(input({
      viewportWidth: 1280,
      leftCollapsed: true,
      rightCollapsed: true,
      leftWidth: 250,
      rightWidth: 250,
      autoCollapsedLeft: true,
      autoCollapsedRight: true,
      leftWidthBeforeCollapse: 250,
      rightWidthBeforeCollapse: 250,
    }))
    expect(decision.expandLeft).toEqual({ newWidth: 250 })
    expect(decision.expandRight).toEqual({ newWidth: 250 })
  })

  it('does not auto-expand sidebars the user manually collapsed', () => {
    // leftCollapsed=true but autoCollapsedLeft=false → user-collapsed; never auto-expand.
    const decision = decideAutoCollapse(input({
      viewportWidth: 1920,
      leftCollapsed: true,
      autoCollapsedLeft: false,
      rightCollapsed: true,
      autoCollapsedRight: false,
    }))
    expect(decision).toEqual({})
  })

  it('does not auto-expand if the restored size would still exceed half the viewport', () => {
    // Viewport 800 → half = 400. Restoring 300 + 300 = 600 still too big.
    const decision = decideAutoCollapse(input({
      viewportWidth: 800,
      leftCollapsed: true,
      rightCollapsed: true,
      autoCollapsedLeft: true,
      autoCollapsedRight: true,
      leftWidthBeforeCollapse: 300,
      rightWidthBeforeCollapse: 300,
    }))
    expect(decision).toEqual({})
  })

  it('expands only the side that was auto-collapsed', () => {
    // Left was auto-collapsed; right is still expanded. Restore left only.
    const decision = decideAutoCollapse(input({
      viewportWidth: 1280,
      leftCollapsed: true,
      autoCollapsedLeft: true,
      leftWidthBeforeCollapse: 200,
      rightCollapsed: false,
      rightWidth: 200,
    }))
    expect(decision.expandLeft).toEqual({ newWidth: 200 })
    expect(decision.expandRight).toBeUndefined()
  })

  it('mixed expand+stay-expanded check uses live width for the uncollapsed side', () => {
    // Left auto-collapsed (would expand to 200); right currently expanded at 350.
    // Half-viewport 350 → restored total 200 + 350 = 550 > 350 → no expand.
    const decision = decideAutoCollapse(input({
      viewportWidth: 700,
      leftCollapsed: true,
      autoCollapsedLeft: true,
      leftWidthBeforeCollapse: 200,
      rightCollapsed: false,
      rightWidth: 350,
    }))
    expect(decision).toEqual({})
  })

  it('does not auto-collapse when both sidebars are already at zero visible width', () => {
    // visibleTotal = 0 short-circuits the collapse branch.
    const decision = decideAutoCollapse(input({
      viewportWidth: 100,
      leftCollapsed: true,
      rightCollapsed: true,
      autoCollapsedLeft: false,
      autoCollapsedRight: false,
    }))
    expect(decision).toEqual({})
  })

  it('preserves widths when viewport widens with sidebars open and fitting', () => {
    // Viewport 1920, half=960; sidebars 300+300=600 fit. No-op (widths unchanged).
    const decision = decideAutoCollapse(input({
      viewportWidth: 1920,
      leftWidth: 300,
      rightWidth: 300,
    }))
    expect(decision).toEqual({})
  })

  it('handles asymmetric widths (only one sidebar exceeds threshold)', () => {
    // Viewport 1280, half=640; left=700 alone exceeds.
    const decision = decideAutoCollapse(input({
      viewportWidth: 1280,
      leftWidth: 700,
      rightWidth: 0, // simulate right at minimum (already-collapsed scenario)
      rightCollapsed: true,
    }))
    expect(decision).toEqual({ collapseLeft: true })
  })
})
