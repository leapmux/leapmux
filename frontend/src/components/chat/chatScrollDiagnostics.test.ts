import type { UnexplainedJumpParams } from './chatScrollDiagnostics'
import { describe, expect, it } from 'vitest'
import { classifyUnexplainedJump, UNEXPLAINED_JUMP_MIN_PX } from './chatScrollDiagnostics'

describe('chatscrolldiagnostics classifyUnexplainedJump', () => {
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
