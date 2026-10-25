import type { QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentInputState } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * The browser's half of the agent input queue's reorder policy, as pure
 * functions. The Worker derives which rows may trade places (each row's
 * `reorderSegment`); this module turns a gesture into the move that the
 * Worker accepts.
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

/**
 * Whether two rows sit in one reorder segment, so a move between them is legal.
 *
 * The Worker derives each row's `reorderSegment` from the same rule that
 * `Store.Move` enforces: a maximal run of contiguous, unlocked rows of one
 * reorder group. An immediate item and a future one never share a segment, two
 * future items share one only at exactly the same scheduled instant, and a
 * locked row (DISPATCHING or AWAITING_ECHO) carries 0, which matches nothing.
 * The browser compares the values and never derives them, because the
 * Worker's clock decides which items are still future.
 */
export function shareReorderSegment(a: QueuedAgentInput, b: QueuedAgentInput): boolean {
  return a.reorderSegment !== 0 && a.reorderSegment === b.reorderSegment
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
 * string moves the item to the end of its segment.
 *
 * A downward move points at the item after the target, because the removal
 * already shifted every item below the source up by one slot. An upward move
 * points at the target itself.
 *
 * A move stays inside ONE reorder segment (see {@link shareReorderSegment}):
 * the moved item and its target must share one. A downward move whose
 * follower is outside that segment -- absent, locked, or in another group --
 * takes the SEGMENT END (an empty `beforeInputId`), which the Worker resolves
 * as the end of the moved item's segment, not the queue's end. Specifying a locked
 * follower would be refused.
 */
export function resolveQueueMove(
  items: readonly QueuedAgentInput[],
  fromIndex: number,
  toIndex: number,
): QueueMove | undefined {
  const moved = items[fromIndex]
  const target = items[toIndex]
  if (!moved || !target || fromIndex === toIndex || !shareReorderSegment(moved, target))
    return undefined
  if (fromIndex < toIndex) {
    const follower = items[toIndex + 1]
    if (!follower || !shareReorderSegment(moved, follower))
      return { moved, beforeInputId: '' }
    return { moved, beforeInputId: follower.id }
  }
  return { moved, beforeInputId: target.id }
}

/**
 * The ids whose rows have at least one legal destination: their segment holds
 * another row. One pass, because every row asks the same question and a
 * per-row scan costs the square of the queue length on every snapshot.
 */
export function movableSegmentIds(items: readonly QueuedAgentInput[]): Set<string> {
  const sizes = new Map<number, number>()
  for (const item of items) {
    if (item.reorderSegment !== 0)
      sizes.set(item.reorderSegment, (sizes.get(item.reorderSegment) ?? 0) + 1)
  }
  const movable = new Set<string>()
  for (const item of items) {
    if ((sizes.get(item.reorderSegment) ?? 0) >= 2)
      movable.add(item.id)
  }
  return movable
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
