import type { Accessor } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { AgentInputQueueSnapshot, Attachment, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import { batch, createEffect, createSignal, untrack } from 'solid-js'
import { clearDraft, loadDraft } from '~/lib/editor/draftPersistence'
import { randomUUID } from '~/lib/idGenerator'
import { queueEditDraftKey } from './composer/draftIdentity'
import { clearScheduleDraft } from './composer/scheduleDrafts'

/**
 * What the begin-edit RPC answers: the queue snapshot it produced, the input's
 * attachments, and the input's full text. The queue row carries a shortened
 * preview, so the full text arrives here.
 */
export interface BeginQueueEditResult {
  // The two scalar members admit an explicit undefined: the proto response the
  // operations layer hands straight through carries its unset fields that way,
  // and the reads below already treat undefined as absence. `attachments` stays
  // a required array because protobuf-es always materializes repeated fields.
  snapshot?: AgentInputQueueSnapshot | undefined
  attachments: Attachment[]
  text?: string | undefined
}

/**
 * Claims the edit lock on one queued input of `agentId`. `takeover` takes the
 * lock away from another client that holds it.
 */
export type BeginQueueEdit = (agentId: string, item: QueuedAgentInput, takeover: boolean) => Promise<BeginQueueEditResult>

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

/**
 * One update of an open queue edit, as {@link QueueEditSession.markUpdateStarted}
 * answers it: the queued input, and the agent whose queue holds it. A queued
 * input states no agent, so the pair is what identifies the edit.
 *
 * The panel sends the update RPC with this pair and hands the same pair to
 * {@link QueueEditSession.markUpdateCompleted} or
 * {@link QueueEditSession.markUpdateFailed}. So the end of an update
 * identifies exactly the edit that the update started from, even after the
 * panel moved to another agent and started another update there.
 */
export interface QueueEditUpdate {
  agentId: string
  input: QueuedAgentInput
}

/**
 * The key of one agent's queued input. A queued input states no agent, and
 * the same input id can occur in two agents' queues, so only the pair
 * identifies one input.
 */
function queueInputKey(agentId: string, inputId: string): string {
  return `${agentId}\0${inputId}`
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
   * answers, which is what an adopted edit wants after a reload. It keeps the
   * edit's schedule draft for the same reason; without it, the edit starts
   * from the schedule that the item holds.
   *
   * A second call for an input whose request is still in flight does nothing,
   * so a repeated click cannot claim the lock twice.
   */
  loadQueueEdit: (item: QueuedAgentInput, takeover: boolean, restoreDraft: boolean) => void
  /** Drops everything the session holds for a queued input that left the queue. */
  clearQueueEditArtifacts: (item: QueuedAgentInput) => void
  /**
   * The update RPC for the open edit starts. Answers that edit and its agent,
   * which the panel sends and later hands to {@link markUpdateCompleted} or
   * {@link markUpdateFailed}, and marks an update of THAT edit in flight.
   *
   * Answers undefined, and marks nothing, when the agent that the panel shows
   * now has no open edit. The panel then sends no update.
   */
  markUpdateStarted: () => QueueEditUpdate | undefined
  /**
   * The update that {@link markUpdateStarted} answered failed. Ends that
   * update alone; an update of another edit stays in flight.
   */
  markUpdateFailed: (update: QueueEditUpdate) => void
  /**
   * The update that {@link markUpdateStarted} answered succeeded. It stays in
   * flight until {@link handleAfterSend} settles it, which closes that edit and
   * no other: the panel can show another agent's edit by then.
   */
  markUpdateCompleted: (update: QueueEditUpdate) => void
  /**
   * The editor finished a send and cleared its draft. Settles every update
   * that completed since the last call: closes its edit when the panel shows
   * it, and ends its in-flight state.
   */
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
 * normal attachments that the edit displaced, and the updates in flight.
 *
 * The panel completes the session with {@link QueueEditSession.bindAttachments},
 * which states why the wiring takes two calls.
 */
export function createQueueEditSession(opts: QueueEditSessionOptions): QueueEditSession {
  // The open edit, with the agent whose queue holds the input. A queued input
  // states no agent, so the session records the one it loaded the edit for.
  const [editingInput, setEditingInput] = createSignal<{ agentId: string, input: QueuedAgentInput }>()
  // How many updates of each edit are in flight, by `queueInputKey`. Keyed,
  // because action state is per agent: a panel that moves to another agent can
  // run two agents' updates at once, and one update's end says nothing about
  // the other. A count rather than a set, because two starts of the same edit
  // can overlap too, and the first one to end must not end the other.
  const [updatesInFlight, setUpdatesInFlight] = createSignal<ReadonlyMap<string, number>>(new Map())
  // The updates that succeeded and wait for `handleAfterSend`. Each one stays
  // in flight until then.
  let completedUpdates: QueueEditUpdate[] = []
  let pendingQueueEditText: string | undefined
  let pendingQueueEditAttachments: FileAttachment[] | undefined
  let normalAttachmentRestore: { key: string, attachments: FileAttachment[] } | undefined
  const queueEditRequests = new Set<string>()
  /**
   * Which queue-edit load the panel is waiting for.
   *
   * Bumped by every `loadQueueEdit`, and compared after each of its two awaits.
   * `queueEditRequests` dedupes the SAME row, so it cannot answer "the user
   * moved to a different row while this one was loading" -- and that is the case
   * that installs one row's text over another's.
   */
  let queueEditLoadToken = 0
  let attachmentPorts: QueueEditAttachmentPorts | undefined

  const requirePorts = (): QueueEditAttachmentPorts => {
    if (!attachmentPorts)
      throw new Error('The queue-edit session holds no attachment handles. Call bindAttachments after useChatAttachments.')
    return attachmentPorts
  }

  const activeEditingInput = () => {
    const editing = editingInput()
    return editing?.agentId === opts.agentId()
      ? editing.input
      : undefined
  }
  const attachmentDraftKey = () => {
    const editing = activeEditingInput()
    return editing
      ? queueEditDraftKey(opts.agentId(), editing.id)
      : opts.agentId()
  }

  /**
   * Adds one in-flight update for each of `updates`, or ends one. A count never
   * goes below zero: an end of an update that already ended changes nothing,
   * so it cannot cancel a later start of the same edit.
   *
   * ONE write for the whole list, so a settle of two updates of the same edit
   * does not depend on what a second write inside a `batch` reads.
   */
  const changeUpdatesInFlight = (updates: readonly QueueEditUpdate[], delta: 1 | -1) => {
    setUpdatesInFlight((previous) => {
      const next = new Map(previous)
      for (const update of updates) {
        const key = queueInputKey(update.agentId, update.input.id)
        const count = (next.get(key) ?? 0) + delta
        if (count > 0)
          next.set(key, count)
        else
          next.delete(key)
      }
      return next
    })
  }

  /** Whether an update of `agentId`'s input `inputId` is in flight. */
  const updateInFlight = (agentId: string, inputId: string): boolean =>
    updatesInFlight().has(queueInputKey(agentId, inputId))

  const loadQueueEdit = (item: QueuedAgentInput, takeover: boolean, restoreDraft: boolean) => {
    // Resolve the handles here, not in the response handler below. Every caller
    // discards this function's rejections, so a throw inside the chain vanishes.
    const ports = requirePorts()
    const editAgentId = untrack(opts.agentId)
    const inputId = item.id
    const requestKey = queueInputKey(editAgentId, inputId)
    if (queueEditRequests.has(requestKey))
      return
    const beginEdit = opts.onBeginQueueEdit()
    if (!beginEdit)
      return
    // THE SESSION, not the agent, is what the guards below compare.
    //
    // `queueEditRequests` dedupes per (agent, input), so two DIFFERENT queued
    // rows of the same agent can be in flight together -- and the queue keeps
    // every other row's Edit button live until one of them installs itself. An
    // agent check passes for both, so the slower load wins and the composer
    // opens on row A's text, attachments and attachment-restore point while the
    // user is pointing at row B. A per-load token answers the question the state
    // actually belongs to: "is this still the load the panel is waiting for?"
    const loadToken = ++queueEditLoadToken
    queueEditRequests.add(requestKey)
    const request = beginEdit(editAgentId, item, takeover)
    void request.then(async (response) => {
      if (loadToken !== queueEditLoadToken || untrack(opts.agentId) !== editAgentId)
        return
      const edited = response.snapshot?.items.find(candidate => candidate.id === inputId) ?? item
      const queueDraftKey = queueEditDraftKey(editAgentId, inputId)
      // The draft read is asynchronous (drafts are an unbounded family on the
      // unmirrored storage tier), so both guards run AGAIN below: this await is
      // a second window in which the panel can move to another agent or the user
      // can start editing another row, and everything after it writes this
      // session's state.
      const savedDraft = restoreDraft
        ? await loadDraft(queueDraftKey)
        : undefined
      if (loadToken !== queueEditLoadToken || untrack(opts.agentId) !== editAgentId)
        return
      pendingQueueEditText = savedDraft?.content
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
      normalAttachmentRestore = {
        key: editAgentId,
        attachments: [
          ...untrack(ports.attachments),
        ],
      }
      if (!restoreDraft)
        clearScheduleDraft(queueDraftKey)
      setEditingInput({ agentId: editAgentId, input: edited })
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
    const draftKey = queueEditDraftKey(opts.agentId(), item.id)
    clearDraft(draftKey)
    clearScheduleDraft(draftKey)
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
    // takes the lock. An update OF THIS EDIT in flight suspends this rule: that
    // update is the reason the worker rewrites the item, and the send path
    // closes the edit itself. An update of another edit does not suspend it,
    // because the worker's rewrite of that other item says nothing about this
    // one.
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
      if ((!current || current.editOwnerClientId !== opts.clientId()) && !updateInFlight(opts.agentId(), initialEditing.id)) {
        pendingQueueEditText = undefined
        pendingQueueEditAttachments = undefined
        ports.clearAllAttachments()
        setEditingInput()
        queueMicrotask(() => {
          const draftKey = queueEditDraftKey(opts.agentId(), initialEditing.id)
          clearDraft(draftKey)
          clearScheduleDraft(draftKey)
        })
      }
    })
  }

  const handleAfterSend = () => {
    if (completedUpdates.length === 0)
      return
    const settled = completedUpdates
    completedUpdates = []
    const current = activeEditingInput()
    const agentId = untrack(opts.agentId)
    // ONE update, so the ownership rule never sees an edit whose update ended
    // while the close of that edit is still pending.
    batch(() => {
      if (current && settled.some(update => update.agentId === agentId && update.input.id === current.id))
        setEditingInput()
      changeUpdatesInFlight(settled, -1)
    })
  }

  const markUpdateStarted = (): QueueEditUpdate | undefined => {
    // Untracked, so a caller inside a reactive scope does not subscribe to
    // the edit that this update is about to close.
    const agentId = untrack(opts.agentId)
    const input = untrack(activeEditingInput)
    if (!input)
      return undefined
    const update = { agentId, input }
    changeUpdatesInFlight([update], 1)
    return update
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
    markUpdateStarted,
    markUpdateFailed: update => changeUpdatesInFlight([update], -1),
    markUpdateCompleted: (update) => {
      completedUpdates.push(update)
    },
    handleAfterSend,
    takePendingText,
    takePendingTextForDraftKey,
  }
}
