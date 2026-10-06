import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/proto/leapmux/v1/common_pb'
import { callHub, getTestChannel } from './api'
import {
  agentStatusViaAPI,
  closeAgentViaAPI,
  closeNativeAgentAndWait,
  terminalExitedViaAPI,
  waitForAgentStartupViaAPI,
  waitForAgentStatusViaAPI,
  waitForAgentsViaAPI,
  waitForSoleAgentViaAPI,
  waitForTerminalExitViaAPI,
  waitForWorkerTabTitle,
} from './workerTabs'

vi.mock('./api', () => ({
  API_POLL_INTERVAL_MS: 1,
  callHub: vi.fn(),
  getTestChannel: vi.fn(),
}))
// A short deadline, so a wait that never succeeds reports its failure within the unit test.
vi.mock('./testDeadline', () => ({ waitTimeoutBeforeTestDeadline: () => 400 }))

const server = { hubUrl: 'http://hub.test', adminToken: 'session', workerId: 'worker' }
const callWorker = vi.fn()

/** Answer ListTabs with `tabs`, and each Worker list call with `answer(method)`. */
function workspace(tabs: Array<{ tabType: string, tabId: string }>, answer: (method: string) => unknown) {
  vi.mocked(callHub).mockResolvedValue({ tabs })
  callWorker.mockImplementation(async (_workerId: string, method: string) => answer(method))
}

beforeEach(() => {
  vi.mocked(callHub).mockReset()
  callWorker.mockReset()
  vi.mocked(getTestChannel).mockReset().mockResolvedValue({ callWorker } as unknown as Awaited<ReturnType<typeof getTestChannel>>)
})

describe('agentStatusViaAPI', () => {
  it('returns the status of the agent that the Worker lists', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }, { tabType: 'TAB_TYPE_AGENT', tabId: 'a-2' }], () => ({
      agents: [{ id: 'a-1', status: AgentStatus.ACTIVE }, { id: 'a-2', status: AgentStatus.INACTIVE }],
    }))
    await expect(agentStatusViaAPI(server, 'ws', 'a-2')).resolves.toBe(AgentStatus.INACTIVE)
    expect(callWorker).toHaveBeenCalledWith('worker', 'ListAgents', expect.anything(), expect.anything(), { tabIds: ['a-1', 'a-2'] })
  })

  it('returns undefined while the Worker lists no such agent', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }], () => ({ agents: [{ id: 'a-1', status: AgentStatus.ACTIVE }] }))
    await expect(agentStatusViaAPI(server, 'ws', 'absent')).resolves.toBeUndefined()
  })

  it('returns undefined for a workspace with no agent tab, with no Worker call', async () => {
    workspace([{ tabType: 'TAB_TYPE_TERMINAL', tabId: 't-1' }], () => {
      throw new Error('The Worker must not be asked.')
    })
    await expect(agentStatusViaAPI(server, 'ws', 'a-1')).resolves.toBeUndefined()
    expect(callWorker).not.toHaveBeenCalled()
  })
})

describe('terminalExitedViaAPI', () => {
  it('reads the exited flag of the terminal that the Worker lists', async () => {
    workspace([{ tabType: 'TAB_TYPE_TERMINAL', tabId: 't-1' }], () => ({
      terminals: [{ terminalId: 't-1', title: 'Terminal', status: 0, exited: true }],
    }))
    await expect(terminalExitedViaAPI(server, 'ws', 't-1')).resolves.toBe(true)
  })

  it('returns undefined while the Worker lists no such terminal', async () => {
    workspace([{ tabType: 'TAB_TYPE_TERMINAL', tabId: 't-1' }], () => ({ terminals: [] }))
    await expect(terminalExitedViaAPI(server, 'ws', 't-1')).resolves.toBeUndefined()
  })
})

describe('waitForSoleAgentViaAPI', () => {
  it('returns the one agent of the workspace', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }], () => ({
      agents: [{ id: 'a-1', title: 'Agent', workingDir: '/repo', status: AgentStatus.ACTIVE, startupError: '' }],
    }))
    await expect(waitForSoleAgentViaAPI(server, 'ws')).resolves.toMatchObject({ id: 'a-1', workingDir: '/repo' })
  })

  it('refuses a workspace with two agents, because "the" agent would be an arbitrary one', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }, { tabType: 'TAB_TYPE_AGENT', tabId: 'a-2' }], () => ({
      agents: [{ id: 'a-1', status: AgentStatus.ACTIVE }, { id: 'a-2', status: AgentStatus.ACTIVE }],
    }))
    await expect(waitForSoleAgentViaAPI(server, 'ws')).rejects.toThrow('must hold exactly one agent, but its Worker lists 2: a-1, a-2')
  })

  it('reads again after a Hub read that throws while the Hub restarts', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }], () => ({
      agents: [{ id: 'a-1', title: 'Agent', workingDir: '/repo', status: AgentStatus.ACTIVE, startupError: '' }],
    }))
    vi.mocked(callHub).mockRejectedValueOnce(new Error('fetch failed'))
    await expect(waitForSoleAgentViaAPI(server, 'ws')).resolves.toMatchObject({ id: 'a-1' })
    expect(callHub).toHaveBeenCalledTimes(2)
  })

  it('states the last failed read when no agent appears', async () => {
    vi.mocked(callHub).mockRejectedValue(new Error('fetch failed'))
    await expect(waitForAgentsViaAPI(server.hubUrl, server.adminToken, server.workerId, 'ws', 50)).rejects.toThrow('the last read that failed: fetch failed')
  })
})

describe('waitForAgentStartupViaAPI', () => {
  it('reads again after a Hub read that throws, and returns the started agents', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }], () => ({
      agents: [{ id: 'a-1', title: 'Agent', workingDir: '/repo', status: AgentStatus.ACTIVE, startupError: '' }],
    }))
    vi.mocked(callHub).mockRejectedValueOnce(new Error('fetch failed'))
    await expect(waitForAgentStartupViaAPI(server.hubUrl, server.adminToken, server.workerId, 'ws')).resolves.toHaveLength(1)
  })
})

describe('waitForAgentStatusViaAPI', () => {
  it('reads again after a read that throws and a read with another status', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }], () => ({ agents: [{ id: 'a-1', status: AgentStatus.ACTIVE }] }))
    vi.mocked(callHub).mockRejectedValueOnce(new Error('fetch failed'))
    callWorker.mockResolvedValueOnce({ agents: [{ id: 'a-1', status: AgentStatus.STARTING }] })
    await expect(waitForAgentStatusViaAPI(server, 'ws', 'a-1', AgentStatus.ACTIVE)).resolves.toBeUndefined()
    expect(callHub).toHaveBeenCalledTimes(3)
  })

  it('fails with the status that the Worker reports when it never reaches the expected one', async () => {
    workspace([{ tabType: 'TAB_TYPE_AGENT', tabId: 'a-1' }], () => ({ agents: [{ id: 'a-1', status: AgentStatus.INACTIVE }] }))
    await expect(waitForAgentStatusViaAPI(server, 'ws', 'a-1', AgentStatus.ACTIVE)).rejects.toThrow('the Worker reports agent a-1 as ACTIVE')
  })
})

describe('waitForTerminalExitViaAPI', () => {
  it('reads again after a read that throws, and ends when the Worker reports the exit', async () => {
    workspace([{ tabType: 'TAB_TYPE_TERMINAL', tabId: 't-1' }], () => ({ terminals: [{ terminalId: 't-1', title: 'Terminal', status: 0, exited: true }] }))
    vi.mocked(callHub).mockRejectedValueOnce(new Error('fetch failed'))
    await expect(waitForTerminalExitViaAPI(server, 'ws', 't-1')).resolves.toBeUndefined()
    expect(callHub).toHaveBeenCalledTimes(2)
  })

  it('fails while the Worker reports the terminal as running', async () => {
    workspace([{ tabType: 'TAB_TYPE_TERMINAL', tabId: 't-1' }], () => ({ terminals: [{ terminalId: 't-1', title: 'Terminal', status: 0, exited: false }] }))
    await expect(waitForTerminalExitViaAPI(server, 'ws', 't-1')).rejects.toThrow('the Worker reports terminal t-1 as exited')
  })
})

describe('waitForWorkerTabTitle', () => {
  it('waits until the list holds the title', async () => {
    const list = vi.fn<() => Promise<Array<{ title: string }>>>()
      .mockResolvedValueOnce([{ title: 'Terminal Orca' }])
      .mockResolvedValue([{ title: 'Terminal Orca' }, { title: 'renamed' }])
    await waitForWorkerTabTitle(list, 'renamed', 'the rename reaches the Worker')
    expect(list.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('reads again after a failed read', async () => {
    const list = vi.fn<() => Promise<Array<{ title: string }>>>()
      .mockRejectedValueOnce(new Error('The Worker channel is not open.'))
      .mockResolvedValue([{ title: 'renamed' }])
    await waitForWorkerTabTitle(list, 'renamed', 'the rename reaches the Worker')
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('reports the titles of the last read when the title never arrives', async () => {
    const list = vi.fn(async () => [{ title: 'Terminal Orca' }])
    await expect(waitForWorkerTabTitle(list, 'renamed', 'the rename reaches the Worker')).rejects.toThrow(/the rename reaches the Worker[\s\S]*Terminal Orca/)
  })

  it('reports the last failed read when every read fails', async () => {
    const list = vi.fn(async (): Promise<Array<{ title: string }>> => {
      throw new Error('The Worker channel is not open.')
    })
    await expect(waitForWorkerTabTitle(list, 'renamed', 'the rename reaches the Worker')).rejects.toThrow('The Worker channel is not open.')
    expect(list.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('refuses an empty title before it reads the list', async () => {
    const list = vi.fn(async () => [{ title: '' }])
    await expect(waitForWorkerTabTitle(list, '', 'any')).rejects.toThrow('needs a title')
    expect(list).not.toHaveBeenCalled()
  })
})

describe('closeAgentViaAPI', () => {
  it('sends KEEP by default and returns the Worker verdict', async () => {
    callWorker.mockResolvedValue({ result: { worktreePath: '/work/tree', worktreeId: 'wt-1', failureMessage: '', failureDetail: '' } })
    await expect(closeAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, 'a-1'))
      .resolves
      .toEqual({ worktreePath: '/work/tree', worktreeId: 'wt-1', failureMessage: '', failureDetail: '' })
    expect(callWorker).toHaveBeenCalledWith('worker', 'CloseAgent', expect.anything(), expect.anything(), { agentId: 'a-1', worktreeAction: WorktreeAction.KEEP })
  })

  it('refuses a response that states no verdict', async () => {
    callWorker.mockResolvedValue({})
    await expect(closeAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, 'a-1')).rejects.toThrow('The Worker sent no close verdict for agent a-1.')
  })
})

describe('closeNativeAgentAndWait', () => {
  const context = { leapmuxServer: server } as unknown as Parameters<typeof closeNativeAgentAndWait>[0]
  const verdict = { result: { worktreePath: '', worktreeId: '', failureMessage: '', failureDetail: '' } }

  it('closes the agent and waits until the Worker no longer lists it', async () => {
    const listed = vi.fn()
      .mockResolvedValueOnce({ agents: [{ id: 'a-1', status: AgentStatus.INACTIVE }] })
      .mockResolvedValue({ agents: [] })
    callWorker.mockImplementation(async (_workerId: string, method: string) => method === 'CloseAgent' ? verdict : listed())
    await closeNativeAgentAndWait(context, 'a-1')
    expect(callWorker).toHaveBeenNthCalledWith(1, 'worker', 'CloseAgent', expect.anything(), expect.anything(), { agentId: 'a-1', worktreeAction: WorktreeAction.KEEP })
    expect(callWorker).toHaveBeenCalledWith('worker', 'ListAgents', expect.anything(), expect.anything(), { tabIds: ['a-1'] })
    expect(listed).toHaveBeenCalledTimes(2)
  })

  it('reads again after a Worker read that fails', async () => {
    const listed = vi.fn()
      .mockRejectedValueOnce(new Error('The Worker channel reconnects.'))
      .mockResolvedValue({ agents: [] })
    callWorker.mockImplementation(async (_workerId: string, method: string) => method === 'CloseAgent' ? verdict : listed())
    await closeNativeAgentAndWait(context, 'a-1')
    expect(listed).toHaveBeenCalledTimes(2)
  })

  it('throws the Worker refusal with its message and detail, and does not wait', async () => {
    callWorker.mockResolvedValue({ result: { worktreePath: '', worktreeId: '', failureMessage: 'Failed to close agent', failureDetail: 'database is locked' } })
    await expect(closeNativeAgentAndWait(context, 'a-1')).rejects.toThrow('The Worker refused to close agent a-1: Failed to close agent (database is locked)')
    expect(callWorker).toHaveBeenCalledOnce()
  })

  it('reports the agent that the Worker still lists when the deadline ends', async () => {
    callWorker.mockImplementation(async (_workerId: string, method: string) => method === 'CloseAgent' ? verdict : { agents: [{ id: 'a-1', status: AgentStatus.ACTIVE }] })
    await expect(closeNativeAgentAndWait(context, 'a-1')).rejects.toThrow('The Worker still lists the closed agent a-1.')
  })

  it('refuses an empty agent ID before it calls the Worker', async () => {
    await expect(closeNativeAgentAndWait(context, '')).rejects.toThrow('requires an agent ID')
    expect(callWorker).not.toHaveBeenCalled()
  })
})
