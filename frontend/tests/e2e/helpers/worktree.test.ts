import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { callHub, getTestChannel } from './api'
import {
  addWorktree,
  agentStatusViaAPI,
  branchExists,
  commitFile,
  createGitRepo,
  createGitRepoWithRemote,
  initGitRepo,
  managedWorktreePath,
  terminalExitedViaAPI,
  waitForSoleAgentViaAPI,
  waitForWorkerTabTitle,
} from './worktree'

vi.mock('./api', () => ({
  API_POLL_INTERVAL_MS: 1,
  callHub: vi.fn(),
  createWorkspaceViaAPI: vi.fn(),
  getTestChannel: vi.fn(),
  openAgentViaAPI: vi.fn(),
}))
vi.mock('./ui', () => ({
  activeWorkspaceRow: vi.fn(),
  expectAnyVisible: vi.fn(),
  isMaybeVisible: vi.fn(),
  loginViaToken: vi.fn(),
  sidebarSectionHeader: vi.fn(),
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
})

/** Run git in `cwd` and return its trimmed output. */
function gitOutput(cwd: string, args: string[]): string {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim()
}

describe('git repository helpers', () => {
  let root: string

  beforeEach(() => {
    const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    root = mkdtempSync(join(scratch, 'worktree-helpers-'))
  })

  afterEach(() => {
    rmSync(root, { recursive: true, force: true })
  })

  it('initGitRepo pins every background writer off, sets an identity, and starts on main', () => {
    const dir = join(root, 'pinned')
    initGitRepo(dir)
    expect(gitOutput(dir, ['config', '--local', 'core.fsmonitor'])).toBe('false')
    expect(gitOutput(dir, ['config', '--local', 'gc.auto'])).toBe('0')
    expect(gitOutput(dir, ['config', '--local', 'maintenance.auto'])).toBe('false')
    expect(gitOutput(dir, ['config', '--local', 'user.email'])).toBe('test@test.com')
    expect(gitOutput(dir, ['config', '--local', 'user.name'])).toBe('Test')
    expect(gitOutput(dir, ['symbolic-ref', '--short', 'HEAD'])).toBe('main')
  })

  it('createGitRepo commits the README on main', () => {
    const dir = createGitRepo(root, 'repo')
    expect(dir).toBe(join(root, 'repo'))
    expect(gitOutput(dir, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('main')
    expect(gitOutput(dir, ['log', '--pretty=%s'])).toBe('init')
    expect(gitOutput(dir, ['status', '--porcelain'])).toBe('')
  })

  it('commitFile creates the parent directories and commits only that file', () => {
    const dir = createGitRepo(root, 'repo')
    commitFile(dir, 'pkg/deep/tracked.txt', 'hello\n', 'add pkg')
    expect(readFileSync(join(dir, 'pkg/deep/tracked.txt'), 'utf8')).toBe('hello\n')
    expect(gitOutput(dir, ['log', '-1', '--pretty=%s'])).toBe('add pkg')
    expect(gitOutput(dir, ['show', '--name-only', '--pretty=', 'HEAD'])).toBe('pkg/deep/tracked.txt')
  })

  it('addWorktree adds a linked worktree on a new branch and returns its real path', () => {
    const dir = createGitRepo(root, 'repo')
    const worktree = addWorktree(dir, root, 'nested/wt', 'wt-branch')
    expect(worktree).toBe(realpathSync(join(root, 'nested/wt')))
    expect(gitOutput(worktree, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('wt-branch')
    expect(branchExists(dir, 'wt-branch')).toBe(true)
  })

  it('branchExists answers for an absent branch and passes a name that looks like a flag to git as a name', () => {
    const dir = createGitRepo(root, 'repo')
    expect(branchExists(dir, 'absent-branch')).toBe(false)
    expect(branchExists(dir, '--all')).toBe(false)
  })

  it('createGitRepoWithRemote gives the clone an upstream on main that the remote holds', () => {
    const { repoDir, bareDir } = createGitRepoWithRemote(root, 'cloned')
    expect(repoDir).toBe(join(root, 'cloned'))
    expect(bareDir).toBe(join(root, 'cloned-bare'))
    expect(gitOutput(repoDir, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('main')
    expect(gitOutput(repoDir, ['rev-parse', '--abbrev-ref', '@{upstream}'])).toBe('origin/main')
    expect(gitOutput(bareDir, ['log', '-1', '--pretty=%s', 'main'])).toBe('init')
    expect(gitOutput(repoDir, ['config', '--local', 'core.fsmonitor'])).toBe('false')
  })

  it('managedWorktreePath places a worktree beside the MAIN repository, also from a linked worktree', () => {
    const dir = createGitRepo(root, 'repo')
    const expected = join(realpathSync(root), 'repo-worktrees', 'feature')
    expect(managedWorktreePath(dir, 'feature')).toBe(expected)
    const linked = addWorktree(dir, root, 'elsewhere/linked', 'linked-branch')
    expect(managedWorktreePath(linked, 'feature')).toBe(expected)
    expect(existsSync(expected)).toBe(false)
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
