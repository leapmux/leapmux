import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentTab } from '~/stores/tab.types'
import type { TabView } from '~/stores/tabView'
import { create } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  AgentInputKind,
  AgentInputQueueSnapshotSchema,
  QueuedAgentInputSchema,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { ChannelError } from '~/lib/channelError'
import { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import { deferred } from '~/test-support/async'
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
vi.mock('~/api/workerRpc', () => ({
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
  const forceScrollToBottom = vi.fn()
  const ops = createAgentInputQueueOperations({
    view: stubView(options.workerByAgent ?? { 'agent-1': 'worker-1' }),
    store,
    clientId: () => 'client-1',
    focusedAgentId: () => (options.focusedAgentId === undefined
      ? 'agent-1'
      : options.focusedAgentId),
    forceScrollToBottom,
  })
  return { ops, store, forceScrollToBottom }
}

/** The `setup` harness over two agents, with a focus that a test can move. */
function setupWithMovableFocus() {
  let focused = 'agent-1'
  const store = createAgentInputQueueStore()
  const ops = createAgentInputQueueOperations({
    view: stubView({ 'agent-1': 'worker-1', 'agent-2': 'worker-2' }),
    store,
    clientId: () => 'client-1',
    focusedAgentId: () => focused,
    forceScrollToBottom: vi.fn(),
  })
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

  it('routes a command to the Worker of the agent it names, not of the focused agent', async () => {
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

  it('reads the focused agent once when it changes the pause state', async () => {
    const { ops } = setup({
      workerByAgent: { 'agent-7': 'worker-7' },
      focusedAgentId: 'agent-7',
    })
    setAgentInputQueuePaused.mockResolvedValueOnce({ snapshot: undefined })

    await ops.setQueuePaused(true)

    expect(setAgentInputQueuePaused.mock.calls[0]?.[0]).toBe('worker-7')
    expect(setAgentInputQueuePaused.mock.calls[0]?.[1]).toMatchObject({ agentId: 'agent-7', paused: true })
  })

  // The snapshot no longer states its agent, so the agent read at call time is
  // the only key the answer can go under. A tab switch while the RPC runs
  // must not file one agent's queue under another.
  it('stores a pause answer under the agent focused at call time after the focus moves', async () => {
    const { ops, store, focusOn } = setupWithMovableFocus()
    const reply = deferred<{ snapshot: ReturnType<typeof snapshotOf> }>()
    setAgentInputQueuePaused.mockReturnValueOnce(reply.promise)

    const pending = ops.setQueuePaused(true)
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

  it('sends the control-feedback kind for a control reply and the message kind for a send', async () => {
    const { ops } = setup()
    enqueueAgentInput.mockResolvedValue({ snapshot: undefined })

    await ops.sendControlFeedback('use the other branch')
    await ops.sendMessage('deploy it')

    expect(enqueueAgentInput.mock.calls[0]?.[1]).toMatchObject({ kind: AgentInputKind.CONTROL_FEEDBACK })
    expect(enqueueAgentInput.mock.calls[1]?.[1]).toMatchObject({ kind: AgentInputKind.USER_MESSAGE })
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
    try {
      expect(enqueueAgentInput).toHaveBeenCalledTimes(1)
    }
    finally {
      response.reject(error)
      await settled
    }
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
