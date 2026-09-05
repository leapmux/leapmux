import type { Component } from 'solid-js'
import type { AgentInputQueueSnapshot, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { For, Show } from 'solid-js'
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

function isSteerableInputKind(kind: AgentInputKind): boolean {
  return kind === AgentInputKind.USER_MESSAGE
    || kind === AgentInputKind.AUTO_CONTINUE
    || kind === AgentInputKind.CONTROL_FEEDBACK
}

function attachmentLabel(item: QueuedAgentInput): string {
  return item.attachments
    .map(attachment => `${attachment.filename} (${attachment.size.toLocaleString()} B)`)
    .join(', ')
}

export const AgentInputQueue: Component<AgentInputQueueProps> = (props) => {
  let draggedId = ''
  const items = () => props.snapshot?.items ?? []
  const hasActiveRegularTurn = () => {
    const kind = props.snapshot?.activeTurnKind
    return props.snapshot?.activeTurn === true
      && kind !== undefined
      && kind !== AgentInputKind.UNSPECIFIED
      && kind !== AgentInputKind.CLEAR_CONTEXT
      && kind !== AgentInputKind.COMPACT_CONTEXT
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
      <div class={styles.root} data-testid="agent-input-queue">
        <For each={items()}>
          {(item, index) => {
            const isHead = () => index() === 0
            const editedByMe = () => item.editOwnerClientId === props.clientId
            const editedByOther = () => !!item.editOwnerClientId && !editedByMe()
            const requiresTakeover = () => editedByOther() || (
              !item.editOwnerClientId
              && items().some(candidate => candidate.id !== item.id && !!candidate.editOwnerClientId)
            )
            const retryable = () => item.state === AgentInputState.FAILED || item.state === AgentInputState.DELIVERY_UNCERTAIN
            return (
              <div
                class={styles.item}
                draggable={item.state !== AgentInputState.DISPATCHING}
                onDragStart={() => { draggedId = item.id }}
                onDragOver={event => event.preventDefault()}
                onDrop={() => {
                  const dragged = items().find(candidate => candidate.id === draggedId)
                  if (dragged && dragged.id !== item.id && item.state !== AgentInputState.DISPATCHING)
                    props.onMove(dragged, item.id)
                  draggedId = ''
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
                  <Show when={isHead() && hasActiveRegularTurn() && props.supportsSteering && isSteerableInputKind(item.kind) && item.state === AgentInputState.QUEUED && !item.editOwnerClientId}>
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
