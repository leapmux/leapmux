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

/** The move a reorder stands for, in the shape `MoveQueuedAgentInput` takes. */
export interface QueueMove {
  moved: QueuedAgentInput
  beforeInputId: string
}

/**
 * The move from one index to another, or `undefined` when the queue refuses it.
 *
 * ONE definition of which reorder is legal. Every path reads it: the drag, the
 * two arrow buttons, and the `disabled` prop of each of those buttons. The rule
 * lived at five sites before, and two of them already disagreed.
 *
 * `beforeInputId` carries the Worker's BEFORE semantics, which
 * MoveQueuedAgentInput defines. The Worker takes the item out of the list
 * first. It then puts the item back in front of `beforeInputId`. An empty
 * string moves the item to the end.
 *
 * So a DOWNWARD move points at the item AFTER the target, because the removal
 * already shifted every item below the source up by one slot. An upward move
 * points at the target itself.
 *
 * A DISPATCHING row is refused at BOTH ends. It cannot move, and it cannot be
 * displaced, because the Worker already sends it.
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
  if (moved.state === AgentInputState.DISPATCHING || target.state === AgentInputState.DISPATCHING)
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
