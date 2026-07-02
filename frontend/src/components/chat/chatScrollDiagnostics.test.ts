import type { AnchorDriftInfo, RepinClampInfo, UnexplainedJumpInfo, UnexplainedJumpParams } from './chatScrollDiagnostics'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ANCHOR_DRIFT_WARN_PX, classifyUnexplainedJump, createScrollDiagnostics, UNEXPLAINED_JUMP_MIN_PX, VISIBLE_ANCHOR_JUMP_PX } from './chatScrollDiagnostics'

describe('chatscrolldiagnostics classifyunexplainedjump', () => {
  /** A mid-list teleport from rest with every exclusion off -- the base unexplained case. */
  function teleport(overrides: Partial<UnexplainedJumpParams> = {}): UnexplainedJumpParams {
    return {
      scrollTopAtStart: 3000,
      lastScrollTopBeforeEvent: 0,
      maxScrollTopAtStart: 4500,
      programmaticEcho: false,
      stillEcho: false,
      discretePage: false,
      staleNative: false,
      wasActivelyFlingingBeforeEvent: false,
      wasFollowingBeforeEvent: false,
      scrollInputActive: false,
      recentMomentumInput: false,
      recentKeyboardScroll: false,
      ...overrides,
    }
  }

  it('flags a bare teleport and reports the signed delta', () => {
    expect(classifyUnexplainedJump(teleport())).toEqual({ deltaFromLast: 3000, isUnexplained: true })
    expect(classifyUnexplainedJump(teleport({ scrollTopAtStart: 0, lastScrollTopBeforeEvent: 3000 })))
      .toEqual({ deltaFromLast: -3000, isUnexplained: true })
  })

  it('ignores moves at or under the absolute floor', () => {
    const small = teleport({ scrollTopAtStart: UNEXPLAINED_JUMP_MIN_PX, lastScrollTopBeforeEvent: 0 })
    expect(classifyUnexplainedJump(small).isUnexplained).toBe(false)
  })

  it('excuses each known legitimate mover', () => {
    for (const key of [
      'programmaticEcho',
      'stillEcho',
      'discretePage',
      'staleNative',
      'wasActivelyFlingingBeforeEvent',
      'scrollInputActive',
      'recentMomentumInput',
      'recentKeyboardScroll',
    ] as const) {
      expect(classifyUnexplainedJump(teleport({ [key]: true })).isUnexplained, key).toBe(false)
    }
  })

  it('excuses a tail-follow landing at the clamped bottom only with pre-event evidence', () => {
    // Landing at the bottom while the view was FOLLOWING before the event: the tail
    // followed a grow (its restick echo can outlive the marker TTL) -- excused.
    const follow = teleport({ scrollTopAtStart: 4500, wasFollowingBeforeEvent: true })
    expect(classifyUnexplainedJump(follow).isUnexplained).toBe(false)
    // A browser force-clamp after a SHRINK: the prior position exceeds the (same-epoch)
    // range even though the mode was anchored -- excused.
    const shrinkClamp = teleport({
      scrollTopAtStart: 2500,
      lastScrollTopBeforeEvent: 4500,
      maxScrollTopAtStart: 2500,
    })
    expect(classifyUnexplainedJump(shrinkClamp).isUnexplained).toBe(false)
    // A teleport that merely LANDS at the bottom from an anchored mid-list position has
    // neither: it is the exact class the detector exists to catch.
    const teleportToBottom = teleport({ scrollTopAtStart: 4500, lastScrollTopBeforeEvent: 1000 })
    expect(classifyUnexplainedJump(teleportToBottom).isUnexplained).toBe(true)
  })
})

describe('chatscrolldiagnostics createscrolldiagnostics', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // A configurable emitter over spyable deps: tests flip hasOlder/hasNewer/flinging and
  // read back the exact WARN payload the logger forwards to console.warn.
  function makeDiagnostics(overrides: Partial<{
    hasOlder: boolean
    hasNewer: boolean
    flinging: boolean
    dom: Record<string, unknown> | undefined
    measurement: unknown
    markers: unknown
  }> = {}) {
    const state = {
      hasOlder: false,
      hasNewer: false,
      flinging: false,
      dom: { scrollTop: 7 } as Record<string, unknown> | undefined,
      measurement: { delta: 5 } as unknown,
      markers: ['marker'] as unknown,
      ...overrides,
    }
    const diag = createScrollDiagnostics({
      domSnapshot: () => state.dom,
      lastMeasurement: () => state.measurement,
      debugMarkers: () => state.markers,
      hasOlderMessages: () => state.hasOlder,
      hasNewerMessages: () => state.hasNewer,
      isActivelyFlinging: () => state.flinging,
    })
    return { diag, state }
  }
  function spyWarn() {
    return vi.spyOn(console, 'warn').mockImplementation(() => {})
  }
  function clampInfo(overrides: Partial<RepinClampInfo> = {}): RepinClampInfo {
    return { anchorId: 'a', clampPx: 200, fromTop: 0, idealTop: 0, targetTop: 0, clientHeight: 800, maxScrollTop: 1000, ...overrides }
  }
  function driftInfo(overrides: Partial<AnchorDriftInfo> = {}): AnchorDriftInfo {
    return { anchorId: 'a', residualPx: 40, reason: 'absorbed', fromTop: 0, clientHeight: 800, ...overrides }
  }
  function jumpInfo(overrides: Partial<UnexplainedJumpInfo> = {}): UnexplainedJumpInfo {
    return { deltaFromLast: 300, scrollTop: 300, lastScrollTop: 0, msSinceLastScrollEvent: 20, speedPxPerMs: 0, wasActivelyFlinging: false, ...overrides }
  }

  describe('emitrepinclamp (detector a)', () => {
    it('warns clampedAt top when a top clamp still had older history to hold the row', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ hasOlder: true, dom: { scrollTop: 5 } })
      diag.emitRepinClamp(clampInfo({ clampPx: 200 }))
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('anchor re-pin clamped'),
        expect.objectContaining({ clampedAt: 'top', clampPx: 200, dom: { scrollTop: 5 } }),
      )
    })

    it('warns clampedAt bottom for a negative clamp against loaded newer history', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ hasNewer: true })
      diag.emitRepinClamp(clampInfo({ clampPx: -200 }))
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('anchor re-pin clamped'),
        expect.objectContaining({ clampedAt: 'bottom', clampPx: -200 }),
      )
    })

    it('stays silent at a genuinely exhausted edge (no history that direction)', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ hasOlder: false, hasNewer: true })
      // clampPx > 0 consults hasOlder (false), NOT hasNewer -- the top edge is exhausted.
      diag.emitRepinClamp(clampInfo({ clampPx: 200 }))
      expect(warn).not.toHaveBeenCalled()
    })

    it('warns exactly at the visible-jump floor but not one px below it', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ hasOlder: true })
      diag.emitRepinClamp(clampInfo({ clampPx: VISIBLE_ANCHOR_JUMP_PX - 1 }))
      expect(warn).not.toHaveBeenCalled()
      diag.emitRepinClamp(clampInfo({ clampPx: VISIBLE_ANCHOR_JUMP_PX }))
      expect(warn).toHaveBeenCalledTimes(1)
    })
  })

  describe('emitanchordrift (detector c)', () => {
    it('warns on an absorbed drift above the floor with zeroed aggregates + measurement', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ measurement: { commit: 9 }, dom: { scrollTop: 3 } })
      diag.emitAnchorDrift(driftInfo({ residualPx: 40 }), 1000)
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('drifted without correction'),
        expect.objectContaining({
          residualPx: 40,
          suppressedSinceLastWarn: 0,
          suppressedResidualPxSum: 0,
          measurement: { commit: 9 },
          dom: { scrollTop: 3 },
        }),
      )
    })

    it('ignores a transient deferred-fling drift', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ reason: 'deferred-fling', residualPx: 400 }), 1000)
      expect(warn).not.toHaveBeenCalled()
    })

    it('ignores a sub-floor drift but warns exactly at the floor', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ residualPx: ANCHOR_DRIFT_WARN_PX - 1 }), 1000)
      expect(warn).not.toHaveBeenCalled()
      diag.emitAnchorDrift(driftInfo({ residualPx: ANCHOR_DRIFT_WARN_PX }), 5000)
      expect(warn).toHaveBeenCalledTimes(1)
    })

    it('ignores a drift while the tracker still reports an active fling', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ flinging: true })
      diag.emitAnchorDrift(driftInfo({ residualPx: 400 }), 1000)
      expect(warn).not.toHaveBeenCalled()
    })

    it('rate-limits within the window and carries the suppressed count + residual sum forward', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ residualPx: 40 }), 0) // warns, resets counters
      warn.mockClear()
      // The window is measured from the LAST WARN (0), not the last event.
      diag.emitAnchorDrift(driftInfo({ residualPx: 40 }), 500) // suppressed
      diag.emitAnchorDrift(driftInfo({ residualPx: -30 }), 900) // suppressed
      expect(warn).not.toHaveBeenCalled()
      diag.emitAnchorDrift(driftInfo({ residualPx: 50 }), 1100) // 1100 - 0 > 1000 -> warns
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('drifted without correction'),
        // Math.round(40 + -30) = 10; the aggregate rides along, then resets.
        expect.objectContaining({ residualPx: 50, suppressedSinceLastWarn: 2, suppressedResidualPxSum: 10 }),
      )
    })
  })

  describe('emitunexplainedjump (detector b)', () => {
    it('warns on the first jump and assembles the full payload', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ measurement: { m: 1 }, markers: ['echo'], dom: { scrollTop: 2 } })
      diag.emitUnexplainedJump(
        jumpInfo({ deltaFromLast: 300, scrollTop: 300, lastScrollTop: 0, msSinceLastScrollEvent: 20, speedPxPerMs: 1.5, wasActivelyFlinging: false }),
        5000,
      )
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('unexpected scroll jump'),
        expect.objectContaining({
          deltaFromLast: 300,
          scrollTop: 300,
          lastScrollTop: 0,
          msSinceLastScrollEvent: 20,
          speedPxPerMs: 1.5,
          wasActivelyFlinging: false,
          suppressedSinceLastWarn: 0,
          measurement: { m: 1 },
          markers: ['echo'],
          dom: { scrollTop: 2 },
        }),
      )
    })

    it('slides the burst window across events and reports the count on the next warn', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitUnexplainedJump(jumpInfo(), 5000) // warns (baseline is -Infinity)
      warn.mockClear()
      diag.emitUnexplainedJump(jumpInfo(), 5500) // 5500 - 5000 <= 1000 -> suppressed, baseline slides to 5500
      diag.emitUnexplainedJump(jumpInfo(), 5900) // 5900 - 5500 <= 1000 -> suppressed (SLIDING), baseline 5900
      expect(warn).not.toHaveBeenCalled()
      diag.emitUnexplainedJump(jumpInfo(), 7000) // 7000 - 5900 > 1000 -> warns with the tally
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('unexpected scroll jump'),
        expect.objectContaining({ suppressedSinceLastWarn: 2 }),
      )
      // The count resets after emitting: an immediate next warn reports 0.
      warn.mockClear()
      diag.emitUnexplainedJump(jumpInfo(), 9000)
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('unexpected scroll jump'),
        expect.objectContaining({ suppressedSinceLastWarn: 0 }),
      )
    })
  })
})
