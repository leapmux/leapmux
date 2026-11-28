import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentTab } from '~/stores/tab.types'
import type { TabView } from '~/stores/tabView'
import { create } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  AgentChatMessageSchema,
  AgentInputKind,
  AgentInputQueueSnapshotSchema,
  MessageSource,
  QueuedAgentInputSchema,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { ChannelError } from '~/lib/channelError'
import { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import { createChatStore } from '~/stores/chat.store'
import { deferred } from '~/test-support/async'
import { holdWorkerReplies, settleWorkerReplies } from '~/test-support/fakeWorker'
import { createAgentInputQueueOperations } from './agentInputQueueOperations'

/**
 * The whole RPC module, replaced. This unit sends no call that these tests do
 * not drive, and the real `callWorker` needs a live channel.
 */
const enqueueAgentInput = vi.hoisted(() => vi.fn())
const deleteQueuedAgentInput = vi.hoisted(() => vi.fn())
const beginQueuedAgentInputEdit = vi.hoisted(() => vi.fn())
const updateQueuedAgentInput = vi.hoisted(() => vi.fn())
const setAgentInputQueuePaused = vi.hoisted(() => vi.fn())
const cancelQueuedAgentInputEdit = vi.hoisted(() => vi.fn())
const moveQueuedAgentInput = vi.hoisted(() => vi.fn())
const retryQueuedAgentInput = vi.hoisted(() => vi.fn())
const steerQueuedAgentInput = vi.hoisted(() => vi.fn())
const preemptQueuedAgentInput = vi.hoisted(() => vi.fn())
const advanceAgentInputQueue = vi.hoisted(() => vi.fn())
vi.mock('~/api/workerRpc', () => ({
  workerAgentInput: {
    enqueueAgentInput,
    deleteQueuedAgentInput,
    beginQueuedAgentInputEdit,
    updateQueuedAgentInput,
    setAgentInputQueuePaused,
    cancelQueuedAgentInputEdit,
    moveQueuedAgentInput,
    retryQueuedAgentInput,
    steerQueuedAgentInput,
    preemptQueuedAgentInput,
    advanceAgentInputQueue,
  },
}))

const showWarnToast = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showWarnToast }))

/** A tab join that answers for the agents in `workerByAgent` and nothing else. */
function stubView(workerByAgent: Record<string, string>): TabView {
  return {
    getAgentTab: (id: string): AgentTab | undefined => {
      const workerId = workerByAgent[id]
      return workerId === undefined
        ? undefined
        : ({ id, workerId } as AgentTab)
    },
  } as unknown as TabView
}

function queuedItem(id: string) {
  return create(QueuedAgentInputSchema, { id, version: 7n })
}

function snapshotOf(revision: bigint, itemIds: string[]) {
  return create(AgentInputQueueSnapshotSchema, {
    revision,
    items: itemIds.map(id => create(QueuedAgentInputSchema, { id })),
  })
}

function transcriptMessage(id: string, seq = 1n, source: MessageSource = MessageSource.USER) {
  return create(AgentChatMessageSchema, { id, seq, source })
}

function attachment(filename: string): FileAttachment {
  const data = new Uint8Array([1, 2, 3])
  return {
    id: filename,
    file: new File([data], filename, { type: 'text/plain' }),
    filename,
    mimeType: 'text/plain',
    data,
    size: data.byteLength,
  }
}

interface SetupOptions {
  /** Agent ids mapped to the Worker that hosts them. */
  workerByAgent?: Record<string, string>
  focusedAgentId?: string | null
}

function setup(options: SetupOptions = {}) {
  const store = createAgentInputQueueStore()
  const chatStore = createChatStore()
  const forceScrollToBottom = vi.fn()
  const deps = {
    view: stubView(options.workerByAgent ?? { 'agent-1': 'worker-1' }),
    store,
    chatStore,
    clientId: () => 'client-1',
    focusedAgentId: () => (options.focusedAgentId === undefined
      ? 'agent-1'
      : options.focusedAgentId),
    forceScrollToBottom,
  }
  const ops = createAgentInputQueueOperations(deps)
  return {
    ops,
    store,
    chatStore,
    forceScrollToBottom,
  }
}

/** The `setup` harness over two agents, with a focus that a test can move. */
function setupWithMovableFocus() {
  let focused = 'agent-1'
  const store = createAgentInputQueueStore()
  const deps = {
    view: stubView({ 'agent-1': 'worker-1', 'agent-2': 'worker-2' }),
    store,
    chatStore: createChatStore(),
    clientId: () => 'client-1',
    focusedAgentId: () => focused,
    forceScrollToBottom: vi.fn(),
  }
  const ops = createAgentInputQueueOperations(deps)
  return {
    ops,
    store,
    focusOn: (agentId: string) => {
      focused = agentId
    },
  }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('createAgentInputQueueOperations queue commands', () => {
  it('applies the snapshot of a successful response to the store', async () => {
    const { ops, store } = setup()
    deleteQueuedAgentInput.mockResolvedValueOnce({
      snapshot: snapshotOf(4n, ['q2', 'q3']),
    })
    expect(store.get('agent-1')).toBeUndefined()

    await ops.deleteQueueItem('agent-1', queuedItem('q1'))

    expect(store.get('agent-1')?.revision).toBe(4n)
    expect(store.get('agent-1')?.items.map(item => item.id)).toEqual(['q2', 'q3'])
    expect(showWarnToast).not.toHaveBeenCalled()
  })

  it('shows the warning before it rethrows, and leaves the store untouched', async () => {
    const { ops, store } = setup()
    const failure = new Error('worker unreachable')
    deleteQueuedAgentInput.mockRejectedValueOnce(failure)
    // The order proves the toast is not lost when the caller handles the
    // rejection: a rethrow that ran first would abandon the warning.
    const order: string[] = []
    showWarnToast.mockImplementation(() => {
      order.push('toast')
    })

    let caught: unknown
    try {
      await ops.deleteQueueItem('agent-1', queuedItem('q1'))
    }
    catch (error) {
      order.push('rethrow')
      caught = error
    }

    expect(order).toEqual(['toast', 'rethrow'])
    expect(caught).toBe(failure)
    expect(showWarnToast).toHaveBeenCalledWith('Failed to delete queued input', failure)
    expect(store.get('agent-1')).toBeUndefined()
  })

  it('routes a command to the Worker of the agent it targets, not of the focused agent', async () => {
    const { ops } = setup({
      workerByAgent: { 'agent-1': 'worker-1', 'agent-2': 'worker-2' },
      focusedAgentId: 'agent-1',
    })
    deleteQueuedAgentInput.mockResolvedValueOnce({ snapshot: undefined })

    await ops.deleteQueueItem('agent-2', queuedItem('q1'))

    expect(deleteQueuedAgentInput.mock.calls[0]?.[0]).toBe('worker-2')
  })

  it('sends an empty Worker id for an agent that left the join', async () => {
    const { ops } = setup()
    deleteQueuedAgentInput.mockResolvedValueOnce({ snapshot: undefined })

    await ops.deleteQueueItem('agent-gone', queuedItem('q1'))

    expect(deleteQueuedAgentInput.mock.calls[0]?.[0]).toBe('')
  })

  it('hands the begin-edit response back to the caller and applies its snapshot', async () => {
    const { ops, store } = setup()
    const response = {
      snapshot: snapshotOf(2n, ['q1']),
      text: 'the full draft',
    }
    beginQueuedAgentInputEdit.mockResolvedValueOnce(response)

    const returned = await ops.beginQueueEdit('agent-1', queuedItem('q1'), true)

    expect(returned).toBe(response)
    expect(store.get('agent-1')?.revision).toBe(2n)
    expect(beginQueuedAgentInputEdit.mock.calls[0]?.[1]).toMatchObject({ clientId: 'client-1', takeover: true })
  })

  // A queued item states no agent, so each command takes the agent beside it.
  // That agent picks the Worker, fills the request, and keys the snapshot in
  // the store, whatever agent has the focus.
  describe('each command for an agent that does not have the focus', () => {
    type Ops = ReturnType<typeof setup>['ops']
    const commands: Array<{
      name: string
      rpc: ReturnType<typeof vi.fn>
      run: (ops: Ops) => Promise<unknown>
      request: Record<string, unknown>
    }> = [
      {
        name: 'beginQueueEdit',
        rpc: beginQueuedAgentInputEdit,
        run: ops => ops.beginQueueEdit('agent-2', queuedItem('q1'), false),
        request: {
          agentId: 'agent-2',
          inputId: 'q1',
          clientId: 'client-1',
          takeover: false,
        },
      },
      {
        name: 'updateQueueItem',
        rpc: updateQueuedAgentInput,
        run: ops => ops.updateQueueItem('agent-2', queuedItem('q1'), 'edited', [], undefined),
        request: {
          agentId: 'agent-2',
          inputId: 'q1',
          clientId: 'client-1',
          expectedVersion: 7n,
          text: 'edited',
          attachments: [],
        },
      },
      {
        name: 'cancelQueueEdit',
        rpc: cancelQueuedAgentInputEdit,
        run: ops => ops.cancelQueueEdit('agent-2', queuedItem('q1')),
        request: { agentId: 'agent-2', inputId: 'q1', clientId: 'client-1' },
      },
      {
        name: 'deleteQueueItem',
        rpc: deleteQueuedAgentInput,
        run: ops => ops.deleteQueueItem('agent-2', queuedItem('q1')),
        request: { agentId: 'agent-2', inputId: 'q1' },
      },
      {
        name: 'moveQueueItem',
        rpc: moveQueuedAgentInput,
        run: ops => ops.moveQueueItem('agent-2', queuedItem('q1'), 'q0'),
        request: { agentId: 'agent-2', inputId: 'q1', beforeInputId: 'q0' },
      },
      {
        name: 'retryQueueItem',
        rpc: retryQueuedAgentInput,
        run: ops => ops.retryQueueItem('agent-2', queuedItem('q1'), true),
        request: { agentId: 'agent-2', inputId: 'q1', confirmDeliveryUncertain: true },
      },
      {
        name: 'steerQueueItem',
        rpc: steerQueuedAgentInput,
        run: ops => ops.steerQueueItem('agent-2', queuedItem('q1')),
        request: { agentId: 'agent-2', inputId: 'q1' },
      },
      {
        name: 'preemptQueueItem',
        rpc: preemptQueuedAgentInput,
        run: ops => ops.preemptQueueItem('agent-2', queuedItem('q1')),
        request: { agentId: 'agent-2', inputId: 'q1' },
      },
      {
        name: 'setQueuePaused',
        rpc: setAgentInputQueuePaused,
        run: ops => ops.setQueuePaused('agent-2', true),
        request: { agentId: 'agent-2', paused: true },
      },
      {
        name: 'advanceInputQueue',
        rpc: advanceAgentInputQueue,
        run: ops => ops.advanceInputQueue('agent-2'),
        request: { agentId: 'agent-2' },
      },
    ]

    it.each(commands)('$name sends the request for that agent to its Worker and stores the snapshot under it', async ({ rpc, run, request }) => {
      const { ops, store } = setup({
        workerByAgent: { 'agent-1': 'worker-1', 'agent-2': 'worker-2' },
        focusedAgentId: 'agent-1',
      })
      rpc.mockResolvedValueOnce({
        snapshot: snapshotOf(5n, ['q1']),
      })

      await run(ops)

      expect(rpc).toHaveBeenCalledExactlyOnceWith('worker-2', request)
      expect(store.get('agent-2')?.revision).toBe(5n)
      expect(store.get('agent-1'), 'the focused agent keeps no snapshot').toBeUndefined()
    })
  })

  it('returns the snapshot from advanceInputQueue after storing it', async () => {
    const { ops, store } = setup({
      workerByAgent: { 'agent-1': 'worker-1' },
      focusedAgentId: 'agent-1',
    })
    const snapshot = snapshotOf(6n, ['q1'])
    advanceAgentInputQueue.mockResolvedValueOnce({ snapshot })

    const returned = await ops.advanceInputQueue('agent-1')

    expect(returned).toBe(snapshot)
    expect(store.get('agent-1')?.revision).toBe(6n)
  })

  // The snapshot no longer states its agent, so the explicit agentId passed
  // at call time is the key the answer goes under even if focus moves.
  it('stores a pause answer under the requested agent after the focus moves', async () => {
    const { ops, store, focusOn } = setupWithMovableFocus()
    const reply = deferred<{ snapshot: ReturnType<typeof snapshotOf> }>()
    setAgentInputQueuePaused.mockReturnValueOnce(reply.promise)

    const pending = ops.setQueuePaused('agent-1', true)
    focusOn('agent-2')
    reply.resolve({
      snapshot: snapshotOf(3n, []),
    })
    await pending

    expect(store.get('agent-1')?.revision).toBe(3n)
    expect(store.get('agent-2')).toBeUndefined()
  })

  it('stores an enqueue answer under the agent focused at send time after the focus moves', async () => {
    const { ops, store, focusOn } = setupWithMovableFocus()
    const reply = deferred<{ snapshot: ReturnType<typeof snapshotOf> }>()
    enqueueAgentInput.mockReturnValueOnce(reply.promise)

    const pending = ops.sendMessage('deploy it')
    focusOn('agent-2')
    reply.resolve({
      snapshot: snapshotOf(4n, ['sent']),
    })
    await pending

    expect(enqueueAgentInput).toHaveBeenCalledExactlyOnceWith('worker-1', expect.objectContaining({ agentId: 'agent-1' }))
    expect(store.get('agent-1')?.revision).toBe(4n)
    expect(store.get('agent-2')).toBeUndefined()
  })
})

describe('createAgentInputQueueOperations composer send', () => {
  /** Read the `inputId` that the module minted for the Nth enqueue attempt. */
  const sentInputId = (attempt: number): string =>
    (enqueueAgentInput.mock.calls[attempt]?.[1] as { inputId: string }).inputId

  it('reuses the input ID when the user resends an unchanged payload', async () => {
    const { ops } = setup()
    enqueueAgentInput.mockRejectedValueOnce(new Error('answer lost'))
    enqueueAgentInput.mockResolvedValueOnce({ snapshot: undefined })
    const files = [
      attachment('notes.txt'),
    ]

    await expect(ops.sendMessage('deploy it', files)).rejects.toThrow('answer lost')
    await ops.sendMessage('deploy it', [
      attachment('notes.txt'),
    ])

    expect(enqueueAgentInput).toHaveBeenCalledTimes(2)
    expect(sentInputId(1)).toBe(sentInputId(0))
  })

  // A queue snapshot that holds the input proves that the Worker took it. The
  // Worker keeps the row of an input that the user removes, so a resend that
  // reused the old id would queue nothing while the browser reported success.
  it('mints a fresh input ID once a queue snapshot showed the unanswered attempt', async () => {
    const { ops, store } = setup()
    enqueueAgentInput.mockRejectedValueOnce(new Error('answer lost'))
    enqueueAgentInput.mockResolvedValueOnce({ snapshot: undefined })

    await expect(ops.sendMessage('deploy it')).rejects.toThrow('answer lost')
    // A broadcast shows the queued input, and the user then removes it.
    store.apply('agent-1', snapshotOf(1n, [
      sentInputId(0),
    ]))
    store.apply('agent-1', snapshotOf(2n, []))
    await ops.sendMessage('deploy it')

    expect(sentInputId(1)).not.toBe(sentInputId(0))
  })

  it('mints a fresh input ID after history shows the dispatched input', async () => {
    const { ops, chatStore } = setup()
    enqueueAgentInput.mockRejectedValueOnce(new Error('answer lost'))
    enqueueAgentInput.mockResolvedValueOnce({ snapshot: undefined })

    await expect(ops.sendMessage('deploy it')).rejects.toThrow('answer lost')
    chatStore.setMessages('agent-1', [
      transcriptMessage(sentInputId(0)),
    ], false)
    await ops.sendMessage('deploy it')

    expect(enqueueAgentInput).toHaveBeenCalledTimes(2)
    expect(sentInputId(1)).not.toBe(sentInputId(0))
  })

  it('confirms an in-flight send from the transcript when the enqueue response is lost', async () => {
    const { ops, chatStore } = setup()
    const response = deferred<{ snapshot: undefined }>()
    enqueueAgentInput.mockReturnValueOnce(response.promise)

    const send = ops.sendMessage('deploy it')
    const settled = Promise.allSettled([send])
    await settleWorkerReplies()
    expect(enqueueAgentInput).toHaveBeenCalledOnce()
    chatStore.addMessage('agent-1', transcriptMessage(sentInputId(0)))
    response.reject(new Error('answer lost'))

    expect(await settled).toEqual([
      { status: 'fulfilled', value: undefined },
    ])
    expect(showWarnToast).not.toHaveBeenCalled()
  })

  it('keeps the original input ID when chat shows a row that does not prove acceptance', async () => {
    const { ops, chatStore } = setup()
    enqueueAgentInput.mockRejectedValueOnce(new Error('answer lost'))
    enqueueAgentInput.mockResolvedValueOnce({ snapshot: undefined })

    await expect(ops.sendMessage('deploy it')).rejects.toThrow('answer lost')
    const id = sentInputId(0)
    chatStore.addMessage('agent-2', transcriptMessage(id))
    chatStore.addMessage('agent-1', transcriptMessage(id, 0n))
    chatStore.addMessage('agent-1', transcriptMessage(id, 1n, MessageSource.AGENT))
    await ops.sendMessage('deploy it')

    expect(sentInputId(1)).toBe(id)
  })

  it('keeps the input ID of an unanswered attempt that no snapshot showed', async () => {
    const { ops, store } = setup()
    enqueueAgentInput.mockRejectedValueOnce(new Error('answer lost'))
    enqueueAgentInput.mockResolvedValueOnce({ snapshot: undefined })

    await expect(ops.sendMessage('deploy it')).rejects.toThrow('answer lost')
    store.apply('agent-1', snapshotOf(1n, ['someone-else']))
    await ops.sendMessage('deploy it')

    expect(sentInputId(1)).toBe(sentInputId(0))
  })

  it('mints a fresh input ID once the Worker accepted the payload', async () => {
    const { ops } = setup()
    enqueueAgentInput.mockResolvedValue({ snapshot: undefined })

    await ops.sendMessage('deploy it')
    await ops.sendMessage('deploy it')

    expect(sentInputId(1)).not.toBe(sentInputId(0))
  })

  it('mints a fresh input ID when the resent text differs', async () => {
    const { ops } = setup()
    enqueueAgentInput.mockRejectedValueOnce(new Error('answer lost'))
    enqueueAgentInput.mockResolvedValueOnce({ snapshot: undefined })

    await expect(ops.sendMessage('deploy it')).rejects.toThrow('answer lost')
    await ops.sendMessage('deploy it now')

    expect(sentInputId(1)).not.toBe(sentInputId(0))
  })

  it('scrolls to the live tail before the RPC, and also when the RPC fails', async () => {
    const { ops, forceScrollToBottom } = setup()
    enqueueAgentInput.mockImplementationOnce(() => {
      expect(forceScrollToBottom).toHaveBeenCalledTimes(1)
      return Promise.reject(new Error('answer lost'))
    })

    await expect(ops.sendMessage('deploy it')).rejects.toThrow('answer lost')

    expect(forceScrollToBottom).toHaveBeenCalledTimes(1)
  })

  it('sends the message kind for a send', async () => {
    const { ops } = setup()
    enqueueAgentInput.mockResolvedValue({ snapshot: undefined })

    await ops.sendMessage('deploy it')

    expect(enqueueAgentInput.mock.calls[0]?.[1]).toMatchObject({ kind: AgentInputKind.USER_MESSAGE })
  })

  it('sends nothing when no agent tab is focused', async () => {
    const { ops, forceScrollToBottom } = setup({ focusedAgentId: null })

    await ops.sendMessage('deploy it')

    expect(enqueueAgentInput).not.toHaveBeenCalled()
    expect(forceScrollToBottom).not.toHaveBeenCalled()
  })
})

// The schedule rides the same enqueue and update requests the text does. A
// save that clears it sends an explicit empty string, and a save that keeps it
// omits the field.
describe('agent input queue operations schedule', () => {
  it('sends the schedule on enqueue', async () => {
    const { ops } = setup({
      workerByAgent: { 'agent-1': 'w1' },
    })
    enqueueAgentInput.mockResolvedValue(snapshotOf(1n, []))
    await ops.sendMessage('later', undefined, '2030-01-01T09:00:00.000Z')
    expect(enqueueAgentInput).toHaveBeenCalledWith('w1', {
      agentId: 'agent-1',
      inputId: expect.any(String),
      kind: AgentInputKind.USER_MESSAGE,
      text: 'later',
      attachments: [],
      scheduledAt: '2030-01-01T09:00:00.000Z',
    })
  })

  it('sends an empty schedule on an immediate enqueue', async () => {
    const { ops } = setup({
      workerByAgent: { 'agent-1': 'w1' },
    })
    enqueueAgentInput.mockResolvedValue(snapshotOf(1n, []))
    await ops.sendMessage('now')
    expect(enqueueAgentInput.mock.calls[0]?.[1]).toMatchObject({ scheduledAt: '' })
  })

  it('omits the schedule from a save that keeps it', async () => {
    const { ops } = setup({
      workerByAgent: { 'agent-1': 'w1' },
    })
    updateQueuedAgentInput.mockResolvedValue(snapshotOf(2n, []))
    await ops.updateQueueItem('agent-1', queuedItem('input-1'), 'edited', [], undefined)
    const request = updateQueuedAgentInput.mock.calls[0]?.[1] as Record<string, unknown>
    expect('scheduledAt' in request).toBe(false)
  })

  it('sends the schedule on update, and an explicit empty string when clearing', async () => {
    const { ops } = setup({
      workerByAgent: { 'agent-1': 'w1' },
    })
    updateQueuedAgentInput.mockResolvedValue(snapshotOf(2n, []))
    const item = queuedItem('input-1')
    await ops.updateQueueItem('agent-1', item, 'edited', [], '2030-01-01T09:00:00.000Z')
    expect(updateQueuedAgentInput).toHaveBeenCalledWith('w1', {
      agentId: 'agent-1',
      inputId: 'input-1',
      clientId: 'client-1',
      expectedVersion: 7n,
      text: 'edited',
      attachments: [],
      scheduledAt: '2030-01-01T09:00:00.000Z',
    })
    await ops.updateQueueItem('agent-1', item, 'edited now', [], '')
    expect(updateQueuedAgentInput).toHaveBeenLastCalledWith('w1', {
      agentId: 'agent-1',
      inputId: 'input-1',
      clientId: 'client-1',
      expectedVersion: 7n,
      text: 'edited now',
      attachments: [],
      scheduledAt: '',
    })
  })
})

// An expired schedule is valid only for an exact unanswered delivery.
describe('createAgentInputQueueOperations retryPendingMessage', () => {
  const at = '2030-01-01T09:00:00.000Z'
  type EnqueueResponse = ReturnType<typeof deferred<{ snapshot: undefined }>>

  it.each([
    new ChannelError('rpc', 'queue full', { code: Code.InvalidArgument }),
    new ChannelError('rpc', 'refused', { code: Code.FailedPrecondition }),
    new ConnectError('not authorized', Code.PermissionDenied),
  ])('keeps a definitive refusal out of the unanswered-attempt exception: %s', async (error) => {
    const { ops, forceScrollToBottom } = setup()
    enqueueAgentInput.mockRejectedValueOnce(error)
    await expect(ops.sendMessage('later', [], at)).rejects.toThrow(error.message)
    forceScrollToBottom.mockClear()
    expect(await ops.retryPendingMessage('later', [], at)).toBe(false)
    expect(enqueueAgentInput).toHaveBeenCalledTimes(1)
    expect(forceScrollToBottom).not.toHaveBeenCalled()
  })

  it.each([
    new Error('answer lost'),
    new ChannelError('transport', 'disconnected'),
    new ChannelError('rpc', 'commit uncertain', { code: Code.Internal }),
  ])('reuses the original id and schedule for an unanswered attempt: %s', async (error) => {
    const { ops } = setup()
    enqueueAgentInput.mockRejectedValueOnce(error)
    await expect(ops.sendMessage('later', undefined, at)).rejects.toThrow(error.message)
    expect(await ops.retryPendingMessage('later')).toBe(false)
    expect(await ops.retryPendingMessage('other', [], at)).toBe(false)
    enqueueAgentInput.mockResolvedValueOnce({
      snapshot: snapshotOf(1n, []),
    })
    expect(await ops.retryPendingMessage('later', [], at)).toBe(true)
    const [first, second] = enqueueAgentInput.mock.calls.map(call => (call[1] as { inputId: string }).inputId)
    expect(second).toBe(first)
    expect(enqueueAgentInput.mock.calls[1]?.[1]).toMatchObject({ scheduledAt: at, text: 'later' })
    expect(await ops.retryPendingMessage('later', [], at)).toBe(false)
  })

  it.each([
    new Error('answer lost'),
    new ChannelError('rpc', 'queue full', { code: Code.InvalidArgument }),
  ])('shares one in-flight request across identical sends: %s', async (error) => {
    const { ops } = setup()
    const response = deferred<{ snapshot: undefined }>()
    enqueueAgentInput.mockReturnValue(response.promise)
    const settled = Promise.allSettled([
      ops.sendMessage('later', [], at),
      ops.sendMessage('later', [], at),
    ])
    // Each send hashes its payload in the hash worker before it looks for an
    // in-flight request.
    await settleWorkerReplies()
    expect(enqueueAgentInput).toHaveBeenCalled()
    response.reject(error)
    const results = await settled
    // The second send joined the first request instead of sending its own.
    expect(enqueueAgentInput).toHaveBeenCalledTimes(1)
    expect(results.map(result => result.status)).toEqual(['rejected', 'rejected'])
  })

  // The hash of the payload runs off the main thread, so the first request can
  // settle while a second identical send still hashes. The second send started
  // while the first was in flight, so it shares the first one's outcome, as it
  // did when the hash ran in one task.
  it.each([
    {
      outcome: 'accepted',
      answer: (response: EnqueueResponse) => response.resolve({ snapshot: undefined }),
    },
    {
      outcome: 'refused',
      answer: (response: EnqueueResponse) => response.reject(new ChannelError('rpc', 'queue full', { code: Code.InvalidArgument })),
    },
  ])('joins an identical send that settles as $outcome while the second send hashes', async ({ answer }) => {
    const { ops } = setup()
    const response: EnqueueResponse = deferred()
    enqueueAgentInput.mockReturnValueOnce(response.promise)
    enqueueAgentInput.mockResolvedValue({ snapshot: undefined })
    const first = ops.sendMessage('later', [], at)
    await settleWorkerReplies()
    expect(enqueueAgentInput).toHaveBeenCalledOnce()

    // The second hash waits until the first request settles.
    const hash = holdWorkerReplies('lib/fieldDigestWorker.ts')
    const second = Promise.allSettled([
      ops.sendMessage('later', [], at),
    ])
    answer(response)
    const [firstResult] = await Promise.allSettled([first])
    await settleWorkerReplies()
    expect(hash.pending()).toBe(1)
    await hash.release()
    const [secondResult] = await second

    expect(enqueueAgentInput).toHaveBeenCalledOnce()
    expect(secondResult!.status).toBe(firstResult!.status)
  })

  it('keeps earlier uncertainty when a later attempt receives a refusal', async () => {
    const { ops } = setup()
    enqueueAgentInput.mockRejectedValueOnce(new Error('answer lost'))
    await expect(ops.sendMessage('later', [], at)).rejects.toThrow('answer lost')
    enqueueAgentInput.mockRejectedValueOnce(new ChannelError('rpc', 'not authorized', { code: Code.PermissionDenied }))
    await expect(ops.retryPendingMessage('later', [], at)).rejects.toThrow('not authorized')
    enqueueAgentInput.mockResolvedValueOnce({ snapshot: undefined })
    expect(await ops.retryPendingMessage('later', [], at)).toBe(true)
    const ids = enqueueAgentInput.mock.calls.map(call => (call[1] as { inputId: string }).inputId)
    expect(new Set(ids).size).toBe(1)
  })

  it('sends nothing while no agent has focus', async () => {
    const { ops } = setup({ focusedAgentId: null })
    expect(await ops.retryPendingMessage('later', [], at)).toBe(false)
    expect(enqueueAgentInput).not.toHaveBeenCalled()
  })
})
