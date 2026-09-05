import type { Accessor } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { AgentInputQueueSnapshot, Attachment, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { createEffect, createSignal, untrack } from 'solid-js'
import { clearDraft, loadDraft } from '~/lib/editor/draftPersistence'
import { randomUUID } from '~/lib/idGenerator'
import { queueEditDraftKey } from './attachments'

/**
 * What the begin-edit RPC answers: the queue snapshot it produced, the input's
 * attachments, and the input's full text. The queue row carries a shortened
 * preview, so the full text arrives here.
 */
export interface BeginQueueEditResult {
  snapshot?: AgentInputQueueSnapshot
  attachments: Attachment[]
  text?: string
}

/**
 * Claims the edit lock on one queued input. `takeover` takes the lock away from
 * another client that holds it.
 */
export type BeginQueueEdit = (item: QueuedAgentInput, takeover: boolean) => Promise<BeginQueueEditResult>

/**
 * The attachment handles that the session drives. Every one of them is an
 * OUTPUT of `useChatAttachments`, while {@link QueueEditSession.attachmentDraftKey}
 * is an input of that same hook. {@link QueueEditSession.bindAttachments} states
 * how the panel resolves that cycle.
 */
export interface QueueEditAttachmentPorts {
  /** The attachments that the composer shows now. */
  attachments: Accessor<FileAttachment[]>
  /**
   * The bucket that the hook loaded last. It trails `attachmentDraftKey` by one
   * effect, so the session waits for it before it swaps the attachments.
   */
  activeDraftKey: Accessor<string>
  replaceAttachments: (attachments: FileAttachment[]) => void
  clearAllAttachments: () => void
}

export interface QueueEditSessionOptions {
  /** The agent that the panel shows now. */
  agentId: Accessor<string>
  /** The agent's input queue, as the worker last reported it. */
  inputQueue: Accessor<AgentInputQueueSnapshot | undefined>
  /** This browser client's queue identity, or undefined before the panel has one. */
  clientId: Accessor<string | undefined>
  /** The begin-edit RPC, or undefined when the panel offers no edit. */
  onBeginQueueEdit: Accessor<BeginQueueEdit | undefined>
}

export interface QueueEditSession {
  /**
   * The queued input that this panel edits for the CURRENT agent, or undefined.
   * A snapshot can move the panel to another agent while an edit is open, and
   * the other agent's edit must not appear in this agent's composer.
   */
  activeEditingInput: Accessor<QueuedAgentInput | undefined>
  /**
   * The attachment bucket key: the queue-edit key while an edit is open, and
   * the agent id otherwise. The panel passes this to `useChatAttachments`, so a
   * queue edit gets its own attachments and gives the normal ones back.
   */
  attachmentDraftKey: Accessor<string>
  /**
   * Supplies the attachment handles and starts the three effects that need
   * them: the effect that adopts an edit this client owns, the effect that
   * swaps and restores the attachments, and the effect that drops an edit whose
   * ownership this client lost.
   *
   * ORDERING RULE. The panel creates the session BEFORE `useChatAttachments`,
   * because {@link QueueEditSession.attachmentDraftKey} is an input of that
   * hook. The panel calls this method immediately AFTER `useChatAttachments`,
   * because every handle in {@link QueueEditAttachmentPorts} is an output of
   * the same hook. One call cannot sit on both sides of that cycle. Each
   * session method that reads a handle runs from an event handler or from an
   * effect, so the panel always fills the handles first; a method that runs
   * before this call fails with an explicit error.
   */
  bindAttachments: (ports: QueueEditAttachmentPorts) => void
  /**
   * Claims the edit lock on `item` and loads its text and attachments into the
   * composer. `takeover` takes the lock from another client. `restoreDraft`
   * keeps a saved draft for this input instead of the text that the RPC
   * answers, which is what an adopted edit wants after a reload.
   *
   * A second call for an input whose request is still in flight does nothing,
   * so a repeated click cannot claim the lock twice.
   */
  loadQueueEdit: (item: QueuedAgentInput, takeover: boolean, restoreDraft: boolean) => void
  /** Drops everything the session holds for a queued input that left the queue. */
  clearQueueEditArtifacts: (item: QueuedAgentInput) => void
  /** The update RPC for the open edit starts. */
  markUpdateStarted: () => void
  /** The update RPC for the open edit failed. */
  markUpdateFailed: () => void
  /** The update RPC for `item` succeeded. {@link handleAfterSend} closes the edit. */
  markUpdateCompleted: (item: QueuedAgentInput) => void
  /** The editor finished the send that carried the update, and cleared its draft. */
  handleAfterSend: () => void
  /**
   * Answers the text that {@link loadQueueEdit} fetched, and forgets it. The
   * panel writes that text into the editor. Answers undefined when no edit is
   * open, or when the editor already took the text.
   */
  takePendingText: () => string | undefined
  /**
   * The same as {@link takePendingText}, for the editor's draft-key change. The
   * editor reports every key it opens, and it reports null for no draft, so
   * this refuses a key that does not belong to the open edit.
   */
  takePendingTextForDraftKey: (key: string | null) => string | undefined
}

/**
 * Owns one agent composer's queue-edit state: which queued input the composer
 * edits, the text and the attachments that the begin-edit RPC fetched, the
 * normal attachments that the edit displaced, and the update-in-flight flag.
 *
 * The panel completes the session with {@link QueueEditSession.bindAttachments},
 * which states why the wiring takes two calls.
 */
export function createQueueEditSession(opts: QueueEditSessionOptions): QueueEditSession {
  const [editingInput, setEditingInput] = createSignal<QueuedAgentInput>()
  const [queueUpdateInFlight, setQueueUpdateInFlight] = createSignal(false)
  let completedQueueEdit: { agentId: string, inputId: string } | undefined
  let pendingQueueEditText: string | undefined
  let pendingQueueEditAttachments: FileAttachment[] | undefined
  let normalAttachmentRestore: { key: string, attachments: FileAttachment[] } | undefined
  const queueEditRequests = new Set<string>()
  let attachmentPorts: QueueEditAttachmentPorts | undefined

  const requirePorts = (): QueueEditAttachmentPorts => {
    if (!attachmentPorts)
      throw new Error('The queue-edit session holds no attachment handles. Call bindAttachments after useChatAttachments.')
    return attachmentPorts
  }

  const activeEditingInput = () => {
    const editing = editingInput()
    return editing?.agentId === opts.agentId() ? editing : undefined
  }
  const attachmentDraftKey = () => {
    const editing = activeEditingInput()
    return editing ? queueEditDraftKey(opts.agentId(), editing.id) : opts.agentId()
  }

  const loadQueueEdit = (item: QueuedAgentInput, takeover: boolean, restoreDraft: boolean) => {
    // Resolve the handles here, not in the response handler below. Every caller
    // discards this function's rejections, so a throw inside the chain vanishes.
    const ports = requirePorts()
    const editAgentId = untrack(opts.agentId)
    const inputId = item.id
    const requestKey = `${editAgentId}\0${inputId}`
    if (queueEditRequests.has(requestKey))
      return
    const beginEdit = opts.onBeginQueueEdit()
    if (!beginEdit)
      return
    queueEditRequests.add(requestKey)
    const request = beginEdit(item, takeover)
    void request.then((response) => {
      if (untrack(opts.agentId) !== editAgentId)
        return
      const edited = response.snapshot?.items.find(candidate => candidate.id === inputId) ?? item
      const queueDraftKey = queueEditDraftKey(editAgentId, inputId)
      pendingQueueEditText = restoreDraft && loadDraft(queueDraftKey).content
        ? undefined
        : (response.text ?? edited.text)
      pendingQueueEditAttachments = response.attachments.map(attachment => ({
        id: randomUUID(),
        file: new File([new Uint8Array(attachment.data).buffer], attachment.filename, { type: attachment.mimeType }),
        filename: attachment.filename,
        mimeType: attachment.mimeType,
        data: attachment.data,
        size: attachment.data.byteLength,
      }))
      normalAttachmentRestore = { key: editAgentId, attachments: [...untrack(ports.attachments)] }
      setEditingInput(edited)
    }).catch(() => {}).finally(() => {
      queueEditRequests.delete(requestKey)
    })
  }

  /**
   * Drop everything the session holds for a queued input that left the queue: a
   * delete removes it, and a cancel gives the edit lock back.
   *
   * Both callers run this AFTER their RPC settles, and it reads the agent id
   * and the editing input at that moment on purpose. A newer snapshot can move
   * the panel to another agent while the RPC is in flight, and the panel must
   * then keep the new agent's editor.
   */
  const clearQueueEditArtifacts = (item: QueuedAgentInput) => {
    const ports = requirePorts()
    clearDraft(queueEditDraftKey(opts.agentId(), item.id))
    if (untrack(activeEditingInput)?.id === item.id) {
      ports.clearAllAttachments()
      setEditingInput()
    }
  }

  const bindAttachments = (ports: QueueEditAttachmentPorts) => {
    // The effects below capture `ports`. After a second call those effects
    // still drive the first set of handles, while `loadQueueEdit` and
    // `clearQueueEditArtifacts` drive the second set.
    if (attachmentPorts)
      throw new Error('The queue-edit session already holds attachment handles. Call bindAttachments once.')
    attachmentPorts = ports

    // Adopt an edit that this browser client already owns. A reload, or a move
    // to another agent and back, leaves the lock on the worker.
    createEffect(() => {
      if (activeEditingInput() || !opts.clientId())
        return
      const owned = opts.inputQueue()?.items.find(item => item.editOwnerClientId === opts.clientId())
      if (owned)
        loadQueueEdit(owned, false, true)
    })

    // Swap the attachments in, and give the normal ones back. Both branches
    // wait for the hook to load the new bucket, because a write before that
    // load goes into the previous bucket.
    createEffect(() => {
      const editing = activeEditingInput()
      const activeAttachmentKey = ports.activeDraftKey()
      if (editing && activeAttachmentKey === queueEditDraftKey(opts.agentId(), editing.id) && pendingQueueEditAttachments !== undefined) {
        ports.replaceAttachments(pendingQueueEditAttachments)
        pendingQueueEditAttachments = undefined
        return
      }
      if (!editing && normalAttachmentRestore?.key === activeAttachmentKey) {
        ports.replaceAttachments(normalAttachmentRestore.attachments)
        normalAttachmentRestore = undefined
      }
    })

    // Drop the edit when the input leaves the queue, or when another client
    // takes the lock. An update in flight suspends this rule: that update is
    // the reason the worker rewrites the item, and the send path closes the
    // edit itself.
    createEffect(() => {
      const initialEditing = activeEditingInput()
      const snapshot = opts.inputQueue()
      if (!initialEditing || !snapshot)
        return
      let current: QueuedAgentInput | undefined
      for (const item of snapshot.items) {
        if (item.id === initialEditing.id) {
          current = item
          break
        }
      }
      if ((!current || current.editOwnerClientId !== opts.clientId()) && !queueUpdateInFlight()) {
        pendingQueueEditText = undefined
        pendingQueueEditAttachments = undefined
        ports.clearAllAttachments()
        setEditingInput()
        queueMicrotask(() => clearDraft(queueEditDraftKey(opts.agentId(), initialEditing.id)))
      }
    })
  }

  const handleAfterSend = () => {
    if (completedQueueEdit) {
      const completed = completedQueueEdit
      completedQueueEdit = undefined
      const current = activeEditingInput()
      if (current?.agentId === completed.agentId && current.id === completed.inputId)
        setEditingInput()
      setQueueUpdateInFlight(false)
    }
  }

  const takePendingText = (): string | undefined => {
    if (!activeEditingInput() || pendingQueueEditText === undefined)
      return undefined
    const text = pendingQueueEditText
    pendingQueueEditText = undefined
    return text
  }

  const takePendingTextForDraftKey = (key: string | null): string | undefined => {
    const editing = activeEditingInput()
    if (!editing || key !== queueEditDraftKey(opts.agentId(), editing.id))
      return undefined
    return takePendingText()
  }

  return {
    activeEditingInput,
    attachmentDraftKey,
    bindAttachments,
    loadQueueEdit,
    clearQueueEditArtifacts,
    markUpdateStarted: () => setQueueUpdateInFlight(true),
    markUpdateFailed: () => setQueueUpdateInFlight(false),
    markUpdateCompleted: (item) => {
      completedQueueEdit = { agentId: item.agentId, inputId: item.id }
    },
    handleAfterSend,
    takePendingText,
    takePendingTextForDraftKey,
  }
}
