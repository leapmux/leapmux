import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentInputQueueSnapshot, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import type { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import type { TabView } from '~/stores/tabView'
import { Code, ConnectError } from '@connectrpc/connect'
import { workerAgentInput } from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { ChannelError } from '~/lib/channelError'
import { createAgentInputEnqueueRetry } from './agentInputEnqueueRetry'

const ENQUEUE_REFUSAL_CODES = new Set([
  Code.InvalidArgument,
  Code.NotFound,
  Code.AlreadyExists,
  Code.PermissionDenied,
  Code.ResourceExhausted,
  Code.FailedPrecondition,
  Code.OutOfRange,
  Code.Unauthenticated,
])

// These responses refuse admission. Internal and transport failures can leave
// the commit uncertain, so they retain the original input ID.
function isDefinitiveEnqueueRefusal(error: unknown): boolean {
  return ((error instanceof ChannelError && error.source === 'rpc') || error instanceof ConnectError)
    && ENQUEUE_REFUSAL_CODES.has(error.code)
}

function queueAttachmentsOf(attachments: readonly FileAttachment[]) {
  return attachments.map(attachment => ({
    filename: attachment.filename,
    mimeType: attachment.mimeType,
    data: attachment.data,
  }))
}

/**
 * The composer's queue commands, built once for the shell.
 *
 * `AgentEditorPanel` takes one callback for each command. Every callback sends
 * a Worker RPC, applies the snapshot that the response carries, and shows one
 * warning when the call fails. That work is a request layer, not a layout
 * decision, so it lives here and `TileRenderer` sends no Worker RPC of its own.
 *
 * The bag holds no per-mount state, so the shell builds it once beside the
 * other renderer-level helpers. The composer unmounts whenever a non-agent tab
 * has focus, and `enqueueRetry` must outlive that unmount.
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
  // One input ID represents one delivery attempt, including across editor
  // remounts. Share its in-flight request so outcomes cannot disagree.
  const inFlightByInputId = new Map<string, Promise<boolean>>()
  /**
   * Run one queue RPC for `agentId` and apply the snapshot that it answers
   * with.
   *
   * Every queue command takes the agent beside the item: a queued item states
   * no agent, because the snapshot that holds it belongs to one. This function
   * resolves the Worker of that agent and puts the same agent into the
   * request, so the two cannot identify different agents. The Worker is read
   * at call time, before the RPC can yield to a tab switch.
   *
   * Every response of this family carries the whole queue, so the apply
   * belongs HERE rather than at each call site: a handler that forgets it
   * leaves the panel on a stale queue until the next broadcast arrives. A
   * failure shows one warning and rethrows, so the caller still sees it.
   */
  // The generated response types declare `snapshot?: ... | undefined` (proto
  // field presence), so the constraint must admit an explicit undefined too.
  const runQueueRpc = async <R extends { agentId?: string }, T extends { snapshot?: AgentInputQueueSnapshot | undefined }>(
    agentId: string,
    label: string,
    rpc: (workerId: string, req: R) => Promise<T>,
    req: Omit<R, 'agentId'>,
  ): Promise<T> => {
    const workerId = deps.view.getAgentTab(agentId)?.workerId ?? ''
    let response: T
    try {
      response = await rpc(workerId, { ...req, agentId } as R)
    }
    catch (error) {
      showWarnToast(label, error)
      throw error
    }
    deps.store.apply(agentId, response.snapshot)
    return response
  }
  // The one handler whose caller needs the response: the composer loads the
  // full text and the attachments of the input that it starts to edit. The
  // response object is handed through UNCHANGED -- the composer's edit session
  // keys off it, so no member is rebuilt or dropped here.
  const beginQueueEdit = (agentId: string, item: QueuedAgentInput, takeover: boolean) =>
    runQueueRpc(agentId, 'Failed to edit queued input', workerAgentInput.beginQueuedAgentInputEdit, {
      inputId: item.id,
      clientId: deps.clientId(),
      takeover,
    })
  /**
   * Save an open edit. `scheduledAt` is undefined to keep the stored schedule,
   * empty to clear it, or the new wire schedule. The parameter is required, so
   * a caller states which one it means.
   */
  const updateQueueItem = async (agentId: string, item: QueuedAgentInput, text: string, fileAttachments: FileAttachment[], scheduledAt: string | undefined) => {
    await runQueueRpc(agentId, 'Failed to save queued input', workerAgentInput.updateQueuedAgentInput, {
      inputId: item.id,
      clientId: deps.clientId(),
      expectedVersion: item.version,
      text,
      attachments: queueAttachmentsOf(fileAttachments),
      // An omitted `scheduled_at` keeps the stored schedule, and an empty one
      // clears it.
      ...(scheduledAt !== undefined
        ? { scheduledAt }
        : {}),
    })
  }
  const cancelQueueEdit = async (agentId: string, item: QueuedAgentInput) => {
    await runQueueRpc(agentId, 'Failed to cancel queued input edit', workerAgentInput.cancelQueuedAgentInputEdit, {
      inputId: item.id,
      clientId: deps.clientId(),
    })
  }
  const deleteQueueItem = async (agentId: string, item: QueuedAgentInput) => {
    await runQueueRpc(agentId, 'Failed to delete queued input', workerAgentInput.deleteQueuedAgentInput, { inputId: item.id })
  }
  const moveQueueItem = async (agentId: string, item: QueuedAgentInput, beforeInputId: string) => {
    await runQueueRpc(agentId, 'Failed to move queued input', workerAgentInput.moveQueuedAgentInput, { inputId: item.id, beforeInputId })
  }
  const retryQueueItem = async (agentId: string, item: QueuedAgentInput, confirmUncertain: boolean) => {
    await runQueueRpc(agentId, 'Failed to retry queued input', workerAgentInput.retryQueuedAgentInput, { inputId: item.id, confirmDeliveryUncertain: confirmUncertain })
  }
  const steerQueueItem = async (agentId: string, item: QueuedAgentInput) => {
    await runQueueRpc(agentId, 'Failed to steer queued input', workerAgentInput.steerQueuedAgentInput, { inputId: item.id })
  }
  // Preempt cancels the running turn; the item itself is dispatched by the
  // ordinary turn-end drain, so the response still shows it queued.
  const preemptQueueItem = async (agentId: string, item: QueuedAgentInput) => {
    await runQueueRpc(agentId, 'Failed to preempt queued input', workerAgentInput.preemptQueuedAgentInput, { inputId: item.id })
  }
  const setQueuePaused = async (paused: boolean) => {
    // The shell mounts the composer for a FOCUSED agent alone, so an agent is
    // focused whenever this runs. Read that agent once, at call time:
    // `runQueueRpc` then resolves the Worker of that same agent, even when a
    // tab switch moves the focus while the RPC is in flight.
    await runQueueRpc(deps.focusedAgentId()!, 'Failed to change queue pause state', workerAgentInput.setAgentInputQueuePaused, { paused })
  }
  const enqueueComposerInput = async (kind: AgentInputKind, content: string, fileAttachments: FileAttachment[] = [], scheduledAt = '', requirePendingAttempt = false): Promise<boolean> => {
    // Capture the destination before the request can yield to a tab switch.
    const initialAgentID = deps.focusedAgentId()
    if (!initialAgentID)
      return false
    const payload = {
      agentId: initialAgentID,
      kind,
      text: content,
      attachments: fileAttachments,
      scheduledAt,
    }
    const inputId = requirePendingAttempt
      ? await enqueueRetry.inputIdForPendingAttempt(payload)
      : await enqueueRetry.inputIdFor(payload)
    if (inputId === undefined)
      return false
    const existing = inFlightByInputId.get(inputId)
    if (existing)
      return existing
    // Scroll before the RPC so a reader above the tail sees the sent message.
    deps.forceScrollToBottom()
    const request = (async () => {
      try {
        await runQueueRpc(initialAgentID, 'Failed to queue message', workerAgentInput.enqueueAgentInput, {
          inputId,
          kind,
          text: content,
          attachments: queueAttachmentsOf(fileAttachments),
          scheduledAt,
        })
        enqueueRetry.markAccepted(inputId)
        return true
      }
      catch (error) {
        if (isDefinitiveEnqueueRefusal(error))
          enqueueRetry.markRefused(inputId)
        else
          enqueueRetry.markUncertain(inputId)
        throw error
      }
      finally {
        inFlightByInputId.delete(inputId)
      }
    })()
    inFlightByInputId.set(inputId, request)
    return request
  }
  // Each command fixes its request kind, so callers cannot select another one.
  const sendMessage = async (content: string, fileAttachments?: FileAttachment[], scheduledAt = '') => {
    await enqueueComposerInput(AgentInputKind.USER_MESSAGE, content, fileAttachments, scheduledAt)
  }
  /** Retry only an exact unanswered payload, preserving its input ID and schedule. */
  const retryPendingMessage = (content: string, fileAttachments?: FileAttachment[], scheduledAt = '') =>
    enqueueComposerInput(AgentInputKind.USER_MESSAGE, content, fileAttachments, scheduledAt, true)
  const sendControlFeedback = async (content: string) => {
    await enqueueComposerInput(AgentInputKind.CONTROL_FEEDBACK, content)
  }

  return {
    beginQueueEdit,
    updateQueueItem,
    cancelQueueEdit,
    deleteQueueItem,
    moveQueueItem,
    retryQueueItem,
    steerQueueItem,
    preemptQueueItem,
    setQueuePaused,
    sendMessage,
    retryPendingMessage,
    sendControlFeedback,
  }
}
