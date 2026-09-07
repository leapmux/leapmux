import type { MessageInitShape } from '@bufbuild/protobuf'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  AgentInputKind,
  AgentInputState,
  QueuedAgentInputSchema,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveQueueDrop, resolveQueueMove } from './agentInputQueueDrop'

function item(id: string, overrides: MessageInitShape<typeof QueuedAgentInputSchema> = {}) {
  return create(QueuedAgentInputSchema, {
    id,
    agentId: 'agent-1',
    text: `text ${id}`,
    kind: AgentInputKind.USER_MESSAGE,
    state: AgentInputState.QUEUED,
    ...overrides,
  })
}

// A pointer drag cannot be reproduced here: solid-dnd activates on real
// pointer geometry and collision detection, which jsdom does not supply. The
// gesture is covered in tests/e2e/108-agent-input-queue.spec.ts; the
// arithmetic it feeds is covered directly.
describe('resolveQueueDrop', () => {
  const three = [item('one'), item('two'), item('three')]

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
    const list = [item('one', { state: AgentInputState.DISPATCHING }), item('two')]
    expect(resolveQueueDrop(list, 'qi-one', 'qi-two')).toBeUndefined()
  })

  it('refuses to displace a dispatching row', () => {
    // The Worker is already sending it, so its slot is not the user's to take.
    const list = [item('one', { state: AgentInputState.DISPATCHING }), item('two')]
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
  const three = [item('one'), item('two'), item('three')]

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
    const two = [item('one'), item('two')]
    expect(resolveQueueMove(two, 0, 1)).toEqual({ moved: expect.objectContaining({ id: 'one' }), beforeInputId: '' })
    expect(resolveQueueMove(two, 1, 0)).toEqual({ moved: expect.objectContaining({ id: 'two' }), beforeInputId: 'one' })
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
    const list = [item('one', { state: AgentInputState.DISPATCHING }), item('two')]
    expect(resolveQueueMove(list, 0, 1)).toBeUndefined()
    expect(resolveQueueMove(list, 1, 0)).toBeUndefined()
  })

  it('refuses an empty list', () => {
    expect(resolveQueueMove([], 0, 1)).toBeUndefined()
  })
})
