import type { ChannelTransport } from '../../../src/lib/channel'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { ChannelManager } from '../../../src/lib/channel'
import { API_POLL_INTERVAL_MS, closeTestChannels, deleteAllWorkspacesViaAPI, deleteWorkspaceViaAPI, getTestChannel, hubSettingsDrift, openAgentViaAPI, resetHubSettingsViaAPI, waitForNewOnlineWorkerViaAPI } from './api'
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

describe('deleteAllWorkspacesViaAPI', () => {
  /** Answer ListWorkspaces with `ids`, and answer each DeleteWorkspace with the status that `statuses` gives its ID. */
  function workspaceResponses(ids: string[], statuses: Record<string, number> = {}) {
    const deleted: string[] = []
    const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/ListWorkspaces'))
        return Response.json({ workspaces: ids.map(id => ({ id })) })
      if (url.endsWith('/DeleteWorkspace')) {
        const { workspaceId } = JSON.parse(String(init?.body)) as { workspaceId: string }
        deleted.push(workspaceId)
        const status = statuses[workspaceId] ?? 200
        return status === 200 ? Response.json({ workerTabs: [] }) : new Response(`refused ${workspaceId}`, { status })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetch)
    return { fetch, deleted }
  }

  it('deletes every workspace of the account', async () => {
    const { deleted } = workspaceResponses(['ws-1', 'ws-2', 'ws-3'])
    await deleteAllWorkspacesViaAPI(hubUrl, 'session')
    expect(deleted.toSorted()).toEqual(['ws-1', 'ws-2', 'ws-3'])
  })

  it('sends no delete for an account with no workspace', async () => {
    const { fetch } = workspaceResponses([])
    await deleteAllWorkspacesViaAPI(hubUrl, 'session')
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('tries every delete when one fails, and reports each failure with its cause', async () => {
    const { deleted } = workspaceResponses(['ws-1', 'ws-2', 'ws-3'], { 'ws-1': 500, 'ws-3': 503 })
    const failure = await deleteAllWorkspacesViaAPI(hubUrl, 'session').then(() => null, (error: unknown) => error)
    expect(deleted.toSorted()).toEqual(['ws-1', 'ws-2', 'ws-3'])
    expect(failure).toBeInstanceOf(AggregateError)
    const messages = (failure as AggregateError).errors.map(error => (error as Error).message)
    expect(messages).toEqual([
      'deleteWorkspaceViaAPI(ws-1) failed: 500 refused ws-1',
      'deleteWorkspaceViaAPI(ws-3) failed: 503 refused ws-3',
    ])
  })

  it('accepts a workspace that is already gone', async () => {
    workspaceResponses(['ws-1'], { 'ws-1': 404 })
    await expect(deleteAllWorkspacesViaAPI(hubUrl, 'session')).resolves.toBeUndefined()
  })
})

describe('hubSettingsDrift', () => {
  const altcha = { key: 'captcha.altcha', valueJson: '{"hmac_key":"<redacted>"}' }

  it('resets only the customized keys that the baseline does not hold', () => {
    const current = [altcha, { key: 'smtp', valueJson: '{"host":"127.0.0.1"}' }, { key: 'open_app_registration', valueJson: 'true' }]
    expect(hubSettingsDrift(current, [altcha])).toEqual({ reset: ['smtp', 'open_app_registration'], changedBaseline: [] })
  })

  it('reports nothing for a hub at its baseline', () => {
    expect(hubSettingsDrift([altcha], [altcha])).toEqual({ reset: [], changedBaseline: [] })
    expect(hubSettingsDrift([], [])).toEqual({ reset: [], changedBaseline: [] })
  })

  it('reports a baseline key whose stored value changed', () => {
    const changed = { key: 'captcha.altcha', valueJson: '{"hmac_key":"<redacted>","max_number":10}' }
    expect(hubSettingsDrift([changed], [altcha])).toEqual({ reset: [], changedBaseline: ['captcha.altcha'] })
  })

  it('reports a baseline key that lost its stored value', () => {
    expect(hubSettingsDrift([], [altcha])).toEqual({ reset: [], changedBaseline: ['captcha.altcha'] })
  })
})

describe('resetHubSettingsViaAPI', () => {
  const altcha = { key: 'captcha.altcha', valueJson: '{"hmac_key":"<redacted>"}' }

  /** Answer ListSettings with `values`, and record each later request with its body. */
  function settingsResponses(values: Array<{ key: string, valueJson?: string, customized: boolean }>, failures: Record<string, number> = {}) {
    const requests: Array<{ method: string, body: unknown }> = []
    const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const method = String(input).split('/').at(-1) ?? ''
      requests.push({ method, body: JSON.parse(String(init?.body)) })
      const status = failures[method]
      if (status !== undefined)
        return new Response(`refused ${method}`, { status })
      if (method === 'ListSettings')
        return Response.json({ values })
      if (method === 'ElevateSession' || method === 'ResetSettings')
        return Response.json({})
      throw new Error(`Unexpected request: ${method}`)
    })
    vi.stubGlobal('fetch', fetch)
    return requests
  }

  it('elevates the session and resets every customized key outside the baseline in one request', async () => {
    const requests = settingsResponses([
      { ...altcha, customized: true },
      { key: 'smtp', valueJson: '{"host":"127.0.0.1"}', customized: true },
      { key: 'open_app_registration', valueJson: 'true', customized: true },
      { key: 'signup_enabled', customized: false },
    ])
    await resetHubSettingsViaAPI(hubUrl, 'session', { baseline: [altcha], password: 'secret' })
    expect(requests).toEqual([
      { method: 'ListSettings', body: {} },
      { method: 'ElevateSession', body: { currentPassword: 'secret' } },
      { method: 'ResetSettings', body: { keys: ['smtp', 'open_app_registration'] } },
    ])
  })

  it('sends no write for a hub at its baseline', async () => {
    const requests = settingsResponses([{ ...altcha, customized: true }, { key: 'smtp', customized: false }])
    await resetHubSettingsViaAPI(hubUrl, 'session', { baseline: [altcha], password: 'secret' })
    expect(requests.map(request => request.method)).toEqual(['ListSettings'])
  })

  it('fails for a changed baseline setting before it writes anything', async () => {
    const requests = settingsResponses([
      { key: 'captcha.altcha', valueJson: '{"hmac_key":"<redacted>","max_number":10}', customized: true },
      { key: 'smtp', valueJson: '{}', customized: true },
    ])
    await expect(resetHubSettingsViaAPI(hubUrl, 'session', { baseline: [altcha], password: 'secret' }))
      .rejects
      .toThrow('The hub settings captcha.altcha differ from their values at the start of the run.')
    expect(requests.map(request => request.method)).toEqual(['ListSettings'])
  })

  it('refuses a customized setting with no key', async () => {
    settingsResponses([{ key: '', valueJson: '{}', customized: true }])
    await expect(resetHubSettingsViaAPI(hubUrl, 'session', { baseline: [], password: 'secret' }))
      .rejects
      .toThrow('customized setting with no key')
  })

  it.each([
    { method: 'ListSettings', message: 'listCustomizedHubSettingsViaAPI failed: 403 refused ListSettings' },
    { method: 'ElevateSession', message: 'elevateSessionViaAPI failed: 403 refused ElevateSession' },
    { method: 'ResetSettings', message: 'resetHubSettingsViaAPI could not reset smtp: 403 refused ResetSettings' },
  ])('reports a refused $method with its status and body', async ({ method, message }) => {
    settingsResponses([{ key: 'smtp', valueJson: '{}', customized: true }], { [method]: 403 })
    await expect(resetHubSettingsViaAPI(hubUrl, 'session', { baseline: [], password: 'secret' })).rejects.toThrow(message)
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
