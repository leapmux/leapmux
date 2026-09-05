import type { Component } from 'solid-js'
import type { AgentInputQueueSnapshot, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo, For, Show } from 'solid-js'
import { AgentInputKind, AgentInputState } from '~/generated/proto/leapmux/v1/agent_pb'
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

function attachmentLabel(item: QueuedAgentInput): string {
  return item.attachments
    .map(attachment => `${attachment.filename} (${attachment.size.toLocaleString()} B)`)
    .join(', ')
}

export const AgentInputQueue: Component<AgentInputQueueProps> = (props) => {
  let draggedId = ''
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
            return (
              <div
                class={styles.item}
                draggable={item.state !== AgentInputState.DISPATCHING}
                onDragStart={() => { draggedId = item.id }}
                onDragOver={event => event.preventDefault()}
                onDrop={() => {
                  const list = items()
                  const fromIndex = list.findIndex(candidate => candidate.id === draggedId)
                  const toIndex = list.findIndex(candidate => candidate.id === item.id)
                  draggedId = ''
                  if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex || item.state === AgentInputState.DISPATCHING)
                    return
                  // A downward drop points at the item AFTER the drop target,
                  // under the before-semantics rule that `moveDown` obeys, so
                  // the dragged row lands on the drop target's own slot. An
                  // upward drop points at the drop target itself.
                  props.onMove(list[fromIndex]!, fromIndex < toIndex ? (list[toIndex + 1]?.id ?? '') : item.id)
                }}
                data-testid={`queued-input-${item.id}`}
              >
                <span class={styles.drag} aria-hidden="true">⋮⋮</span>
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
                  <button class={styles.action} type="button" onClick={() => moveUp(index())} disabled={index() === 0 || item.state === AgentInputState.DISPATCHING || items()[index() - 1]?.state === AgentInputState.DISPATCHING}>Move Up</button>
                  <button class={styles.action} type="button" onClick={() => moveDown(index())} disabled={index() === items().length - 1 || item.state === AgentInputState.DISPATCHING}>Move Down</button>
                  <Show
                    when={requiresTakeover()}
                    fallback={editedByMe()
                      ? props.activeEditInputId === item.id
                        ? <button class={styles.action} type="button" onClick={() => props.onCancelEdit(item)}>Cancel Edit</button>
                        : <button class={styles.action} type="button" onClick={() => props.onEdit(item, false)}>Resume Edit</button>
                      : <button class={styles.action} type="button" onClick={() => props.onEdit(item, false)} disabled={item.state === AgentInputState.DISPATCHING}>Edit</button>}
                  >
                    <button class={styles.action} type="button" onClick={() => props.onEdit(item, true)}>Take Over</button>
                  </Show>
                  <button class={styles.action} type="button" onClick={() => props.onDelete(item)} disabled={item.state === AgentInputState.DISPATCHING}>Delete</button>
                  <Show when={isHead() && retryable() && !item.editOwnerClientId}>
                    <button class={styles.action} type="button" onClick={() => props.onRetry(item, item.state === AgentInputState.DELIVERY_UNCERTAIN)}>Retry</button>
                  </Show>
                  {/*
                    `canSteer` comes from the Worker, which computes it with the
                    same expression that the store's steering guard applies. The
                    browser must never re-implement that precondition: a new
                    input kind would then offer a Steer button that the Worker
                    refuses.
                  */}
                  <Show when={isHead() && props.supportsSteering && item.canSteer}>
                    <button class={styles.action} type="button" onClick={() => props.onSteer(item)}>Steer</button>
                  </Show>
                </div>
              </div>
            )
          }}
        </For>
      </div>
    </Show>
  )
}
