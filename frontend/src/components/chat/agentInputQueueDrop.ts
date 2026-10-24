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

/**
 * The reorder group the Worker derived for one item: the empty string is the
 * immediate/past-due group, and a future item's group is its scheduled
 * instant. The browser COMPARES this value and never re-derives it, because
 * the browser clock can disagree with the Worker's -- an item whose deadline
 * just passed must keep the group the Worker stamped until the next snapshot.
 */
export function reorderGroupOf(item: QueuedAgentInput): string {
  return item.reorderGroup
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
 * A move stays inside ONE reorder group: the moved item and its target must
 * carry the same Worker-derived `reorderGroup`, and every row between them
 * must too, unlocked. An immediate item and a future one never trade places,
 * and two future items share a segment only at exactly the same scheduled
 * instant. A downward move whose follower belongs to another group takes the
 * SEGMENT END (an empty `beforeInputId`), which the Worker resolves as the end
 * of the moved item's compatible unlocked segment -- not the queue's end.
 *
 * DISPATCHING and AWAITING_ECHO lock a row. Neither endpoint can be locked,
 * and no locked row may sit between them. The provider can already read that
 * input, so a move must not displace it.
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
  if (reorderGroupOf(moved) !== reorderGroupOf(target))
    return undefined
  const from = Math.min(fromIndex, toIndex)
  const to = Math.max(fromIndex, toIndex)
  for (let index = from; index <= to; index++) {
    const between = items[index]
    if (!between || isLockedInputState(between.state) || reorderGroupOf(between) !== reorderGroupOf(moved))
      return undefined
  }
  if (fromIndex < toIndex) {
    const follower = items[toIndex + 1]
    if (!follower)
      return { moved, beforeInputId: '' }
    if (isLockedInputState(follower.state))
      return undefined
    if (reorderGroupOf(follower) !== reorderGroupOf(moved))
      return { moved, beforeInputId: '' }
    return { moved, beforeInputId: follower.id }
  }
  return { moved, beforeInputId: target.id }
}

/**
 * The ids whose rows have at least one legal destination: their contiguous,
 * unlocked, same-group segment holds another row. One scan, because every row
 * asks the same question and a per-row scan costs the square of the queue
 * length on every snapshot.
 */
export function movableSegmentIds(items: readonly QueuedAgentInput[]): Set<string> {
  const movable = new Set<string>()
  let run: QueuedAgentInput[] = []
  const flush = () => {
    if (run.length >= 2)
      run.forEach(item => movable.add(item.id))
    run = []
  }
  for (const item of items) {
    if (isLockedInputState(item.state)) {
      flush()
      continue
    }
    const last = run[run.length - 1]
    if (last !== undefined && reorderGroupOf(last) !== reorderGroupOf(item)) {
      flush()
    }
    run.push(item)
  }
  flush()
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
