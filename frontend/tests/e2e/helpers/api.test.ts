import type { ChannelTransport } from '../../../src/lib/channel'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { unitWorkingDir } from '~/test-support/unitWorkingDir'
import { ChannelManager } from '../../../src/lib/channel'
import {
  API_POLL_INTERVAL_MS,
  attemptLoginViaAPI,
  callHub,
  closeTestChannels,
  configureBrokenSmtpViaAPI,
  deleteAllWorkspacesViaAPI,
  deletePasskeyViaAPI,
  deleteWorkspaceViaAPI,
  elevatedAdminSessionViaAPI,
  freshAdminSessionViaAPI,
  getTestChannel,
  getWorkerId,
  hubRefusal,
  hubSettingsDrift,
  listOnlineWorkerIDsViaAPI,
  listWorkersViaAPI,
  loginViaAPI,
  logoutViaAPI,
  openAgentViaAPI,
  resetHubSettingsViaAPI,
  sqliteTextLiteral,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
  waitForEmailEnabled,
  waitForNewOnlineWorkerViaAPI,
} from './api'
import { createTestChannelManager } from './e2e-channel'

vi.mock('./e2e-channel', () => ({ createTestChannelManager: vi.fn() }))
vi.mock('./altcha', () => ({
  solveCaptchaViaAPI: vi.fn(async () => ({ captchaPayload: 'solved-captcha', honeypot: '' })),
}))

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
  const server = () => ({ hubUrl, adminToken: 'session', workerId: 'worker-a' })

  /** Stop the Worker request, so the test reads the request that the helper sent. */
  function stopWorkerRequest(): Error {
    const stopped = new Error('The controlled Worker request stopped before tab registration.')
    callWorker.mockRejectedValue(stopped)
    return stopped
  }

  it.each(['', '   ', '\n\t'])('rejects an empty requested native session before it opens a channel: %j', async (agentSessionId) => {
    await expect(openAgentViaAPI(server(), 'workspace', '/project', { agentSessionId }))
      .rejects
      .toThrow('The native session ID must be nonempty')
    expect(createTestChannelManager).not.toHaveBeenCalled()
    expect(callWorker).not.toHaveBeenCalled()
  })

  it('sends the requested native session ID through the actual Worker request', async () => {
    const stopped = stopWorkerRequest()
    await expect(openAgentViaAPI(server(), 'workspace', '/project', { agentSessionId: 'native-conversation-42' })).rejects.toBe(stopped)
    expect(callWorker).toHaveBeenCalledTimes(1)
    expect(callWorker.mock.calls[0]?.[0]).toBe('worker-a')
    expect(callWorker.mock.calls[0]?.[4]).toMatchObject({ workerId: 'worker-a', workingDir: '/project', agentSessionId: 'native-conversation-42' })
  })

  it('keeps session creation when no native session ID is supplied', async () => {
    const stopped = stopWorkerRequest()
    await expect(openAgentViaAPI(server(), 'workspace', '/project')).rejects.toBe(stopped)
    expect(callWorker.mock.calls[0]?.[4]).not.toHaveProperty('agentSessionId')
  })

  it('sends the model in the options map beside the option values, and the provider', async () => {
    const stopped = stopWorkerRequest()
    await expect(openAgentViaAPI(server(), 'workspace', unitWorkingDir('/project'), {
      agentProvider: AgentProvider.CODEX,
      model: 'gpt-mock',
      optionValues: { effort: 'high' },
    })).rejects.toBe(stopped)
    expect(callWorker.mock.calls[0]?.[4]).toMatchObject({
      workingDir: '/project',
      agentProvider: AgentProvider.CODEX,
      options: { effort: 'high', model: 'gpt-mock' },
    })
  })

  it('sends no options map, no provider, and no title for the Worker default', async () => {
    const stopped = stopWorkerRequest()
    await expect(openAgentViaAPI(server(), 'workspace')).rejects.toBe(stopped)
    const request = callWorker.mock.calls[0]?.[4]
    expect(request).toMatchObject({ workingDir: '' })
    expect(request).not.toHaveProperty('options')
    expect(request).not.toHaveProperty('agentProvider')
    expect(request).not.toHaveProperty('title')
  })

  it.each([
    { label: 'a model given as an option value', options: { optionValues: { model: 'another-model' } }, message: 'give the model as `model`' },
    { label: 'a model given twice', options: { model: 'gpt-mock', optionValues: { model: 'another-model' } }, message: 'give the model as `model`' },
    { label: 'an empty model', options: { model: '' }, message: 'a model needs a model ID' },
    { label: 'a blank model', options: { model: '  ' }, message: 'a model needs a model ID' },
    { label: 'an explicit unspecified provider', options: { agentProvider: AgentProvider.UNSPECIFIED }, message: 'must not be UNSPECIFIED' },
  ])('refuses $label before it opens a channel', async ({ options, message }) => {
    await expect(openAgentViaAPI(server(), 'workspace', unitWorkingDir('/project'), options)).rejects.toThrow(message)
    expect(createTestChannelManager).not.toHaveBeenCalled()
    expect(callWorker).not.toHaveBeenCalled()
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
    await expect(deleteWorkspaceViaAPI(hubUrl, 'session', 'workspace')).rejects.toThrow('cannot decode enum leapmux.v1.TabType from JSON: "TAB_TYPE_UNKNOWN"')
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
      'deleteWorkspaceViaAPI(ws-1) failed: WorkspaceService/DeleteWorkspace returned HTTP 500: refused ws-1',
      'deleteWorkspaceViaAPI(ws-3) failed: WorkspaceService/DeleteWorkspace returned HTTP 503: refused ws-3',
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
    { method: 'ListSettings', message: 'listCustomizedHubSettingsViaAPI failed: AdminSettingsService/ListSettings returned HTTP 403: refused ListSettings' },
    { method: 'ElevateSession', message: 'elevateSessionViaAPI failed: UserService/ElevateSession returned HTTP 403: refused ElevateSession' },
    { method: 'ResetSettings', message: 'resetHubSettingsViaAPI(smtp) failed: AdminSettingsService/ResetSettings returned HTTP 403: refused ResetSettings' },
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

  it('keeps polling through a failed read and gives the last failure as the cause of the timeout', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn(async () => new Response('worker table locked', { status: 503 }))
    vi.stubGlobal('fetch', fetch)
    const waiting = waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set(), API_POLL_INTERVAL_MS * 2)
    const outcome = waiting.then(() => null, (error: unknown) => error)
    await vi.advanceTimersByTimeAsync(API_POLL_INTERVAL_MS * 3)
    const failure = await outcome
    expect(failure).toBeInstanceOf(Error)
    expect((failure as Error).message).toContain('no new worker came online')
    expect(((failure as Error).cause as Error).message)
      .toBe('listWorkersViaAPI failed: WorkerManagementService/ListWorkers returned HTTP 503: worker table locked')
    expect(fetch.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('fails at once when a read cannot reach the hub', async () => {
    const unreachable = new TypeError('fetch failed')
    const fetch = vi.fn(async () => {
      throw unreachable
    })
    vi.stubGlobal('fetch', fetch)
    await expect(waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set())).rejects.toMatchObject({
      message: 'listWorkersViaAPI could not reach the hub (WorkerManagementService/ListWorkers): TypeError: fetch failed',
      cause: unreachable,
    })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('returns a worker that comes online after a failed read', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn<() => Promise<Response>>()
      .mockResolvedValueOnce(new Response('restarting', { status: 503 }))
      .mockImplementation(async () => Response.json({ workers: [{ id: 'late-worker', online: true }] }))
    vi.stubGlobal('fetch', fetch)
    const waiting = waitForNewOnlineWorkerViaAPI(hubUrl, 'private-token', new Set())
    await vi.advanceTimersByTimeAsync(API_POLL_INTERVAL_MS)
    await expect(waiting).resolves.toBe('late-worker')
    expect(fetch).toHaveBeenCalledTimes(2)
  })
})

describe('waitForEmailEnabled', () => {
  it('reads again until the hub reports email enabled', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn<() => Promise<Response>>()
      .mockResolvedValueOnce(Response.json({ emailEnabled: false }))
      .mockImplementation(async () => Response.json({ emailEnabled: true }))
    vi.stubGlobal('fetch', fetch)
    const waiting = waitForEmailEnabled(hubUrl)
    await vi.advanceTimersByTimeAsync(API_POLL_INTERVAL_MS)
    await expect(waiting).resolves.toBeUndefined()
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('reads again after a refused read', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn<() => Promise<Response>>()
      .mockResolvedValueOnce(new Response('settings reload', { status: 503 }))
      .mockImplementation(async () => Response.json({ emailEnabled: true }))
    vi.stubGlobal('fetch', fetch)
    const waiting = waitForEmailEnabled(hubUrl)
    await vi.advanceTimersByTimeAsync(API_POLL_INTERVAL_MS)
    await expect(waiting).resolves.toBeUndefined()
  })

  it('states the hub reason of the last refused read when email never becomes enabled', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"code":"unavailable","message":"settings store closed"}', { status: 503 })))
    const outcome = waitForEmailEnabled(hubUrl, API_POLL_INTERVAL_MS * 2).then(() => null, (error: unknown) => error)
    await vi.advanceTimersByTimeAsync(API_POLL_INTERVAL_MS * 3)
    const failure = await outcome
    expect(failure).toBeInstanceOf(Error)
    expect((failure as Error).message).toContain('settings store closed')
  })

  it('fails at once when a read cannot reach the hub', async () => {
    const fetch = vi.fn(async () => {
      throw new TypeError('fetch failed')
    })
    vi.stubGlobal('fetch', fetch)
    await expect(waitForEmailEnabled(hubUrl)).rejects.toThrow('could not reach the hub (AuthService/GetSystemInfo)')
    expect(fetch).toHaveBeenCalledOnce()
  })
})

/** Record each request that reaches `fetch`, and answer it with `answer`. */
function recordedRequests(answer: (method: string) => Response) {
  const requests: Array<{ url: string, method: string, headers: Record<string, string>, body: unknown, redirect?: RequestRedirect }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input)
    const method = url.split('.v1.').at(-1) ?? ''
    requests.push({
      url,
      method,
      headers: init?.headers as Record<string, string>,
      body: JSON.parse(String(init?.body)),
      ...(init?.redirect ? { redirect: init.redirect } : {}),
    })
    return answer(method)
  }))
  return requests
}

describe('callHub', () => {
  it('posts the JSON body with the session cookie and returns the JSON answer', async () => {
    const requests = recordedRequests(() => Response.json({ workspaceId: 'ws-9' }))
    await expect(callHub(hubUrl, 'WorkspaceService/CreateWorkspace', { title: 'Docs' }, { cookie: 'leapmux-session=s1', operation: 'unit' }))
      .resolves
      .toEqual({ workspaceId: 'ws-9' })
    expect(requests).toEqual([{
      url: `${hubUrl}/leapmux.v1.WorkspaceService/CreateWorkspace`,
      method: 'WorkspaceService/CreateWorkspace',
      headers: { 'Content-Type': 'application/json', 'Cookie': 'leapmux-session=s1' },
      body: { title: 'Docs' },
    }])
  })

  it('sends no cookie for an RPC that needs no session', async () => {
    const requests = recordedRequests(() => Response.json({}))
    await callHub(hubUrl, 'AuthService/GetSystemInfo', {}, { operation: 'unit' })
    expect(requests[0]?.headers).toEqual({ 'Content-Type': 'application/json' })
  })

  it('states the operation, the RPC, the status, and the hub reason of a refusal', async () => {
    recordedRequests(() => new Response('{"code":"permission_denied","message":"admin only"}', { status: 403 }))
    await expect(callHub(hubUrl, 'AdminSettingsService/ListSettings', {}, { cookie: 'c', operation: 'listSettings(unit)' }))
      .rejects
      .toThrow('listSettings(unit) failed: AdminSettingsService/ListSettings returned HTTP 403: {"code":"permission_denied","message":"admin only"}')
  })

  // Node's fetch reports a transport failure as a bare "fetch failed". Its cause holds the reason, and the HTTP parser
  // of undici keeps the bytes that it refused in `data`.
  it('states the operation and the bytes that the HTTP parser refused when the request fails in transport', async () => {
    const parserError = Object.assign(new Error('Response does not match the HTTP/1.1 protocol (Expected HTTP/, RTSP/ or ICE/)'), {
      name: 'HTTPParserError',
      code: 'HPE_INVALID_CONSTANT',
      data: '\u0000\u0000\u0012\u0004',
    })
    const failure = new TypeError('fetch failed', { cause: parserError })
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw failure
    }))
    const call = callHub(hubUrl, 'UserService/ElevateSession', {}, { cookie: 'c', operation: 'elevateSessionViaAPI' })
    await expect(call).rejects.toThrow('elevateSessionViaAPI could not reach the hub (UserService/ElevateSession): '
      + 'TypeError: fetch failed; HTTPParserError HPE_INVALID_CONSTANT: Response does not match the HTTP/1.1 protocol '
      + '(Expected HTTP/, RTSP/ or ICE/); the refused bytes: "\\u0000\\u0000\\u0012\\u0004"')
    await expect(call).rejects.toMatchObject({ cause: failure })
  })

  it('keeps the error of a request that its caller aborted', async () => {
    const controller = new AbortController()
    controller.abort()
    const aborted = new DOMException('This operation was aborted', 'AbortError')
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw aborted
    }))
    await expect(callHub(hubUrl, 'WorkerManagementService/ListWorkers', {}, { cookie: 'c', signal: controller.signal, operation: 'unit' }))
      .rejects
      .toBe(aborted)
  })
})

describe('hubRefusal', () => {
  it('reads the code and the message of a Connect error', async () => {
    const res = new Response('{"code":"unauthenticated","message":"invalid credentials"}', { status: 401 })
    await expect(hubRefusal(res)).resolves.toEqual({ code: 'unauthenticated', message: 'invalid credentials' })
  })

  it('reads an absent message as empty', async () => {
    await expect(hubRefusal(new Response('{"code":"not_found"}', { status: 404 }))).resolves.toEqual({ code: 'not_found', message: '' })
  })

  it('throws for a successful response, so an expected refusal cannot pass on an accepted request', async () => {
    await expect(hubRefusal(Response.json({ user: {} }))).rejects.toThrow('the test expects a refusal')
  })

  it.each([
    { label: 'a body that is not JSON', body: '<html>Bad gateway</html>' },
    { label: 'a JSON body with no code', body: '{"message":"no code"}' },
    { label: 'a JSON body whose code is not text', body: '{"code":16}' },
  ])('throws for $label', async ({ body }) => {
    await expect(hubRefusal(new Response(body, { status: 502 }))).rejects.toThrow('holds no Connect error')
  })
})

describe('attemptLoginViaAPI', () => {
  it('sends the solved captcha fields with the credentials and returns a refusal as it is', async () => {
    const requests = recordedRequests(() => new Response('{"code":"unauthenticated","message":"invalid credentials"}', { status: 401 }))
    const res = await attemptLoginViaAPI(hubUrl, 'alice', 'old-password')
    expect(res.status).toBe(401)
    expect(requests).toEqual([{
      url: `${hubUrl}/leapmux.v1.AuthService/Login`,
      method: 'AuthService/Login',
      headers: { 'Content-Type': 'application/json' },
      body: { username: 'alice', password: 'old-password', captchaPayload: 'solved-captcha', honeypot: '' },
      redirect: 'manual',
    }])
  })
})

describe('loginViaAPI', () => {
  it('returns the session cookie of an accepted sign-in', async () => {
    recordedRequests(() => new Response('{}', { headers: { 'Set-Cookie': 'leapmux-session=abc; Path=/; HttpOnly' } }))
    await expect(loginViaAPI(hubUrl, 'alice', 'secret')).resolves.toBe('leapmux-session=abc')
  })

  it('states the hub reason of a refused sign-in', async () => {
    recordedRequests(() => new Response('{"code":"unauthenticated","message":"invalid credentials"}', { status: 401 }))
    await expect(loginViaAPI(hubUrl, 'alice', 'wrong')).rejects.toThrow('loginViaAPI failed: AuthService/Login returned HTTP 401: {"code":"unauthenticated","message":"invalid credentials"}')
  })
})

describe('freshAdminSessionViaAPI', () => {
  it('signs in as the test administrator and elevates nothing', async () => {
    const requests = recordedRequests(() => new Response('{}', { headers: { 'Set-Cookie': 'leapmux-session=fresh; Path=/' } }))
    await expect(freshAdminSessionViaAPI(hubUrl)).resolves.toBe('leapmux-session=fresh')
    expect(requests.map(request => [request.method, request.body])).toEqual([
      ['AuthService/Login', { username: TEST_ADMIN_USERNAME, password: TEST_ADMIN_PASSWORD, captchaPayload: 'solved-captcha', honeypot: '' }],
    ])
  })
})

describe('elevatedAdminSessionViaAPI', () => {
  it('signs in as the test administrator, then elevates that new session with the password', async () => {
    const requests = recordedRequests(method => method === 'AuthService/Login'
      ? new Response('{}', { headers: { 'Set-Cookie': 'leapmux-session=own; Path=/' } })
      : Response.json({}))
    await expect(elevatedAdminSessionViaAPI(hubUrl)).resolves.toBe('leapmux-session=own')
    expect(requests.map(request => [request.method, request.headers.Cookie, request.body])).toEqual([
      ['AuthService/Login', undefined, { username: TEST_ADMIN_USERNAME, password: TEST_ADMIN_PASSWORD, captchaPayload: 'solved-captcha', honeypot: '' }],
      ['UserService/ElevateSession', 'leapmux-session=own', { currentPassword: TEST_ADMIN_PASSWORD }],
    ])
  })

  it('returns no session when the elevation is refused', async () => {
    recordedRequests(method => method === 'AuthService/Login'
      ? new Response('{}', { headers: { 'Set-Cookie': 'leapmux-session=own; Path=/' } })
      : new Response('{"code":"permission_denied"}', { status: 403 }))
    await expect(elevatedAdminSessionViaAPI(hubUrl)).rejects.toThrow('elevateSessionViaAPI failed')
  })
})

describe('logoutViaAPI', () => {
  it('ends the session of the cookie', async () => {
    const requests = recordedRequests(() => Response.json({}))
    await logoutViaAPI(hubUrl, 'leapmux-session=s2')
    expect(requests.map(request => [request.method, request.headers.Cookie])).toEqual([['AuthService/Logout', 'leapmux-session=s2']])
  })

  it('throws for a refused logout', async () => {
    recordedRequests(() => new Response('gone', { status: 500 }))
    await expect(logoutViaAPI(hubUrl, 'leapmux-session=s2')).rejects.toThrow('logoutViaAPI failed: AuthService/Logout returned HTTP 500: gone')
  })
})

describe('listWorkersViaAPI', () => {
  it('reads each worker with its online state, and an absent state as offline', async () => {
    recordedRequests(() => Response.json({ workers: [{ id: 'w-1', online: true }, { id: 'w-2' }] }))
    await expect(listWorkersViaAPI(hubUrl, 'c')).resolves.toEqual([{ id: 'w-1', online: true }, { id: 'w-2', online: false }])
  })

  it('reads an absent workers field as no worker', async () => {
    recordedRequests(() => Response.json({}))
    await expect(listWorkersViaAPI(hubUrl, 'c')).resolves.toEqual([])
  })

  it('refuses a worker with no ID', async () => {
    recordedRequests(() => Response.json({ workers: [{ online: true }] }))
    await expect(listWorkersViaAPI(hubUrl, 'c')).rejects.toThrow('a worker with no ID')
  })
})

describe('listOnlineWorkerIDsViaAPI', () => {
  it('returns only the online workers', async () => {
    recordedRequests(() => Response.json({ workers: [{ id: 'on', online: true }, { id: 'off', online: false }] }))
    await expect(listOnlineWorkerIDsViaAPI(hubUrl, 'c')).resolves.toEqual(['on'])
  })
})

describe('getWorkerId', () => {
  it('waits until the first worker is online and returns its ID', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn<() => Promise<Response>>()
      .mockResolvedValueOnce(Response.json({ workers: [{ id: 'w-1', online: false }] }))
      .mockImplementation(async () => Response.json({ workers: [{ id: 'w-1', online: true }] }))
    vi.stubGlobal('fetch', fetch)
    const waiting = getWorkerId(hubUrl, 'c')
    await vi.advanceTimersByTimeAsync(API_POLL_INTERVAL_MS)
    await expect(waiting).resolves.toBe('w-1')
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('throws a refused read at once', async () => {
    recordedRequests(() => new Response('admin only', { status: 403 }))
    await expect(getWorkerId(hubUrl, 'c')).rejects.toThrow('listWorkersViaAPI failed: WorkerManagementService/ListWorkers returned HTTP 403: admin only')
  })
})

describe('configureBrokenSmtpViaAPI', () => {
  it('writes an SMTP relay on loopback port 1, where nothing listens', async () => {
    const requests = recordedRequests(() => Response.json({}))
    await configureBrokenSmtpViaAPI(hubUrl, 'admin-cookie')
    expect(requests).toHaveLength(1)
    expect(requests[0]?.method).toBe('AdminSettingsService/UpdateSetting')
    const body = requests[0]?.body as { key: string, partialJson: string }
    expect(body.key).toBe('smtp')
    expect(JSON.parse(body.partialJson)).toEqual({ host: '127.0.0.1', port: 1, from_address: 'hub@test.local', tls_mode: 'none' })
  })
})

describe('deletePasskeyViaAPI', () => {
  it('states the passkey and the hub reason of a refused delete', async () => {
    recordedRequests(() => new Response('{"code":"failed_precondition"}', { status: 400 }))
    await expect(deletePasskeyViaAPI(hubUrl, 'c', 'pk-1')).rejects.toThrow('deletePasskeyViaAPI(pk-1) failed: UserService/DeletePasskey returned HTTP 400: {"code":"failed_precondition"}')
  })
})

describe('sqliteTextLiteral', () => {
  it('quotes a plain value', () => {
    expect(sqliteTextLiteral('alice')).toBe('\'alice\'')
  })

  it('doubles each quote character, so the value cannot end the literal', () => {
    expect(sqliteTextLiteral('o\'brien\'; DROP TABLE users; --')).toBe('\'o\'\'brien\'\'; DROP TABLE users; --\'')
  })

  it('quotes an empty value', () => {
    expect(sqliteTextLiteral('')).toBe('\'\'')
  })

  it('refuses a NUL character, which the sqlite3 command line cannot carry', () => {
    expect(() => sqliteTextLiteral('a\0b')).toThrow('NUL')
  })
})
