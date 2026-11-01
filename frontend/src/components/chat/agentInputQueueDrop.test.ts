import type { MessageInitShape } from '@bufbuild/protobuf'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  AgentInputKind,
  AgentInputState,
  QueuedAgentInputSchema,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { isLockedInputState, movableSegmentIds, queueHeadIndex, resolveQueueDrop, resolveQueueMove, shareReorderSegment } from './agentInputQueueDrop'

describe('queueHeadIndex', () => {
  it('skips awaiting-echo rows', () => {
    expect(queueHeadIndex([])).toBe(-1)
    const pending = locked('pending', AgentInputState.AWAITING_ECHO)
    expect(queueHeadIndex([pending])).toBe(-1)
    expect(queueHeadIndex([
      pending,
      item('head'),
    ])).toBe(1)
  })
})

describe('isLockedInputState', () => {
  it('locks dispatching and awaiting-echo inputs only', () => {
    expect(isLockedInputState(AgentInputState.DISPATCHING)).toBe(true)
    expect(isLockedInputState(AgentInputState.AWAITING_ECHO)).toBe(true)
    for (const state of [AgentInputState.QUEUED, AgentInputState.FAILED, AgentInputState.DELIVERY_UNCERTAIN])
      expect(isLockedInputState(state)).toBe(false)
  })
})

/**
 * A queued row in segment 1 unless the overrides state another one. Each
 * fixture states the segments that the Worker would send for its queue: the
 * browser compares them and never derives them.
 */
function item(id: string, overrides: MessageInitShape<typeof QueuedAgentInputSchema> = {}) {
  return create(QueuedAgentInputSchema, {
    id,
    text: `text ${id}`,
    kind: AgentInputKind.USER_MESSAGE,
    state: AgentInputState.QUEUED,
    reorderSegment: 1,
    ...overrides,
  })
}

/** A locked row, which the Worker gives segment 0. */
function locked(id: string, state: AgentInputState) {
  return item(id, { state, reorderSegment: 0 })
}

describe('shareReorderSegment', () => {
  it('pairs two rows of one segment', () => {
    expect(shareReorderSegment(item('a'), item('b'))).toBe(true)
  })

  it('separates rows of different segments', () => {
    expect(shareReorderSegment(item('a'), item('b', { reorderSegment: 2 }))).toBe(false)
  })

  it('pairs no locked row, not even with another locked row or itself', () => {
    const sending = locked('sending', AgentInputState.DISPATCHING)
    expect(shareReorderSegment(sending, locked('echo', AgentInputState.AWAITING_ECHO))).toBe(false)
    expect(shareReorderSegment(sending, sending)).toBe(false)
  })
})

// A pointer drag cannot be reproduced here: solid-dnd activates on real
// pointer geometry and collision detection, which jsdom does not supply. The
// gesture is covered in tests/e2e/108-agent-input-queue.spec.ts; the
// arithmetic it feeds is covered directly.
describe('resolveQueueDrop', () => {
  const three = [
    item('one'),
    item('two'),
    item('three'),
  ]

  it('points an upward drop at the drop target itself', () => {
    expect(resolveQueueDrop(three, 'qi-two', 'qi-one')).toEqual({
      moved: expect.objectContaining({ id: 'two' }),
      beforeInputId: 'one',
    })
  })

  it('points a downward drop at the item after the drop target', () => {
    // The removal shifts everything below up by one, so aiming at the target
    // itself would land the row one slot short.
    expect(resolveQueueDrop(three, 'qi-one', 'qi-two')).toEqual({
      moved: expect.objectContaining({ id: 'one' }),
      beforeInputId: 'three',
    })
  })

  it('sends a drop on the last row to the end', () => {
    expect(resolveQueueDrop(three, 'qi-one', 'qi-three')).toEqual({
      moved: expect.objectContaining({ id: 'one' }),
      beforeInputId: '',
    })
  })

  it('refuses a drop on the dragged row itself', () => {
    expect(resolveQueueDrop(three, 'qi-two', 'qi-two')).toBeUndefined()
  })

  it('refuses an id that is not in the list', () => {
    expect(resolveQueueDrop(three, 'qi-nope', 'qi-one')).toBeUndefined()
    expect(resolveQueueDrop(three, 'qi-one', 'qi-nope')).toBeUndefined()
  })

  it('refuses to move a dispatching row', () => {
    const list = [
      locked('one', AgentInputState.DISPATCHING),
      item('two'),
    ]
    expect(resolveQueueDrop(list, 'qi-one', 'qi-two')).toBeUndefined()
  })

  it('refuses to displace a dispatching row', () => {
    // The Worker is already sending it, so its slot is not the user's to take.
    const list = [
      locked('one', AgentInputState.DISPATCHING),
      item('two'),
    ]
    expect(resolveQueueDrop(list, 'qi-two', 'qi-one')).toBeUndefined()
  })

  it('refuses an empty list', () => {
    expect(resolveQueueDrop([], 'qi-one', 'qi-two')).toBeUndefined()
  })
})

// The one definition of a legal reorder. `resolveQueueDrop`, both arrow
// buttons, and the `disabled` prop of each button all read it, so a case that
// passes here holds for every path.
describe('resolveQueueMove', () => {
  it('refuses to move an awaiting-echo row or onto one', () => {
    const items = [
      locked('sent', AgentInputState.AWAITING_ECHO),
      item('queued'),
    ]
    expect(resolveQueueMove(items, 0, 1)).toBeUndefined()
    expect(resolveQueueMove(items, 1, 0)).toBeUndefined()
  })

  const three = [
    item('one'),
    item('two'),
    item('three'),
  ]

  it('points an upward move at the item one slot above', () => {
    expect(resolveQueueMove(three, 1, 0)).toEqual({
      moved: expect.objectContaining({ id: 'two' }),
      beforeInputId: 'one',
    })
  })

  it('points a downward move two slots below, where the removal leaves the gap', () => {
    expect(resolveQueueMove(three, 0, 1)).toEqual({
      moved: expect.objectContaining({ id: 'one' }),
      beforeInputId: 'three',
    })
  })

  it('sends a move onto the last row to the end', () => {
    expect(resolveQueueMove(three, 0, 2)).toEqual({
      moved: expect.objectContaining({ id: 'one' }),
      beforeInputId: '',
    })
  })

  it('moves the last row up over its neighbour', () => {
    expect(resolveQueueMove(three, 2, 1)).toEqual({
      moved: expect.objectContaining({ id: 'three' }),
      beforeInputId: 'two',
    })
  })

  it('handles a two-item list in both directions', () => {
    const two = [
      item('one'),
      item('two'),
    ]
    expect(resolveQueueMove(two, 0, 1)).toEqual({
      moved: expect.objectContaining({ id: 'one' }),
      beforeInputId: '',
    })
    expect(resolveQueueMove(two, 1, 0)).toEqual({
      moved: expect.objectContaining({ id: 'two' }),
      beforeInputId: 'one',
    })
  })

  it('refuses a move to the row itself', () => {
    expect(resolveQueueMove(three, 1, 1)).toBeUndefined()
  })

  it('refuses an index off either end, which is what disables the arrows', () => {
    // Move Up on the head asks for -1, and Move Down on the tail asks for 3.
    // The two buttons read exactly this, so an out-of-range index must be a
    // refusal and never a throw.
    expect(resolveQueueMove(three, 0, -1)).toBeUndefined()
    expect(resolveQueueMove(three, 2, 3)).toBeUndefined()
  })

  it('refuses to move a dispatching row and refuses to displace one', () => {
    const list = [
      locked('one', AgentInputState.DISPATCHING),
      item('two'),
    ]
    expect(resolveQueueMove(list, 0, 1)).toBeUndefined()
    expect(resolveQueueMove(list, 1, 0)).toBeUndefined()
  })

  it('refuses an empty list', () => {
    expect(resolveQueueMove([], 0, 1)).toBeUndefined()
  })
})

// Reorder segments: the Worker stamps each item with the segment it belongs
// to, and a legal move never leaves its segment.
describe('resolveQueueMove across reorder segments', () => {
  it('refuses a move between two segments, such as an immediate and a future item', () => {
    const list = [
      item('now'),
      item('later', { scheduledAt: '2030-01-01T09:00:00.000Z', reorderSegment: 2 }),
    ]
    expect(resolveQueueMove(list, 1, 0)).toBeUndefined()
    expect(resolveQueueMove(list, 0, 1)).toBeUndefined()
  })

  it('moves items that share one segment, such as one scheduled instant', () => {
    const at = { scheduledAt: '2030-01-01T09:00:00.000Z', reorderSegment: 2 }
    const list = [
      item('now'),
      item('a', at),
      item('b', at),
    ]
    expect(resolveQueueMove(list, 2, 1)?.beforeInputId).toBe('a')
  })

  it('takes the segment end when a downward move reaches the next segment', () => {
    const list = [
      item('now-1'),
      item('now-2'),
      item('later', { scheduledAt: '2030-01-01T09:00:00.000Z', reorderSegment: 2 }),
    ]
    // Moving now-1 past now-2 lands at the end of the immediate segment, not
    // before the future item.
    expect(resolveQueueMove(list, 0, 1)?.beforeInputId).toBe('')
  })

  it('takes the segment end when a downward move reaches a locked row', () => {
    const list = [
      item('one'),
      item('two'),
      locked('sending', AgentInputState.DISPATCHING),
    ]
    // The Worker refuses a locked `beforeInputId`, and resolves the empty one
    // as the slot before the locked row.
    expect(resolveQueueMove(list, 0, 1)?.beforeInputId).toBe('')
    expect(resolveQueueMove(list, 1, 0)?.beforeInputId).toBe('one')
  })

  it('refuses a move whose span crosses a locked row', () => {
    const list = [
      item('one'),
      locked('dispatching', AgentInputState.DISPATCHING),
      item('three', { reorderSegment: 2 }),
    ]
    expect(resolveQueueMove(list, 2, 0)).toBeUndefined()
    expect(resolveQueueMove(list, 0, 2)).toBeUndefined()
  })
})

describe('movableSegmentIds', () => {
  it('marks rows whose segment holds another row', () => {
    const list = [
      item('now-1'),
      item('now-2'),
      locked('locked', AgentInputState.AWAITING_ECHO),
      item('now-3', { reorderSegment: 2 }),
      item('later-1', { scheduledAt: '2030-01-01T09:00:00.000Z', reorderSegment: 3 }),
      item('later-2', { scheduledAt: '2030-01-01T10:00:00.000Z', reorderSegment: 4 }),
    ]
    expect([
      ...movableSegmentIds(list),
    ].sort()).toEqual(['now-1', 'now-2'])
  })

  it('marks nothing in a queue of one row per segment', () => {
    const list = [
      item('now'),
      item('later', { scheduledAt: '2030-01-01T09:00:00.000Z', reorderSegment: 2 }),
    ]
    expect(movableSegmentIds(list)).toEqual(new Set())
  })

  it('marks no locked rows, however many there are', () => {
    const list = [
      locked('a', AgentInputState.DISPATCHING),
      locked('b', AgentInputState.AWAITING_ECHO),
    ]
    expect(movableSegmentIds(list)).toEqual(new Set())
  })

  it('marks nothing in an empty queue', () => {
    expect(movableSegmentIds([])).toEqual(new Set())
  })
})
