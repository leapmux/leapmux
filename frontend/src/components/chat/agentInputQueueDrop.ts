import type { QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentInputState } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * The whole reorder policy of the agent input queue, as pure functions.
 *
 * Its own module, because a pointer drag cannot be reproduced in a unit test:
 * solid-dnd activates on real pointer geometry and collision detection, which
 * jsdom does not supply. The gesture is covered end to end in
 * `tests/e2e/108-agent-input-queue.spec.ts`; the arithmetic is covered beside
 * this file, with no component, no drag library and no icons to import.
 */

/**
 * The sortable id of a row, which is also the row's key.
 *
 * Prefixed, and NOT the `queued-input-<id>` test id: solid-dnd registers a
 * droppable under the same string, so the two namespaces must not be able to
 * collide.
 */
export function dragIdOf(item: QueuedAgentInput): string {
  return `qi-${item.id}`
}

/** Lock inputs that the provider can already read. */
export function isLockedInputState(state: AgentInputState): boolean {
  return state === AgentInputState.DISPATCHING || state === AgentInputState.AWAITING_ECHO
}

/** Find the first input that does not await an echo, or return -1. */
export function queueHeadIndex(items: readonly QueuedAgentInput[]): number {
  return items.findIndex(item => item.state !== AgentInputState.AWAITING_ECHO)
}

/** The move a reorder stands for, in the shape `MoveQueuedAgentInput` takes. */
export interface QueueMove {
  moved: QueuedAgentInput
  beforeInputId: string
}

/**
 * The move from one index to another, or `undefined` when the queue refuses it.
 *
 * The drag and the arrow buttons use this rule to permit or refuse a move.
 * The disabled state of each button uses the same rule.
 *
 * `beforeInputId` identifies the item that must follow the moved item.
 * MoveQueuedAgentInput defines this order. The Worker takes the item out of the list
 * first. It then puts the item back in front of `beforeInputId`. An empty
 * string moves the item to the end.
 *
 * A downward move points at the item after the target, because the removal
 * already shifted every item below the source up by one slot. An upward move
 * points at the target itself.
 *
 * DISPATCHING and AWAITING_ECHO lock a row. Neither endpoint can be locked.
 * The provider can already read that input, so a move must not displace it.
 */
export function resolveQueueMove(
  items: readonly QueuedAgentInput[],
  fromIndex: number,
  toIndex: number,
): QueueMove | undefined {
  const moved = items[fromIndex]
  const target = items[toIndex]
  if (!moved || !target || fromIndex === toIndex)
    return undefined
  if (isLockedInputState(moved.state) || isLockedInputState(target.state))
    return undefined
  return {
    moved,
    beforeInputId: fromIndex < toIndex ? (items[toIndex + 1]?.id ?? '') : target.id,
  }
}

/** {@link resolveQueueMove}, addressed by sortable id rather than by index. */
export function resolveQueueDrop(
  items: readonly QueuedAgentInput[],
  draggableId: string,
  droppableId: string,
): QueueMove | undefined {
  return resolveQueueMove(
    items,
    items.findIndex(candidate => dragIdOf(candidate) === draggableId),
    items.findIndex(candidate => dragIdOf(candidate) === droppableId),
  )
}
