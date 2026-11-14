import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'

import { inferScrollDirection } from './chatScrollGeometry'
import { computeBufferAwareKeepNewest, computeKeepNewest } from './useChatScroll'
import { installScrollTestEnv } from './useChatScroll.testkit'

installScrollTestEnv()

describe('inferScrollDirection', () => {
  it('returns older when scrollTop moved up (toward older history)', () => {
    expect(inferScrollDirection(500, 300)).toBe('older')
  })

  it('returns newer when scrollTop moved down', () => {
    expect(inferScrollDirection(300, 500)).toBe('newer')
  })

  it('returns null when the position did not change (no direction to infer)', () => {
    expect(inferScrollDirection(420, 420)).toBeNull()
  })

  it('treats a one-pixel delta as a direction (scrollbar nudge / momentum tail)', () => {
    expect(inferScrollDirection(100, 101)).toBe('newer')
    expect(inferScrollDirection(100, 99)).toBe('older')
  })
})

describe('computeKeepNewest', () => {
  const msg = (id: string, seq: bigint) => ({ id, seq } as AgentChatMessage)
  const anchor = (id: string) => ({ id, offsetWithinRow: 0 })
  const msgs = [
    msg('m1', 1n),
    msg('m2', 2n),
    msg('m3', 3n),
    msg('m4', 4n),
  ]

  it('keeps 0 when following the tail (no anchor), so the store applies the normal cap', () => {
    expect(computeKeepNewest(msgs, null, -1)).toBe(0)
  })

  it('keeps the whole window when the anchor is set but unresolvable (displaced / empty)', () => {
    expect(computeKeepNewest(msgs, anchor('gone'), -1)).toBe(msgs.length)
  })

  it('keeps the server rows from the anchor down to the tail', () => {
    // Anchor at index 1 (m2): keep m2, m3, m4 -> 3 rows.
    expect(computeKeepNewest(msgs, anchor('m2'), 1)).toBe(3)
  })
})

describe('computeBufferAwareKeepNewest', () => {
  const msg = (id: string, seq: bigint) => ({ id, seq } as AgentChatMessage)
  const anchor = (id: string) => ({ id, offsetWithinRow: 0 })
  const msgs = [
    msg('m1', 1n),
    msg('m2', 2n),
    msg('m3', 3n),
    msg('m4', 4n),
  ]
  const mustNotResolve = (): null => {
    throw new Error('anchorAt should not be called')
  }

  it('keeps the lean base cap (0) for a hidden tab (clientHeight 0), without resolving an anchor', () => {
    expect(computeBufferAwareKeepNewest(msgs, 1000, 0, 200, mustNotResolve)).toBe(0)
  })

  it('keeps the whole window when less than a buffer of visible content sits above the top', () => {
    // scrollTop 150 - bufferTargetPx 200 = -50 <= 0 -> keep all (never reap surfaced content).
    expect(computeBufferAwareKeepNewest(msgs, 150, 500, 200, mustNotResolve)).toBe(msgs.length)
  })

  it('delegates to computeKeepNewest from the resolved buffer-top anchor', () => {
    // bufTop = 1000 - 200 = 800; anchorAt resolves to m2 (index 1) -> keep m2..m4 = 3.
    let askedBufTop = -1
    const got = computeBufferAwareKeepNewest(msgs, 1000, 500, 200, (bufTop) => {
      askedBufTop = bufTop
      return anchor('m2')
    })
    expect(askedBufTop).toBe(800)
    expect(got).toBe(3)
  })

  it('keeps the whole window when the buffer top is unresolvable (anchorAt null)', () => {
    expect(computeBufferAwareKeepNewest(msgs, 1000, 500, 200, () => null)).toBe(msgs.length)
  })
})
