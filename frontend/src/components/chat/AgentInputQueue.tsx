import type { CollisionDetector, DragEventHandler } from '@thisbeyond/solid-dnd'
import type { LucideIcon } from 'lucide-solid'
import type { Accessor, Component } from 'solid-js'
import type { AgentInputQueueSnapshot, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { closestCenter, DragDropProvider, SortableProvider } from '@thisbeyond/solid-dnd'
import ArrowDown from 'lucide-solid/icons/arrow-down'
import ArrowUp from 'lucide-solid/icons/arrow-up'
import Pencil from 'lucide-solid/icons/pencil'
import PencilOff from 'lucide-solid/icons/pencil-off'
import RotateCcw from 'lucide-solid/icons/rotate-ccw'
import SendHorizontal from 'lucide-solid/icons/send-horizontal'
import SquarePen from 'lucide-solid/icons/square-pen'
import Trash2 from 'lucide-solid/icons/trash-2'
import TriangleAlert from 'lucide-solid/icons/triangle-alert'
import UserPen from 'lucide-solid/icons/user-pen'
import { createMemo, createSignal, Show } from 'solid-js'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { DragHandle } from '~/components/common/DragHandle'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { GuardedPointerSensor } from '~/components/shell/guardedPointerSensor'
import { AgentInputKind, AgentInputState } from '~/generated/proto/leapmux/v1/agent_pb'
import { attachDragActivators } from '~/lib/dragActivators'
import { createGuardedSortableRow } from '~/lib/dragRow'
import { createKeyedRows, KeyedFor } from '~/lib/keyedRows'
import * as styles from './AgentInputQueue.css'
import { dragIdOf, resolveQueueDrop, resolveQueueMove } from './agentInputQueueDrop'

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

/**
 * The operation a queued input performs.
 *
 * A table with `satisfies`, not a `switch` with a `default`. A new
 * `AgentInputKind` then fails the frontend typecheck instead of rendering as
 * "Message" and telling the user the wrong thing. `UNSPECIFIED` and
 * `USER_MESSAGE` both read "Message" on purpose.
 */
const OPERATION_LABELS = {
  [AgentInputKind.UNSPECIFIED]: 'Message',
  [AgentInputKind.USER_MESSAGE]: 'Message',
  [AgentInputKind.CLEAR_CONTEXT]: 'Clear context',
  [AgentInputKind.COMPACT_CONTEXT]: 'Compact context',
  [AgentInputKind.PLAN_EXECUTION]: 'Execute plan',
  [AgentInputKind.AUTO_CONTINUE]: 'Auto-continue',
  [AgentInputKind.CONTROL_FEEDBACK]: 'Control feedback',
} satisfies Record<AgentInputKind, string>

/**
 * What the queue does with an input right now.
 *
 * The same table shape as `OPERATION_LABELS`, for the same reason.
 */
const STATE_LABELS = {
  [AgentInputState.UNSPECIFIED]: 'Queued',
  [AgentInputState.QUEUED]: 'Queued',
  [AgentInputState.DISPATCHING]: 'Dispatching',
  [AgentInputState.FAILED]: 'Failed',
  [AgentInputState.DELIVERY_UNCERTAIN]: 'Delivery uncertain',
} satisfies Record<AgentInputState, string>

// A Worker ahead of this client sends an enum number that the generated table
// has no key for, so both lookups keep a runtime fallback. protobuf-es passes
// an unknown number through unchanged.
function operationLabel(kind: AgentInputKind): string {
  return OPERATION_LABELS[kind] ?? OPERATION_LABELS[AgentInputKind.UNSPECIFIED]
}

function stateLabel(state: AgentInputState): string {
  return STATE_LABELS[state] ?? STATE_LABELS[AgentInputState.UNSPECIFIED]
}

function attachmentLabel(item: QueuedAgentInput): string {
  return item.attachments
    .map(attachment => `${attachment.filename} (${attachment.size.toLocaleString()} B)`)
    .join(', ')
}

/**
 * The class every icon-only row action carries: Oat's `outline` look plus this
 * row's square geometry.
 *
 * One constant, because `RowAction` and the Delete `ConfirmButton` must render
 * the same square. A second spelling let one of them drift.
 */
const ROW_ACTION_CLASS = `outline ${styles.iconAction}`

/**
 * One action in a queue row.
 *
 * The label never renders: it gives the button its name through the tooltip and
 * through the accessibility tree, which is where an icon-only control has to
 * state it. Every by-name lookup in the tests and in the E2E specs still
 * resolves, because an `aria-label` is what those lookups read.
 */
const RowAction: Component<{
  icon: LucideIcon
  label: string
  disabled?: boolean
  onClick: () => void
}> = props => (
  <Tooltip text={props.label} ariaLabel>
    <button
      class={ROW_ACTION_CLASS}
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
  /**
   * Stable string keys, and a live lookup from a key to its item.
   *
   * `<For>` over the items themselves keys by object REFERENCE, and every
   * snapshot the Worker pushes replaces the whole array with freshly decoded
   * objects. Each of those pushes then disposed and rebuilt every row, which
   * threw away what the row's DOM was holding: an armed Delete button lost its
   * armed state between the user's two clicks, and a drag in flight lost its
   * drop preview. It also rebuilt every Tooltip, every sortable registration
   * and every drag activator of every row, on every queue event.
   *
   * Keys are strings, so the `<For>` inside `KeyedFor` reconciles only when a
   * row is added, removed, or reordered. Every other field is read INSIDE the
   * row through the `item()` accessor, where Solid updates in place.
   */
  const { keys, byKey } = createKeyedRows(items, dragIdOf)
  // O(1) per row, and one pass per change. A per-row `indexOf` would cost the
  // square of the queue length on every snapshot.
  const indexByKey = createMemo(() => new Map(keys().map((key, index) => [key, index])))
  // ONE scan for the whole list, not one scan per row. Each row asks the same
  // question, so a per-row scan costs the square of the queue length on every
  // snapshot that the Worker sends.
  const anyItemIsEdited = createMemo(() => items().some(candidate => !!candidate.editOwnerClientId))

  const handleDragEnd: DragEventHandler = ({ draggable, droppable }) => {
    if (!draggable || !droppable)
      return
    const drop = resolveQueueDrop(items(), String(draggable.id), String(droppable.id))
    if (drop)
      props.onMove(drop.moved, drop.beforeInputId)
  }

  /**
   * `closestCenter`, with every DISPATCHING row taken out of the candidates.
   *
   * `createSortable` registers a droppable for every row, whether or not the
   * queue can displace it, so the Worker's own row won the collision like any
   * other. `SortableProvider` then PREVIEWED that move -- the rows visibly
   * reflowed -- and `handleDragEnd` discarded it, because `resolveQueueMove`
   * refuses a DISPATCHING target. The row snapped back and nothing said why.
   *
   * Filtering here keeps the preview and the drop on one answer: the pointer
   * lands on the first LEGAL slot instead. `resolveQueueMove` stays the single
   * definition of a legal reorder; this only stops the pointer from selecting a
   * target that function will refuse.
   */
  const dropTarget: CollisionDetector = (draggable, droppables, context) =>
    closestCenter(
      draggable,
      droppables.filter(candidate => byKey().get(String(candidate.id))?.state !== AgentInputState.DISPATCHING),
      context,
    )

  const move = (fromIndex: number, toIndex: number) => {
    const resolved = resolveQueueMove(items(), fromIndex, toIndex)
    if (resolved)
      props.onMove(resolved.moved, resolved.beforeInputId)
  }

  /**
   * Whether this key's row can be dragged, resolved through the LIVE lookup.
   *
   * Read through `byKey()` and never from an item the row captured at mount.
   * The row keeps its identity now, so a captured item would freeze this at the
   * state the row started with, and a row that becomes DISPATCHING would keep a
   * live grip and live drag activators.
   */
  const canDragKey = (key: string) => {
    const item = byKey().get(key)
    return !!item && item.state !== AgentInputState.DISPATCHING
  }

  /**
   * The per-row drag wiring, created in the FOR-ROW owner.
   *
   * Outside the `<Show>` that resolves the item, so a tick where the lookup
   * misses cannot dispose the sortable and drop a drag mid-gesture. The key is
   * the row's identity for the whole of its life, so the sortable id is fixed
   * at creation.
   */
  const setupRowDnd = (key: string) => {
    const dragRow = createGuardedSortableRow(key)
    const [rowEl, setRowEl] = createSignal<HTMLElement>()
    // Fine-pointer presses on the row body drag it; touch presses do not, so a
    // swipe still scrolls the queue. The grip carries the raw activators, which
    // is the only route a touch drag takes.
    // eslint-disable-next-line solid/reactivity -- attachDragActivators reads this inside its own createEffect
    attachDragActivators(() => (canDragKey(key) ? rowEl() : undefined), dragRow.bodyActivators, { touch: 'block' })
    return { dragRow, rowEl, setRowEl, canDrag: () => canDragKey(key) }
  }

  const renderRow = (item: Accessor<QueuedAgentInput>, key: string, dnd: ReturnType<typeof setupRowDnd>) => {
    const index = () => indexByKey().get(key) ?? -1
    const isHead = () => index() === 0
    const isDispatching = () => item().state === AgentInputState.DISPATCHING
    const editedByMe = () => item().editOwnerClientId === props.clientId
    const editedByOther = () => !!item().editOwnerClientId && !editedByMe()
    // This row has no edit owner, so the shared scan needs no exclusion
    // for this row: it can only add a false term.
    const requiresTakeover = () => editedByOther() || (!item().editOwnerClientId && anyItemIsEdited())
    const retryable = () => item().state === AgentInputState.FAILED || item().state === AgentInputState.DELIVERY_UNCERTAIN
    // The edit slot holds ONE button, and who owns the edit decides
    // which. A memo keeps the four cases in one place, so the icon,
    // the name and the handler of a case cannot drift apart.
    const editAction = createMemo(() => {
      if (requiresTakeover())
        return { icon: UserPen, label: 'Take Over', onClick: () => props.onEdit(item(), true) }
      if (!editedByMe()) {
        return {
          icon: Pencil,
          label: 'Edit',
          disabled: isDispatching(),
          onClick: () => props.onEdit(item(), false),
        }
      }
      return props.activeEditInputId === item().id
        ? { icon: PencilOff, label: 'Cancel Edit', onClick: () => props.onCancelEdit(item()) }
        : { icon: SquarePen, label: 'Resume Edit', onClick: () => props.onEdit(item(), false) }
    })

    return (
      <div
        ref={(el) => {
          dnd.setRowEl(el)
          // Node registration only. Activation lives on the guarded
          // body and on the grip, never on the whole row.
          dnd.dragRow.ref(el)
        }}
        class={styles.item}
        classList={{
          [styles.itemDragging]: dnd.dragRow.isActiveDraggable,
          [styles.itemDraggable]: dnd.canDrag(),
        }}
        style={dnd.dragRow.style()}
        data-testid={`queued-input-${item().id}`}
      >
        {/*
          Kept in the row whether or not this row can move, so a DISPATCHING row
          does not shift its text one slot left. `dragHandleInert` hides it
          without removing its box, and the activators go away with it -- an
          affordance that cannot drag must not look like one.
        */}
        <DragHandle
          activators={() => (dnd.canDrag() ? dnd.dragRow.gripActivators() : undefined)}
          class={dnd.canDrag() ? undefined : styles.dragHandleInert}
          testId={`queue-drag-handle-${item().id}`}
        />
        <div class={styles.body}>
          <div class={styles.preview}>{item().text || '(attachments only)'}</div>
          <div class={styles.metadata}>
            {operationLabel(item().kind)}
            {' · '}
            {stateLabel(item().state)}
            <Show when={item().attachments.length > 0}>
              {` · ${attachmentLabel(item())}`}
            </Show>
          </div>
          <Show when={item().error}><div class={styles.error}>{item().error}</div></Show>
        </div>
        <div class={styles.actions}>
          <RowAction
            icon={ArrowUp}
            label="Move Up"
            disabled={!resolveQueueMove(items(), index(), index() - 1)}
            onClick={() => move(index(), index() - 1)}
          />
          <RowAction
            icon={ArrowDown}
            label="Move Down"
            disabled={!resolveQueueMove(items(), index(), index() + 1)}
            onClick={() => move(index(), index() + 1)}
          />
          <RowAction
            icon={editAction().icon}
            label={editAction().label}
            disabled={editAction().disabled}
            onClick={() => editAction().onClick()}
          />
          {/*
            Delete takes a second click, because an icon carries no word to read
            before the press and the queued input is gone for good. The armed
            state swaps the bin for a warning sign and turns the outline red,
            and `confirmTooltip` changes the button's name so a screen reader
            hears the armed state too.
          */}
          <ConfirmButton
            class={ROW_ACTION_CLASS}
            tooltip="Delete"
            confirmTooltip="Confirm delete?"
            confirmLabel={<Icon icon={TriangleAlert} size="xs" />}
            disabled={isDispatching()}
            onClick={() => props.onDelete(item())}
          >
            <Icon icon={Trash2} size="xs" />
          </ConfirmButton>
          <Show when={isHead() && retryable() && !item().editOwnerClientId}>
            <RowAction
              icon={RotateCcw}
              label="Retry"
              onClick={() => props.onRetry(item(), item().state === AgentInputState.DELIVERY_UNCERTAIN)}
            />
          </Show>
          {/*
            Steer stays last, so the one button that still shows a word sits at
            the end of the row and the squares before it keep an even rhythm.

            `canSteer` comes from the Worker, which computes it with the same
            expression that the store's steering guard applies. The browser must
            never re-implement that precondition: a new input kind would then
            offer a Steer button that the Worker refuses.
          */}
          <Show when={isHead() && props.supportsSteering && item().canSteer}>
            <Tooltip text="Steer" ariaLabel>
              <button class={styles.steerAction} type="button" onClick={() => props.onSteer(item())}>
                <Icon icon={SendHorizontal} size="xs" />
                <span>Steer</span>
              </button>
            </Tooltip>
          </Show>
        </div>
      </div>
    )
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
      <DragDropProvider onDragEnd={handleDragEnd} collisionDetector={dropTarget}>
        {/* The stock pointer sensor plus this app's guards: a touch press only
            starts a drag from a grip, so a finger that swipes the queue still
            scrolls it. See ~/components/shell/guardedPointerSensor.ts. */}
        <GuardedPointerSensor />
        <SortableProvider ids={keys()}>
          <div class={styles.root} data-testid="agent-input-queue">
            <KeyedFor each={keys()} lookup={key => byKey().get(key)} rowSetup={setupRowDnd}>
              {renderRow}
            </KeyedFor>
          </div>
        </SortableProvider>
      </DragDropProvider>
    </Show>
  )
}
