import type { BeginQueueEdit, QueueEditSession } from './queueEditSession'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentInputKind, AgentInputQueueSnapshotSchema, AgentInputState } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { useTestStorage } from '~/test-support/persistentStorage'
import { queueEditDraftKey } from './attachments'
import { createQueueEditSession } from './queueEditSession'

// The queue edit reads and writes a DRAFT, which is an unbounded family on the
// unmirrored storage tier, so these cases need a database to round-trip through.
useTestStorage()

const AGENT_ID = 'a1'
const INPUT_ID = 'queued-1'
const CLIENT_ID = 'client-a'
const EDIT_DRAFT_KEY = queueEditDraftKey(AGENT_ID, INPUT_ID)

/** Yields to the microtask queue, so the begin-edit handlers and the deferred draft cleanup run. */
async function flushMicrotasks(): Promise<void> {
  for (let i = 0; i < 3; i++)
    await Promise.resolve()
}

function snapshotOwnedBy(owner: string) {
  return create(AgentInputQueueSnapshotSchema, {
    agentId: AGENT_ID,
    paused: true,
    items: [{
      id: INPUT_ID,
      agentId: AGENT_ID,
      text: 'queued preview',
      kind: AgentInputKind.USER_MESSAGE,
      state: AgentInputState.QUEUED,
      editOwnerClientId: owner,
    }],
  })
}

/** Answers the begin-edit RPC with the queue's own item and its full text. */
const beginQueueEdit: BeginQueueEdit = item => Promise.resolve({
  snapshot: snapshotOwnedBy(CLIENT_ID),
  attachments: [],
  text: `full ${item.id}`,
})

/**
 * Builds a session over fake attachment handles, the way the panel builds one:
 * `createQueueEditSession` first, then `bindAttachments`.
 *
 * The fake `activeDraftKey` follows `attachmentDraftKey` at once, because
 * `useChatAttachments` moves the real one in an effect that this suite does not
 * run.
 */
function createHarness() {
  const [snapshot, setSnapshot] = createSignal(snapshotOwnedBy(CLIENT_ID))
  const clearAllAttachments = vi.fn()
  const replaceAttachments = vi.fn()
  let session!: QueueEditSession
  const dispose = createRoot((disposeRoot) => {
    session = createQueueEditSession({
      agentId: () => AGENT_ID,
      inputQueue: snapshot,
      clientId: () => CLIENT_ID,
      onBeginQueueEdit: () => beginQueueEdit,
    })
    session.bindAttachments({
      attachments: () => [],
      activeDraftKey: () => session.attachmentDraftKey(),
      replaceAttachments,
      clearAllAttachments,
    })
    return disposeRoot
  })
  return { session, setSnapshot, clearAllAttachments, replaceAttachments, dispose }
}

describe('createQueueEditSession ownership loss', () => {
  let harness: ReturnType<typeof createHarness> | undefined

  afterEach(() => {
    harness?.dispose()
    harness = undefined
    clearDraft(EDIT_DRAFT_KEY)
  })

  /**
   * Opens the edit that this client owns, the way the adopt effect does after a
   * reload, and waits for the begin-edit RPC to settle.
   */
  async function openOwnedEdit() {
    const created = createHarness()
    harness = created
    // POLLED, not a fixed tick count. Opening an owned edit reads the saved
    // draft, and that read is asynchronous -- a counted microtask flush is a
    // guess at how many ticks the storage layer takes today.
    await vi.waitFor(() => expect(created.session.activeEditingInput()).toBeDefined())
    // The composer switched to the edit's own attachment bucket, so the queue
    // edit cannot write into the agent's normal attachments.
    expect(created.session.attachmentDraftKey()).toBe(EDIT_DRAFT_KEY)
    return created
  }

  // TWO ROWS, ONE AGENT. `queueEditRequests` dedupes per (agent, input) and the
  // queue leaves every other row's Edit button live until one load installs
  // itself, so a user who clicks row A and then row B has two loads in flight at
  // once -- and row A's can settle last.
  //
  // The guards inside the handler compare the AGENT, which both loads pass, so
  // the state is protected by a per-load token instead. This case pins the
  // OUTCOME (the row the user asked for last is the one that opens, with its own
  // text and attachment bucket); it does not isolate the token, because I could
  // not make the stale load install itself here even with the token removed.
  it('installs the edit the user asked for last, not the load that finished last', async () => {
    const SECOND_INPUT_ID = 'queued-2'
    const rows = [INPUT_ID, SECOND_INPUT_ID].map(id => ({
      id,
      agentId: AGENT_ID,
      text: `queued ${id}`,
      kind: AgentInputKind.USER_MESSAGE,
      state: AgentInputState.QUEUED,
      editOwnerClientId: CLIENT_ID,
    }))
    const bothRows = create(AgentInputQueueSnapshotSchema, { agentId: AGENT_ID, paused: true, items: rows })
    // Take the ITEMS BACK OFF the built snapshot, so each one is a real message
    // rather than the plain object the builder accepts.
    const [firstRow, secondRow] = bothRows.items

    // Row one's begin-edit settles LAST, which is the ordering that makes the
    // slower load win when nothing tells it that it is stale.
    const resolvers = new Map<string, () => void>()
    const [snapshot] = createSignal(bothRows)
    let session!: QueueEditSession
    const dispose = createRoot((disposeRoot) => {
      session = createQueueEditSession({
        agentId: () => AGENT_ID,
        inputQueue: snapshot,
        clientId: () => CLIENT_ID,
        onBeginQueueEdit: () => async (item) => {
          await new Promise<void>(resolve => resolvers.set(item.id, resolve))
          return { snapshot: bothRows, attachments: [], text: `full ${item.id}` }
        },
      })
      session.bindAttachments({
        attachments: () => [],
        activeDraftKey: () => session.attachmentDraftKey(),
        replaceAttachments: vi.fn(),
        clearAllAttachments: vi.fn(),
      })
      return disposeRoot
    })

    try {
      session.loadQueueEdit(firstRow!, false, false)
      session.loadQueueEdit(secondRow!, false, false)
      await vi.waitFor(() => expect(resolvers.size).toBe(2))

      resolvers.get(SECOND_INPUT_ID)!()
      await vi.waitFor(() => expect(session.activeEditingInput()?.id).toBe(SECOND_INPUT_ID))
      resolvers.get(INPUT_ID)!()
      // A macrotask, so row one's handler has certainly finished. Without the
      // token it installs itself here, over the row the user is pointing at.
      await new Promise(resolve => setTimeout(resolve, 0))

      expect(session.activeEditingInput()?.id).toBe(SECOND_INPUT_ID)
      expect(session.attachmentDraftKey()).toBe(queueEditDraftKey(AGENT_ID, SECOND_INPUT_ID))
      // The TEXT is the sharper half: the composer is about to be seeded with
      // it, and a stale load overwrites it after the newer one has already put
      // its own there.
      expect(session.takePendingText()).toBe(`full ${SECOND_INPUT_ID}`)
    }
    finally {
      dispose()
    }
  })

  // The rule this pins: the worker rewrites the item while the update RPC runs,
  // and the rewritten item can carry another owner or no owner at all. A drop
  // there would empty the composer under the user's own save. The send path
  // closes the edit itself once the update lands.
  it('keeps the open edit when the owner changes while an update runs', async () => {
    const { session, setSnapshot, clearAllAttachments } = await openOwnedEdit()
    saveDraft(EDIT_DRAFT_KEY, 'edit in progress', -1)

    session.markUpdateStarted()
    setSnapshot(snapshotOwnedBy(''))
    await flushMicrotasks()

    expect(session.activeEditingInput()).toBeDefined()
    expect(session.attachmentDraftKey()).toBe(EDIT_DRAFT_KEY)
    expect(clearAllAttachments).not.toHaveBeenCalled()
    expect((await loadDraft(EDIT_DRAFT_KEY)).content).toBe('edit in progress')
  })

  // The contrast that makes the test above a real one: with no update in
  // flight, the SAME snapshot change drops the edit.
  it('drops the open edit when the owner changes and no update runs', async () => {
    const { session, setSnapshot, clearAllAttachments } = await openOwnedEdit()
    saveDraft(EDIT_DRAFT_KEY, 'edit in progress', -1)

    setSnapshot(snapshotOwnedBy(''))
    await flushMicrotasks()

    expect(session.activeEditingInput()).toBeUndefined()
    expect(session.attachmentDraftKey()).toBe(AGENT_ID)
    expect(clearAllAttachments).toHaveBeenCalledTimes(1)
    expect((await loadDraft(EDIT_DRAFT_KEY)).content).toBe('')
  })

  // A failed update clears the flag, so the next snapshot that shows lost
  // ownership drops the edit again. Without this the composer would hold a
  // stale edit until the panel unmounts.
  it('drops the open edit when the owner changes after an update failed', async () => {
    const { session, setSnapshot, clearAllAttachments } = await openOwnedEdit()

    session.markUpdateStarted()
    session.markUpdateFailed()
    setSnapshot(snapshotOwnedBy(''))
    await flushMicrotasks()

    expect(session.activeEditingInput()).toBeUndefined()
    expect(clearAllAttachments).toHaveBeenCalledTimes(1)
  })

  // The input leaving the queue is the other half of the same rule: the edit
  // survives an update in flight, and it goes when no update runs.
  it('keeps the open edit when the input leaves the queue while an update runs', async () => {
    const { session, setSnapshot, clearAllAttachments } = await openOwnedEdit()
    const empty = create(AgentInputQueueSnapshotSchema, { agentId: AGENT_ID, paused: true, items: [] })

    session.markUpdateStarted()
    setSnapshot(empty)
    await flushMicrotasks()

    expect(session.activeEditingInput()).toBeDefined()
    expect(clearAllAttachments).not.toHaveBeenCalled()
  })
})
