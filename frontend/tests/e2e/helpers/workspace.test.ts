import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createProcessStub } from '~/test-support/childProcess'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './api'
import { createTestDirectory } from './runDirectory'
import { withAgentWorkspace, withTestWorkspace } from './workspace'

vi.mock('./api', () => ({ createWorkspaceViaAPI: vi.fn(), deleteWorkspaceViaAPI: vi.fn(), openAgentViaAPI: vi.fn() }))
vi.mock('./runDirectory', () => ({ createTestDirectory: vi.fn(() => '/private-directory') }))

const server = { hubUrl: 'http://hub.test', adminToken: 'session', workerId: 'worker' }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(createWorkspaceViaAPI).mockReset().mockResolvedValue('workspace')
  vi.mocked(deleteWorkspaceViaAPI).mockReset().mockResolvedValue(undefined)
  vi.mocked(openAgentViaAPI).mockReset().mockResolvedValue('agent')
})

describe('workspace fixture lifetime', () => {
  it('deletes the workspace after the fixture finishes', async () => {
    const order: string[] = []
    vi.mocked(deleteWorkspaceViaAPI).mockImplementation(async () => {
      order.push('delete')
    })
    await withTestWorkspace(server, 'test', async () => {
      order.push('use')
    })
    expect(order).toEqual(['use', 'delete'])
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledExactlyOnceWith(server.hubUrl, server.adminToken, 'workspace')
  })

  it('deletes the workspace when its setup callback fails', async () => {
    const error = new Error('setup failed')
    await expect(withTestWorkspace(server, 'test', async () => {
      throw error
    })).rejects.toBe(error)
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })

  it('does not hide an operation failure when the hub stops', async () => {
    const hub = createProcessStub()
    const error = new Error('operation failed')
    await expect(withTestWorkspace({ ...server, hubProc: hub.proc }, 'test', async () => {
      hub.emitter.exitCode = 0
      throw error
    })).rejects.toBe(error)
    expect(deleteWorkspaceViaAPI).not.toHaveBeenCalled()
  })

  it('does not run setup or deletion when creation fails', async () => {
    const error = new Error('create failed')
    vi.mocked(createWorkspaceViaAPI).mockRejectedValueOnce(error)
    const use = vi.fn()
    await expect(withTestWorkspace(server, 'test', use)).rejects.toBe(error)
    expect(use).not.toHaveBeenCalled()
    expect(deleteWorkspaceViaAPI).not.toHaveBeenCalled()
  })

  it('opens the selected provider in a private directory before fixture use', async () => {
    const order: string[] = []
    vi.mocked(openAgentViaAPI).mockImplementation(async () => {
      order.push('open')
      return 'agent'
    })
    vi.mocked(deleteWorkspaceViaAPI).mockImplementation(async () => {
      order.push('delete')
    })
    await withAgentWorkspace(server, { provider: AgentProvider.CODEX, prefix: 'codex' }, async () => {
      order.push('use')
    })
    expect(order).toEqual(['open', 'use', 'delete'])
    expect(createTestDirectory).toHaveBeenCalledExactlyOnceWith('codex-wd-')
    expect(openAgentViaAPI).toHaveBeenCalledWith(server.hubUrl, server.adminToken, server.workerId, 'workspace', '/private-directory', expect.objectContaining({ agentProvider: AgentProvider.CODEX }))
  })

  it('opens the agent in the working directory that the caller creates, and makes no private one', async () => {
    const workingDir = vi.fn(() => '/repository/checkout')
    await withAgentWorkspace(server, { provider: AgentProvider.KIRO, prefix: 'kiro', workingDir }, async () => {})
    expect(workingDir).toHaveBeenCalledOnce()
    expect(createTestDirectory).not.toHaveBeenCalled()
    expect(openAgentViaAPI).toHaveBeenCalledWith(server.hubUrl, server.adminToken, server.workerId, 'workspace', '/repository/checkout', expect.objectContaining({ agentProvider: AgentProvider.KIRO }))
  })

  // The directory belongs to the workspace, so the workspace exists first and the
  // cleanup still deletes it when the directory cannot be made.
  it('creates the working directory after the workspace, and deletes the workspace when that fails', async () => {
    const error = new Error('no checkout')
    const use = vi.fn()
    await expect(withAgentWorkspace(server, {
      provider: AgentProvider.KIRO,
      prefix: 'kiro',
      workingDir: () => {
        expect(createWorkspaceViaAPI).toHaveBeenCalledOnce()
        throw error
      },
    }, use)).rejects.toBe(error)
    expect(use).not.toHaveBeenCalled()
    expect(openAgentViaAPI).not.toHaveBeenCalled()
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })

  it('deletes a workspace after its agent fails to open', async () => {
    const error = new Error('open failed')
    vi.mocked(openAgentViaAPI).mockRejectedValueOnce(error)
    const use = vi.fn()
    await expect(withAgentWorkspace(server, { provider: AgentProvider.CODEX, prefix: 'codex' }, use)).rejects.toBe(error)
    expect(use).not.toHaveBeenCalled()
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })
})
