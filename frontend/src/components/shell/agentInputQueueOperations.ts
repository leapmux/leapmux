import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentInputQueueSnapshot, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import type { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import type { TabView } from '~/stores/tabView'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { createAgentInputEnqueueRetry } from './agentInputEnqueueRetry'

/**
 * The composer's queue commands, built once for the shell.
 *
 * `AgentEditorPanel` takes one callback for each command. Every callback sends
 * a Worker RPC, applies the snapshot that the response carries, and shows one
 * warning when the call fails. That work is a request layer, not a layout
 * decision, so it lives here and `TileRenderer` sends no Worker RPC of its own.
 *
 * The bag holds no per-mount state, so the shell builds it once beside the
 * other renderer-level helpers. The composer remounts on every tab switch, and
 * `enqueueRetry` must outlive that remount.
 */
export function createAgentInputQueueOperations(deps: {
  /** The tab join. Each command resolves its Worker through this. */
  view: TabView
  /** The store that every response snapshot lands in. */
  store: ReturnType<typeof createAgentInputQueueStore>
  /** This browser tab's identity. The edit commands claim the edit lock with it. */
  clientId: () => string
  /** The agent that the composer writes to, or null when no agent tab is focused. */
  focusedAgentId: () => string | null
  /** Move the focused transcript to the live tail. */
  forceScrollToBottom: () => void
}) {
  // Keep ambiguous enqueue attempts across editor unmounts. A user can switch
  // to a file and back while the unchanged draft still needs its original ID.
  const enqueueRetry = createAgentInputEnqueueRetry()
  const queueWorkerID = (item: QueuedAgentInput) => deps.view.getAgentTab(item.agentId)?.workerId ?? ''
  /**
   * Run one queue RPC and apply the snapshot that it answers with.
   *
   * Every response of this family carries the whole queue, so the apply
   * belongs HERE rather than at each call site: a handler that forgets it
   * leaves the panel on a stale queue until the next broadcast arrives. A
   * failure shows one warning and rethrows, so the caller still sees it.
   */
  const runQueueRpc = async <T extends { snapshot?: AgentInputQueueSnapshot }>(label: string, call: () => Promise<T>): Promise<T> => {
    let response: T
    try {
      response = await call()
    }
    catch (error) {
      showWarnToast(label, error)
      throw error
    }
    deps.store.apply(response.snapshot)
    return response
  }
  // The one handler whose caller needs the response: the composer loads the
  // full text and the attachments of the input that it starts to edit.
  const beginQueueEdit = (item: QueuedAgentInput, takeover: boolean) =>
    runQueueRpc('Failed to edit queued input', () => workerRpc.beginQueuedAgentInputEdit(queueWorkerID(item), {
      agentId: item.agentId,
      inputId: item.id,
      clientId: deps.clientId(),
      takeover,
    }))
  const updateQueueItem = async (item: QueuedAgentInput, text: string, fileAttachments: FileAttachment[]) => {
    await runQueueRpc('Failed to save queued input', () => workerRpc.updateQueuedAgentInput(queueWorkerID(item), {
      agentId: item.agentId,
      inputId: item.id,
      clientId: deps.clientId(),
      expectedVersion: item.version,
      text,
      attachments: fileAttachments.map(attachment => ({
        filename: attachment.filename,
        mimeType: attachment.mimeType,
        data: attachment.data,
      })),
    }))
  }
  const cancelQueueEdit = async (item: QueuedAgentInput) => {
    await runQueueRpc('Failed to cancel queued input edit', () => workerRpc.cancelQueuedAgentInputEdit(queueWorkerID(item), {
      agentId: item.agentId,
      inputId: item.id,
      clientId: deps.clientId(),
    }))
  }
  const deleteQueueItem = async (item: QueuedAgentInput) => {
    await runQueueRpc('Failed to delete queued input', () => workerRpc.deleteQueuedAgentInput(queueWorkerID(item), { agentId: item.agentId, inputId: item.id }))
  }
  const moveQueueItem = async (item: QueuedAgentInput, beforeInputId: string) => {
    await runQueueRpc('Failed to move queued input', () => workerRpc.moveQueuedAgentInput(queueWorkerID(item), { agentId: item.agentId, inputId: item.id, beforeInputId }))
  }
  const retryQueueItem = async (item: QueuedAgentInput, confirmUncertain: boolean) => {
    await runQueueRpc('Failed to retry queued input', () => workerRpc.retryQueuedAgentInput(queueWorkerID(item), {
      agentId: item.agentId,
      inputId: item.id,
      confirmDeliveryUncertain: confirmUncertain,
    }))
  }
  const steerQueueItem = async (item: QueuedAgentInput) => {
    await runQueueRpc('Failed to steer queued input', () => workerRpc.steerQueuedAgentInput(queueWorkerID(item), { agentId: item.agentId, inputId: item.id }))
  }
  const setQueuePaused = async (paused: boolean) => {
    // The shell mounts the composer for a FOCUSED agent alone, so an agent is
    // focused whenever this runs. Read that agent once, at call time: the
    // Worker lookup and the request must identify the same agent even when a
    // tab switch moves the focus while the RPC is in flight.
    const initialAgentID = deps.focusedAgentId()!
    const initialAgentTab = deps.view.getAgentTab(initialAgentID)
    await runQueueRpc('Failed to change queue pause state', () => workerRpc.setAgentInputQueuePaused(initialAgentTab?.workerId ?? '', { agentId: initialAgentID, paused }))
  }
  const enqueueComposerInput = async (kind: AgentInputKind, content: string, fileAttachments?: FileAttachment[]) => {
    // One read of the focused agent, at send time. The RPC below must reach
    // the agent that the user typed into, never the one that a tab switch
    // focused while the enqueue was in flight.
    const initialAgentID = deps.focusedAgentId()
    if (!initialAgentID)
      return
    // Jump to the live tail BEFORE the RPC. A reader who scrolled up must
    // land on the message that they just sent, and the live-append
    // auto-scroll never moves a reader who sits above the tail.
    deps.forceScrollToBottom()
    const sendAgent = deps.view.getAgentTab(initialAgentID)
    const attachments = fileAttachments ?? []
    const inputId = enqueueRetry.inputIdFor({ agentId: initialAgentID, kind, text: content, attachments })
    await runQueueRpc('Failed to queue message', () => workerRpc.enqueueAgentInput(sendAgent?.workerId ?? '', {
      agentId: initialAgentID,
      inputId,
      kind,
      text: content,
      attachments: attachments.map(attachment => ({
        filename: attachment.filename,
        mimeType: attachment.mimeType,
        data: attachment.data,
      })),
    }))
    enqueueRetry.markAccepted(inputId)
  }
  // One command for each kind that the composer sends. The kind is a wire
  // value of this request, so `AgentInputKind` stays inside this module and no
  // caller selects the enum member.
  const sendMessage = (content: string, fileAttachments?: FileAttachment[]) =>
    enqueueComposerInput(AgentInputKind.USER_MESSAGE, content, fileAttachments)
  const sendControlFeedback = (content: string) =>
    enqueueComposerInput(AgentInputKind.CONTROL_FEEDBACK, content)

  return {
    beginQueueEdit,
    updateQueueItem,
    cancelQueueEdit,
    deleteQueueItem,
    moveQueueItem,
    retryQueueItem,
    steerQueueItem,
    setQueuePaused,
    sendMessage,
    sendControlFeedback,
  }
}
