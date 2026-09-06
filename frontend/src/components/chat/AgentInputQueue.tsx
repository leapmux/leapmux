import type { DragEventHandler } from '@thisbeyond/solid-dnd'
import type { LucideIcon } from 'lucide-solid'
import type { Component } from 'solid-js'
import type { AgentInputQueueSnapshot, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { closestCenter, DragDropProvider, SortableProvider } from '@thisbeyond/solid-dnd'
import ArrowDown from 'lucide-solid/icons/arrow-down'
import ArrowUp from 'lucide-solid/icons/arrow-up'
import Navigation from 'lucide-solid/icons/navigation'
import Pencil from 'lucide-solid/icons/pencil'
import PencilOff from 'lucide-solid/icons/pencil-off'
import RotateCcw from 'lucide-solid/icons/rotate-ccw'
import SquarePen from 'lucide-solid/icons/square-pen'
import Trash2 from 'lucide-solid/icons/trash-2'
import TriangleAlert from 'lucide-solid/icons/triangle-alert'
import UserPen from 'lucide-solid/icons/user-pen'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { DragHandle } from '~/components/common/DragHandle'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { GuardedPointerSensor } from '~/components/shell/guardedPointerSensor'
import { AgentInputKind, AgentInputState } from '~/generated/proto/leapmux/v1/agent_pb'
import { attachDragActivators } from '~/lib/dragActivators'
import { createGuardedSortableRow } from '~/lib/dragRow'
import * as styles from './AgentInputQueue.css'

export interface AgentInputQueueProps {
  snapshot?: AgentInputQueueSnapshot
  clientId: string
  activeEditInputId?: string
  supportsSteering: boolean
  onEdit: (item: QueuedAgentInput, takeover: boolean) => void
  onCancelEdit: (item: QueuedAgentInput) => void
  onDelete: (item: QueuedAgentInput) => void
  onMove: (item: QueuedAgentInput, beforeInputId: string) => void
  onRetry: (item: QueuedAgentInput, confirmUncertain: boolean) => void
  onSteer: (item: QueuedAgentInput) => void
}

function operationLabel(kind: AgentInputKind): string {
  switch (kind) {
    case AgentInputKind.CLEAR_CONTEXT: return 'Clear context'
    case AgentInputKind.COMPACT_CONTEXT: return 'Compact context'
    case AgentInputKind.PLAN_EXECUTION: return 'Execute plan'
    case AgentInputKind.AUTO_CONTINUE: return 'Auto-continue'
    case AgentInputKind.CONTROL_FEEDBACK: return 'Control feedback'
    default: return 'Message'
  }
}

function stateLabel(state: AgentInputState): string {
  switch (state) {
    case AgentInputState.DISPATCHING: return 'Dispatching'
    case AgentInputState.FAILED: return 'Failed'
    case AgentInputState.DELIVERY_UNCERTAIN: return 'Delivery uncertain'
    default: return 'Queued'
  }
}

/**
 * The sortable id of a row. Prefixed, and NOT the `queued-input-<id>` test id:
 * solid-dnd registers a droppable under the same string, so the two namespaces
 * must not be able to collide.
 */
function dragIdOf(item: QueuedAgentInput): string {
  return `qi-${item.id}`
}

/**
 * The move a drop stands for, or `undefined` when the drop changes nothing.
 *
 * Pure, and exported, because this is the whole of the reorder logic and a
 * pointer drag cannot be reproduced in a unit test: solid-dnd activates on real
 * pointer geometry and collision detection. The gesture is covered end to end
 * in `tests/e2e/108-agent-input-queue.spec.ts`; the arithmetic is covered here.
 *
 * `beforeInputId` carries the Worker's BEFORE semantics, which
 * MoveQueuedAgentInput defines: the item comes out of the list first, then goes
 * back in front of `beforeInputId`, and an empty string means the end. So a
 * DOWNWARD drop points at the item AFTER the drop target -- the removal already
 * shifted everything below up by one -- and an upward drop points at the target
 * itself. `moveDown` obeys the same rule.
 */
export function resolveQueueDrop(
  items: readonly QueuedAgentInput[],
  draggableId: string,
  droppableId: string,
): { moved: QueuedAgentInput, beforeInputId: string } | undefined {
  const fromIndex = items.findIndex(candidate => dragIdOf(candidate) === draggableId)
  const toIndex = items.findIndex(candidate => dragIdOf(candidate) === droppableId)
  if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex)
    return undefined
  const moved = items[fromIndex]!
  const target = items[toIndex]!
  // A DISPATCHING row is refused at BOTH ends: it cannot be picked up, and it
  // cannot be displaced, because the Worker is already sending it.
  if (moved.state === AgentInputState.DISPATCHING || target.state === AgentInputState.DISPATCHING)
    return undefined
  return {
    moved,
    beforeInputId: fromIndex < toIndex ? (items[toIndex + 1]?.id ?? '') : target.id,
  }
}

function attachmentLabel(item: QueuedAgentInput): string {
  return item.attachments
    .map(attachment => `${attachment.filename} (${attachment.size.toLocaleString()} B)`)
    .join(', ')
}

/**
 * One action in a queue row.
 *
 * The label never renders: it names the button to the tooltip and to the
 * accessibility tree, which is where an icon-only control has to state it.
 * Every by-name lookup in the tests and in the E2E specs still resolves,
 * because an `aria-label` is what those lookups read.
 */
const RowAction: Component<{
  icon: LucideIcon
  label: string
  disabled?: boolean
  onClick: () => void
}> = props => (
  <Tooltip text={props.label} ariaLabel>
    <button
      class={`outline ${styles.iconAction}`}
      type="button"
      disabled={props.disabled}
      onClick={() => props.onClick()}
    >
      <Icon icon={props.icon} size="xs" />
    </button>
  </Tooltip>
)

export const AgentInputQueue: Component<AgentInputQueueProps> = (props) => {
  const items = () => props.snapshot?.items ?? []
  // ONE scan for the whole list, not one scan per row. Each row asks the same
  // question, so a per-row scan costs the square of the queue length on every
  // snapshot that the Worker sends.
  const anyItemIsEdited = createMemo(() => items().some(candidate => !!candidate.editOwnerClientId))
  // `onMove` carries BEFORE semantics, which MoveQueuedAgentInput defines: the
  // Worker takes the item out of the list first, then puts it back in front of
  // `beforeInputId`. An empty `beforeInputId` moves the item to the end.
  //
  // An upward move points at the item one slot above, which is the slot the
  // user wants. A DOWNWARD move points one slot further, at the item TWO slots
  // below, because the removal already shifted the item one slot below up into
  // the vacated slot. Every downward site obeys this: `moveDown` and the
  // downward branch of the drop handler.
  const handleDragEnd: DragEventHandler = ({ draggable, droppable }) => {
    if (!draggable || !droppable)
      return
    const drop = resolveQueueDrop(items(), String(draggable.id), String(droppable.id))
    if (drop)
      props.onMove(drop.moved, drop.beforeInputId)
  }

  const moveUp = (index: number) => {
    if (index > 0 && items()[index - 1]?.state !== AgentInputState.DISPATCHING)
      props.onMove(items()[index]!, items()[index - 1]!.id)
  }
  const moveDown = (index: number) => {
    const list = items()
    if (index >= list.length - 1)
      return
    props.onMove(list[index]!, list[index + 2]?.id ?? '')
  }

  return (
    <Show when={items().length > 0}>
      {/*
        The queue owns its OWN drag context, rather than registering with the
        shell's `SectionDragProvider` the way tab drags do. Nothing ever leaves
        this list -- an input reorders inside its own queue and nowhere else --
        so there is no cross-surface interaction to preserve, and reaching up to
        a sidebar-level context would couple the composer to the shell and break
        it in every test that renders the panel on its own. The shadowing this
        creates is confined to the list's own subtree.
      */}
      <DragDropProvider onDragEnd={handleDragEnd} collisionDetector={closestCenter}>
        {/* The stock pointer sensor plus this app's guards: a touch press only
            starts a drag from a grip, so a finger that swipes the queue still
            scrolls it. See ~/components/shell/guardedPointerSensor.ts. */}
        <GuardedPointerSensor />
        <SortableProvider ids={items().map(dragIdOf)}>
          <div class={styles.root} data-testid="agent-input-queue">
            <For each={items()}>
              {(item, index) => {
                const isHead = () => index() === 0
                const editedByMe = () => item.editOwnerClientId === props.clientId
                const editedByOther = () => !!item.editOwnerClientId && !editedByMe()
                // This row has no edit owner, so the shared scan needs no exclusion
                // for this row: it can only add a false term.
                const requiresTakeover = () => editedByOther() || (!item.editOwnerClientId && anyItemIsEdited())
                const retryable = () => item.state === AgentInputState.FAILED || item.state === AgentInputState.DELIVERY_UNCERTAIN
                // The edit slot holds ONE button, and who owns the edit decides
                // which. A memo keeps the four cases in one place, so the icon,
                // the name and the handler of a case cannot drift apart.
                const editAction = createMemo(() => {
                  if (requiresTakeover())
                    return { icon: UserPen, label: 'Take Over', onClick: () => props.onEdit(item, true) }
                  if (!editedByMe()) {
                    return {
                      icon: Pencil,
                      label: 'Edit',
                      disabled: item.state === AgentInputState.DISPATCHING,
                      onClick: () => props.onEdit(item, false),
                    }
                  }
                  return props.activeEditInputId === item.id
                    ? { icon: PencilOff, label: 'Cancel Edit', onClick: () => props.onCancelEdit(item) }
                    : { icon: SquarePen, label: 'Resume Edit', onClick: () => props.onEdit(item, false) }
                })
                const dragRow = createGuardedSortableRow(dragIdOf(item))
                const canDrag = () => item.state !== AgentInputState.DISPATCHING
                const [rowEl, setRowEl] = createSignal<HTMLElement>()
                // Fine-pointer presses on the row body drag it; touch presses do
                // not, so a swipe still scrolls the queue. The grip above carries
                // the raw activators, which is the only way a touch drag starts.
                attachDragActivators(() => (canDrag() ? rowEl() : undefined), dragRow.bodyActivators, { touch: 'block' })

                return (
                  <div
                    ref={(el: HTMLElement) => {
                      setRowEl(el)
                      // Node registration only. Activation lives on the guarded
                      // body and on the grip, never on the whole row.
                      dragRow.ref(el)
                    }}
                    class={styles.item}
                    classList={{ [styles.itemDragging]: dragRow.isActiveDraggable }}
                    style={dragRow.style()}
                    data-testid={`queued-input-${item.id}`}
                  >
                    {/*
                  Kept in the grid whether or not this row can move, so a
                  DISPATCHING row does not shift its text one column left.
                  `dragHandleInert` hides it without removing its box, and the
                  activators go away with it -- an affordance that cannot drag
                  must not look like one.
                */}
                    <DragHandle
                      activators={() => (canDrag() ? dragRow.gripActivators() : undefined)}
                      class={canDrag() ? undefined : styles.dragHandleInert}
                      testId={`queue-drag-handle-${item.id}`}
                    />
                    <div class={styles.body}>
                      <div class={styles.preview}>{item.text || '(attachments only)'}</div>
                      <div class={styles.metadata}>
                        {operationLabel(item.kind)}
                        {' · '}
                        {stateLabel(item.state)}
                        <Show when={item.attachments.length > 0}>
                          {` · ${attachmentLabel(item)}`}
                        </Show>
                      </div>
                      <Show when={item.error}><div class={styles.error}>{item.error}</div></Show>
                    </div>
                    <div class={styles.actions}>
                      <RowAction
                        icon={ArrowUp}
                        label="Move Up"
                        disabled={index() === 0 || item.state === AgentInputState.DISPATCHING || items()[index() - 1]?.state === AgentInputState.DISPATCHING}
                        onClick={() => moveUp(index())}
                      />
                      <RowAction
                        icon={ArrowDown}
                        label="Move Down"
                        disabled={index() === items().length - 1 || item.state === AgentInputState.DISPATCHING}
                        onClick={() => moveDown(index())}
                      />
                      <RowAction
                        icon={editAction().icon}
                        label={editAction().label}
                        disabled={editAction().disabled}
                        onClick={() => editAction().onClick()}
                      />
                      {/*
                    Delete takes a second click, because an icon carries no
                    word to read before the press and the queued input is gone
                    for good. The armed state swaps the bin for a warning sign
                    and turns the outline red, and `confirmTooltip` renames the
                    button so a screen reader hears the armed state too.
                  */}
                      <ConfirmButton
                        class={`outline ${styles.iconAction}`}
                        tooltip="Delete"
                        confirmTooltip="Confirm delete?"
                        confirmLabel={<Icon icon={TriangleAlert} size="xs" />}
                        disabled={item.state === AgentInputState.DISPATCHING}
                        onClick={() => props.onDelete(item)}
                      >
                        <Icon icon={Trash2} size="xs" />
                      </ConfirmButton>
                      <Show when={isHead() && retryable() && !item.editOwnerClientId}>
                        <RowAction
                          icon={RotateCcw}
                          label="Retry"
                          onClick={() => props.onRetry(item, item.state === AgentInputState.DELIVERY_UNCERTAIN)}
                        />
                      </Show>
                      {/*
                    Steer stays last, so the one button that still shows a word
                    sits at the end of the row and the squares before it keep an
                    even rhythm.

                    `canSteer` comes from the Worker, which computes it with the
                    same expression that the store's steering guard applies. The
                    browser must never re-implement that precondition: a new
                    input kind would then offer a Steer button that the Worker
                    refuses.
                  */}
                      <Show when={isHead() && props.supportsSteering && item.canSteer}>
                        <Tooltip text="Steer" ariaLabel>
                          <button class={styles.steerAction} type="button" onClick={() => props.onSteer(item)}>
                            <Icon icon={Navigation} size="xs" />
                            <span class={styles.steerLabel}>Steer</span>
                          </button>
                        </Tooltip>
                      </Show>
                    </div>
                  </div>
                )
              }}
            </For>
          </div>
        </SortableProvider>
      </DragDropProvider>
    </Show>
  )
}
