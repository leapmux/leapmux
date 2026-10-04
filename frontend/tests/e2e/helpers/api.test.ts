import type { ChannelTransport } from '../../../src/lib/channel'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { ChannelManager } from '../../../src/lib/channel'
import { API_POLL_INTERVAL_MS, closeTestChannels, deleteWorkspaceViaAPI, getTestChannel, openAgentViaAPI, waitForNewOnlineWorkerViaAPI } from './api'
import { createTestChannelManager } from './e2e-channel'

vi.mock('./e2e-channel', () => ({ createTestChannelManager: vi.fn() }))

let hubNumber = 0
let hubUrl: string
const callWorker = vi.fn<(...args: unknown[]) => Promise<unknown>>(async () => undefined)
const closeAll = vi.fn()
/** Create a dormant real manager. Reject unexpected transport calls. */
function controlledChannelManager(closeHandler = closeAll) {
  const transport: ChannelTransport = {
    getWorkerHandshakeParams: async () => { throw new Error('The controlled manager must not start a handshake.') },
    openChannel: async () => { throw new Error('The controlled manager must not open a channel.') },
    closeChannel: async () => { throw new Error('The controlled manager must not close a transport channel.') },
    createWebSocket: () => { throw new Error('The controlled manager must not open a WebSocket.') },
  }
  return Object.assign(new ChannelManager(transport, { installWakeListener: false }), { callWorker, closeAll: closeHandler })
}
const channel = controlledChannelManager()

beforeEach(() => {
  hubUrl = `http://fixture-${hubNumber++}.test`
  callWorker.mockReset()
  callWorker.mockResolvedValue(undefined)
  closeAll.mockReset()
  vi.mocked(createTestChannelManager).mockReset()
  vi.mocked(createTestChannelManager).mockResolvedValue(channel)
})

afterEach(async () => {
  await closeTestChannels(hubUrl)
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function deletionResponses(workerTabs: unknown[], onlineWorkers = ['worker-a', 'worker-b']) {
  const fetch = vi.fn(async (input: string | URL | Request) => {
    const url = String(input)
    if (url.endsWith('/DeleteWorkspace'))
      return Response.json({ workerTabs })
    if (url.endsWith('/ListWorkers'))
      return Response.json({ workers: onlineWorkers.map(id => ({ id, online: true })) })
    throw new Error(`Unexpected request: ${url}`)
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}

describe('openAgentViaAPI', () => {
  it.each(['', '   ', '\n\t'])('rejects an empty requested native session before it opens a channel: %j', async (agentSessionId) => {
    await expect(openAgentViaAPI(hubUrl, 'session', 'worker-a', 'workspace', '/project', { agentSessionId }))
      .rejects
      .toThrow('The native session ID must be nonempty')
    expect(createTestChannelManager).not.toHaveBeenCalled()
    expect(callWorker).not.toHaveBeenCalled()
  })

  it('sends the requested native session ID through the actual Worker request', async () => {
    const stopped = new Error('The controlled Worker request stopped before tab registration.')
    callWorker.mockRejectedValue(stopped)
    await expect(openAgentViaAPI(hubUrl, 'session', 'worker-a', 'workspace', '/project', { agentSessionId: 'native-conversation-42' })).rejects.toBe(stopped)
    expect(callWorker).toHaveBeenCalledTimes(1)
    expect(callWorker.mock.calls[0]?.[4]).toMatchObject({ workerId: 'worker-a', workingDir: '/project', agentSessionId: 'native-conversation-42' })
  })

  it('keeps session creation when no native session ID is supplied', async () => {
    const stopped = new Error('The controlled Worker request stopped before tab registration.')
    callWorker.mockRejectedValue(stopped)
    await expect(openAgentViaAPI(hubUrl, 'session', 'worker-a', 'workspace', '/project')).rejects.toBe(stopped)
    expect(callWorker.mock.calls[0]?.[4]).not.toHaveProperty('agentSessionId')
  })
})

describe('workspace deletion cleanup', () => {
  it('closes the tabs from the atomic delete response on every owning worker', async () => {
    const fetch = deletionResponses([
      { workerId: 'worker-a', tabs: [{ tabType: 'TAB_TYPE_AGENT', tabId: 'agent-a' }] },
      { workerId: 'worker-b', tabs: [{ tabType: 'TAB_TYPE_TERMINAL', tabId: 'terminal-b' }, { tabType: 'TAB_TYPE_IMAGE', tabId: 'image-b' }, { tabType: 'TAB_TYPE_FILE', tabId: 'file-b' }] },
    ])
    await deleteWorkspaceViaAPI(hubUrl, 'session', 'workspace')
    expect(fetch.mock.calls.map(([url]) => String(url).split('/').at(-1)))
      .toEqual(['DeleteWorkspace', 'ListWorkers'])
    expect(callWorker).toHaveBeenCalledTimes(2)
    expect(callWorker.mock.calls.map(call => call[0])).toEqual(['worker-a', 'worker-b'])
    expect(callWorker.mock.calls[0]?.[4]).toMatchObject({ tabs: [{ tabType: TabType.AGENT, tabId: 'agent-a' }] })
    expect(callWorker.mock.calls[1]?.[4]).toMatchObject({ tabs: [
      { tabType: TabType.TERMINAL, tabId: 'terminal-b' },
      { tabType: TabType.IMAGE, tabId: 'image-b' },
      { tabType: TabType.FILE, tabId: 'file-b' },
    ] })
  })

  it('does no channel or worker lookup for an empty workspace', async () => {
    const fetch = deletionResponses([])
    await deleteWorkspaceViaAPI(hubUrl, 'session', 'workspace')
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(createTestChannelManager).not.toHaveBeenCalled()
  })

  it('does not wait for an offline worker during cleanup', async () => {
    deletionResponses([{ workerId: 'worker-a', tabs: [{ tabType: 'TAB_TYPE_AGENT', tabId: 'agent-a' }] }], [])
    await deleteWorkspaceViaAPI(hubUrl, 'session', 'workspace')
    expect(createTestChannelManager).not.toHaveBeenCalled()
  })

  it('accepts a workspace that the test already deleted', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 404 })))
    await expect(deleteWorkspaceViaAPI(hubUrl, 'session', 'workspace')).resolves.toBeUndefined()
    expect(createTestChannelManager).not.toHaveBeenCalled()
  })

  it('reports malformed tab data instead of skipping worker cleanup', async () => {
    deletionResponses([{ workerId: 'worker-a', tabs: [{ tabType: 'TAB_TYPE_UNKNOWN', tabId: 'agent-a' }] }])
    await expect(deleteWorkspaceViaAPI(hubUrl, 'session', 'workspace')).rejects.toThrow()
    expect(callWorker).not.toHaveBeenCalled()
  })

  it('attempts every worker cleanup before reporting a failure', async () => {
    deletionResponses([
      { workerId: 'worker-a', tabs: [{ tabType: 'TAB_TYPE_AGENT', tabId: 'agent-a' }] },
      { workerId: 'worker-b', tabs: [{ tabType: 'TAB_TYPE_AGENT', tabId: 'agent-b' }] },
    ])
    callWorker.mockRejectedValueOnce(new Error('worker-a failed'))
    await expect(deleteWorkspaceViaAPI(hubUrl, 'session', 'workspace')).rejects.toBeInstanceOf(AggregateError)
    expect(callWorker).toHaveBeenCalledTimes(2)
  })
})

describe('test channel initialization', () => {
  it('closes only the selected hub and creates a fresh manager on reuse', async () => {
    const otherClose = vi.fn()
    const other = controlledChannelManager(otherClose)
    await getTestChannel(hubUrl, 'session')
    vi.mocked(createTestChannelManager).mockResolvedValueOnce(other)
    await getTestChannel('http://other.test', 'session')
    try {
      await closeTestChannels(hubUrl)
      expect(closeAll).toHaveBeenCalledOnce()
      expect(otherClose).not.toHaveBeenCalled()
      await getTestChannel(hubUrl, 'session')
      expect(createTestChannelManager).toHaveBeenCalledTimes(3)
    }
    finally {
      await closeTestChannels('http://other.test')
    }
  })

  it('waits for pending initialization before closing its manager', async () => {
    let ready!: (channel: ChannelManager) => void
    vi.mocked(createTestChannelManager).mockReturnValueOnce(new Promise((resolve) => {
      ready = resolve
    }))
    const opened = getTestChannel(hubUrl, 'session')
    let finished = false
    const closed = closeTestChannels(hubUrl).then(() => {
      finished = true
    })
    await Promise.resolve()
    expect(finished).toBe(false)
    ready(channel)
    await Promise.all([opened, closed])
    expect(closeAll).toHaveBeenCalledOnce()
  })

  it('keeps a replacement when a removed initialization rejects', async () => {
    let fail!: (error: Error) => void
    vi.mocked(createTestChannelManager).mockReturnValueOnce(new Promise((_resolve, reject) => {
      fail = reject
    }))
    const first = getTestChannel(hubUrl, 'session').catch(error => error)
    const closing = closeTestChannels(hubUrl)
    await getTestChannel(hubUrl, 'session')
    fail(new Error('old initialization failed'))
    await Promise.all([first, closing])
    await getTestChannel(hubUrl, 'session')
    expect(createTestChannelManager).toHaveBeenCalledTimes(2)
  })

  it('shares an in-flight connection attempt', async () => {
    await Promise.all([getTestChannel(hubUrl, 'session'), getTestChannel(hubUrl, 'session')])
    await getTestChannel(hubUrl, 'session')
    expect(createTestChannelManager).toHaveBeenCalledTimes(1)
  })

  it('permits a new connection after an initialization failure', async () => {
    const error = new Error('hub unavailable')
    vi.mocked(createTestChannelManager).mockRejectedValueOnce(error)
    await expect(getTestChannel(hubUrl, 'session')).rejects.toBe(error)
    await expect(getTestChannel(hubUrl, 'session')).resolves.toBe(channel)
    expect(createTestChannelManager).toHaveBeenCalledTimes(2)
  })
})

describe('waitForNewOnlineWorkerViaAPI', () => {
  it('returns the actual new online identity and excludes old or offline Workers', async () => {
    const fetch = vi.fn(async () => Response.json({ workers: [
      { id: 'old-worker', online: true },
      { id: 'offline-worker', online: false },
      { id: 'new-worker', online: true },
    ] }))
    vi.stubGlobal('fetch', fetch)
    await expect(waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set(['old-worker']))).resolves.toBe('new-worker')
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('rejects an aborted online wait before any HTTP request', async () => {
    const abort = new AbortController()
    const reason = new Error('The Worker already stopped.')
    abort.abort(reason)
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    await expect(waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set(), undefined, abort.signal)).rejects.toBe(reason)
    expect(fetch).not.toHaveBeenCalled()
  })

  it('cancels the actual in-flight HTTP request', async () => {
    const abort = new AbortController()
    const reason = new Error('The Worker failed during registration.')
    let entered!: () => void
    const requested = new Promise<void>((resolve) => {
      entered = resolve
    })
    const fetch = vi.fn((_url: string, init: RequestInit) => {
      const signal = init.signal
      if (!signal)
        throw new Error('The online HTTP request received no AbortSignal.')
      entered()
      return new Promise<Response>((_, reject) => {
        signal.addEventListener('abort', () => reject(signal.reason), { once: true })
      })
    })
    vi.stubGlobal('fetch', fetch)
    const waiting = waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set(), undefined, abort.signal)
    await requested
    const rejected = expect(waiting).rejects.toBe(reason)
    abort.abort(reason)
    await rejected
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('cancels the polling interval without another request or retained timer', async () => {
    vi.useFakeTimers()
    const abort = new AbortController()
    const reason = new Error('The Worker stopped before it reached online state.')
    const fetch = vi.fn(async () => Response.json({ workers: [] }))
    vi.stubGlobal('fetch', fetch)
    const waiting = waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set(), undefined, abort.signal)
    await vi.advanceTimersByTimeAsync(0)
    expect(vi.getTimerCount()).toBe(1)
    const rejected = expect(waiting).rejects.toBe(reason)
    abort.abort(reason)
    await rejected
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(API_POLL_INTERVAL_MS)
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('rejects an identity if cancellation arrives while the HTTP body decodes', async () => {
    const abort = new AbortController()
    const reason = new Error('The Worker stopped while its identity decoded.')
    let entered!: () => void
    let finishBody!: (value: unknown) => void
    const decoding = new Promise<void>((resolve) => {
      entered = resolve
    })
    const response = Response.json({})
    vi.spyOn(response, 'json').mockImplementation(() => {
      entered()
      return new Promise((resolve) => {
        finishBody = resolve
      })
    })
    vi.stubGlobal('fetch', vi.fn(async () => response))
    const waiting = waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set(), undefined, abort.signal)
    await decoding
    const rejected = expect(waiting).rejects.toBe(reason)
    abort.abort(reason)
    finishBody({ workers: [{ id: 'stopped-worker', online: true }] })
    await rejected
  })
})
