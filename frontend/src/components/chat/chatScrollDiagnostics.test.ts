import type { AnchorDriftInfo, RepinClampInfo, UnexplainedJumpInfo, UnexplainedJumpParams } from './chatScrollDiagnostics'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ANCHOR_DRIFT_WARN_PX, classifyUnexplainedJump, createScrollDiagnostics, UNEXPLAINED_JUMP_MIN_PX, VISIBLE_ANCHOR_JUMP_PX } from './chatScrollDiagnostics'

describe('chatScrollDiagnostics classifyUnexplainedJump', () => {
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

describe('chatScrollDiagnostics createScrollDiagnostics', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // A configurable emitter over spyable deps: tests flip hasOlder/hasNewer and read back the
  // exact WARN payload the logger forwards to console.warn.
  function makeDiagnostics(overrides: Partial<{
    hasOlder: boolean
    hasNewer: boolean
    dom: Record<string, unknown> | undefined
    measurement: unknown
    batch: unknown
    markers: unknown
  }> = {}) {
    const state = {
      hasOlder: false,
      hasNewer: false,
      dom: { scrollTop: 7 } as Record<string, unknown> | undefined,
      measurement: { delta: 5 } as unknown,
      batch: { commits: 1, deltaSum: 5, totalHeightDelta: 5 } as unknown,
      markers: ['marker'] as unknown,
      ...overrides,
    }
    const diag = createScrollDiagnostics({
      domSnapshot: () => state.dom,
      lastMeasurement: () => state.measurement,
      measurementBatch: () => state.batch,
      debugMarkers: () => state.markers,
      hasOlderMessages: () => state.hasOlder,
      hasNewerMessages: () => state.hasNewer,
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

  describe('emitRepinClamp (detector a)', () => {
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

    it('names the geometry change that caused the clamp', () => {
      const warn = spyWarn()
      // A clamp is the downstream effect of a flush moving the map, so the payload has to
      // say which commit did it -- the same correlation the drift and jump payloads carry.
      const { diag } = makeDiagnostics({
        hasOlder: true,
        measurement: { id: 'r', delta: -300 },
        batch: { commits: 4, deltaSum: -300, totalHeightDelta: -300 },
      })
      diag.emitRepinClamp(clampInfo({ clampPx: 200 }))
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('anchor re-pin clamped'),
        expect.objectContaining({
          measurement: { id: 'r', delta: -300 },
          batch: { commits: 4, deltaSum: -300, totalHeightDelta: -300 },
        }),
      )
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

  describe('emitAnchorDrift (detector c)', () => {
    it('warns on an absorbed drift above the floor, with the running total + measurement', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({ measurement: { commit: 9 }, dom: { scrollTop: 3 } })
      diag.emitAnchorDrift(driftInfo({ residualPx: 40 }), 1000)
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('drifted without correction'),
        expect.objectContaining({
          residualPx: 40,
          driftPxSinceLastWarn: 40,
          absorbsSinceLastWarn: 1,
          measurement: { commit: 9 },
          dom: { scrollTop: 3 },
        }),
      )
    })

    it('accumulates sub-floor absorbs until their SUM crosses the floor', () => {
      // The regression this detector exists for, and the one a per-event floor cannot see.
      // The engine's absorb cap (8px) sits UNDER this floor (16px) by design, so no single
      // absorb it can produce ever reaches the floor -- gating per event made the detector
      // permanently silent while content kept moving. Each absorb re-anchors at the displaced
      // position, so the displacement is retained and the shifts add up.
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ residualPx: 6 }), 1000)
      diag.emitAnchorDrift(driftInfo({ residualPx: 6 }), 1001)
      expect(warn).not.toHaveBeenCalled() // 12px so far: still under the floor
      diag.emitAnchorDrift(driftInfo({ residualPx: 6 }), 1002)
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('drifted without correction'),
        expect.objectContaining({
          residualPx: 6, // the single absorb that crossed it -- individually imperceptible
          driftPxSinceLastWarn: 18,
          absorbsSinceLastWarn: 3,
        }),
      )
    })

    it('nets opposing absorbs, so shifts that cancel never warn', () => {
      // The sum is SIGNED on purpose: a shift up followed by an equal shift down leaves the
      // content where it started, which is not drift. Summing magnitudes would WARN on it.
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      for (const px of [7, -7, 7, -7, 7, -7])
        diag.emitAnchorDrift(driftInfo({ residualPx: px }), 1000)
      expect(warn).not.toHaveBeenCalled()
    })

    it('resets the running total after a warn', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ residualPx: 20 }), 0) // warns
      warn.mockClear()
      diag.emitAnchorDrift(driftInfo({ residualPx: 6 }), 2000)
      expect(warn).not.toHaveBeenCalled() // the total restarted at 0, so 6px is under the floor
      diag.emitAnchorDrift(driftInfo({ residualPx: 12 }), 4000)
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('drifted without correction'),
        expect.objectContaining({ driftPxSinceLastWarn: 18, absorbsSinceLastWarn: 2 }),
      )
    })

    it('carries the whole flush\'s commit totals, not only the last commit', () => {
      const warn = spyWarn()
      // The reported shape: a batched premeasure whose FINAL row measured exactly at its
      // estimate (delta 0), while the flush as a whole shrank the map by 74px. Without the
      // batch totals the payload says the geometry moved for no reason.
      const { diag } = makeDiagnostics({
        measurement: { delta: 0, commitSeq: 257 },
        batch: { commits: 9, deltaSum: -68, totalHeightDelta: -74 },
      })
      diag.emitAnchorDrift(driftInfo({ residualPx: -74 }), 1000)
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('drifted without correction'),
        expect.objectContaining({
          measurement: { delta: 0, commitSeq: 257 },
          batch: { commits: 9, deltaSum: -68, totalHeightDelta: -74 },
        }),
      )
    })

    it('never reports a deferred-fling drift, whatever its size', () => {
      // NOT because it is transient -- the fling-settle drops the drift and re-anchors, so it
      // is permanent too. It is excluded because the reader watched the viewport coast to that
      // position under their own gesture, which makes landing there expected, not an anomaly.
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      for (let i = 0; i < 5; i++)
        diag.emitAnchorDrift(driftInfo({ reason: 'deferred-fling', residualPx: 400 }), 1000 + i)
      expect(warn).not.toHaveBeenCalled()
    })

    it('does not let a deferred-fling drift feed the absorbed running total', () => {
      // The reason filter runs BEFORE the accumulator. If it did not, one deferred fling would
      // push the sum past the floor and make the next unrelated absorb warn for it.
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ reason: 'deferred-fling', residualPx: 400 }), 1000)
      diag.emitAnchorDrift(driftInfo({ residualPx: 6 }), 2000)
      expect(warn).not.toHaveBeenCalled()
    })

    it('warns exactly at the floor, not below it', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ residualPx: ANCHOR_DRIFT_WARN_PX - 1 }), 1000)
      expect(warn).not.toHaveBeenCalled()
      const { diag: fresh } = makeDiagnostics()
      fresh.emitAnchorDrift(driftInfo({ residualPx: ANCHOR_DRIFT_WARN_PX }), 1000)
      expect(warn).toHaveBeenCalledTimes(1)
    })

    it('rate-limits within the window and keeps accumulating across it', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics()
      diag.emitAnchorDrift(driftInfo({ residualPx: 40 }), 0) // warns, resets the total
      warn.mockClear()
      // The window is measured from the LAST WARN (0), not the last event.
      diag.emitAnchorDrift(driftInfo({ residualPx: 40 }), 500) // over the floor, but rate-limited
      diag.emitAnchorDrift(driftInfo({ residualPx: -30 }), 900) // still inside the window
      expect(warn).not.toHaveBeenCalled()
      diag.emitAnchorDrift(driftInfo({ residualPx: 50 }), 1100) // 1100 - 0 > 1000 -> warns
      expect(warn).toHaveBeenCalledWith(
        '[chatScroll]',
        expect.stringContaining('drifted without correction'),
        // 40 - 30 + 50: nothing the rate limit held back is lost from the total.
        expect.objectContaining({ residualPx: 50, driftPxSinceLastWarn: 60, absorbsSinceLastWarn: 3 }),
      )
    })
  })

  describe('emitUnexplainedJump (detector b)', () => {
    it('warns on the first jump and assembles the full payload', () => {
      const warn = spyWarn()
      const { diag } = makeDiagnostics({
        measurement: { m: 1 },
        batch: { commits: 3, deltaSum: -12, totalHeightDelta: -40 },
        markers: ['echo'],
        dom: { scrollTop: 2 },
      })
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
          // A jump that coincides with a geometry flush points at a render cause rather than
          // momentum, so Detector B carries the same batch totals as the drift payload.
          batch: { commits: 3, deltaSum: -12, totalHeightDelta: -40 },
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
