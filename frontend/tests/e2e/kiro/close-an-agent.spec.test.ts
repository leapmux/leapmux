import type { ProcessRow } from '../helpers/processTree'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { resolveNativeProcessOwnership } from '../helpers/nativeProcessOwnership'

const calls = vi.hoisted(() => ({
  registered: new Map<string, unknown>(),
  processes: vi.fn(),
  close: vi.fn(),
  exercise: vi.fn<typeof import('../helpers/nativeLifecycle').exerciseCloseAgent>(),
  ended: new Set<number>(),
}))

vi.mock('../kiro-fixtures', () => ({
  openKiroAgent: vi.fn(async () => ({ agentId: 'selected-agent', workingDir: '/private/shard-1/project' })),
  kiroTest: Object.assign((title: string, body: unknown) => calls.registered.set(title, body), {
    describe: (_title: string, body: () => void) => body(),
  }),
}))
vi.mock('../helpers/ui', () => ({
  openWorkspace: vi.fn(async () => {}),
  sendMessage: vi.fn(async () => {}),
  waitForAgentIdle: vi.fn(async () => {}),
}))
vi.mock('../helpers/worktree', () => ({ closeAgentViaAPI: calls.close }))
vi.mock('../helpers/nativeLifecycle', () => ({ exerciseCloseAgent: calls.exercise }))
vi.mock('../helpers/processTree', async original => ({
  ...await original<typeof import('../helpers/processTree')>(),
  listProcesses: calls.processes,
  isAlive: (pid: number) => !calls.ended.has(pid),
}))
vi.mock('@playwright/test', async original => ({
  ...await original<typeof import('@playwright/test')>(),
  expect: Object.assign((value: unknown, message?: string) => expect(value, message), {
    poll: (read: () => unknown | Promise<unknown>, options?: { message?: string }) => ({
      toEqual: async (expected: unknown) => expect(await read(), options?.message).toEqual(expected),
    }),
  }),
}))

const WORKER = '/private/shard-1/leapmux'
const OTHER_WORKER = '/private/shard-2/leapmux'
const OWNED_PIDS = [20, 21, 22, 30]
const OTHER_PIDS = [50, 60, 70, 80]
let rows: ProcessRow[]

beforeAll(async () => {
  await import('./close-an-agent.spec')
})

beforeEach(() => {
  vi.clearAllMocks()
  calls.ended.clear()
  rows = [
    { pid: 10, ppid: 1, command: `${WORKER} dev` },
    { pid: 20, ppid: 10, command: '/bin/zsh native-provider-wrapper' },
    { pid: 21, ppid: 20, command: '/installed/kiro-cli-chat acp' },
    { pid: 22, ppid: 21, command: '/installed/node /private/shard-1/kiro-data/kas/version/node_modules/@kiro/agent/dist/index.js' },
    { pid: 30, ppid: 22, command: '/installed/node selected-held-tool' },
    { pid: 50, ppid: 1, command: `${OTHER_WORKER} dev` },
    { pid: 60, ppid: 50, command: '/installed/kiro-cli-chat acp' },
    { pid: 70, ppid: 60, command: '/installed/node /private/shard-2/kiro-data/kas/version/node_modules/@kiro/agent/dist/index.js' },
    { pid: 80, ppid: 70, command: '/installed/node other-held-tool' },
  ]
  calls.processes.mockImplementationOnce(() => rows.filter(row => [10, 50].includes(row.pid)))
    .mockImplementation(() => rows.filter(row => !calls.ended.has(row.pid)))
  calls.close.mockImplementation(async () => {
    for (const pid of OWNED_PIDS)
      calls.ended.add(pid)
    return { failureMessage: '' }
  })
  calls.exercise.mockImplementation(async (context, options) => {
    const ownership = resolveNativeProcessOwnership(rows, 30, WORKER)
    await options?.nativeOwnership?.({ rows, toolPid: 30, ownership })
    await calls.close(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken, context.leapmuxServer.workerId, 'selected-agent')
    expect(ownership.ownedPids.filter(pid => !calls.ended.has(pid))).toEqual([])
    expect(calls.ended.has(ownership.workerPid)).toBe(false)
  })
})

async function runActualCloseCase(): Promise<void> {
  const run = calls.registered.get('stops the whole process tree when the agent closes')
  if (typeof run !== 'function')
    throw new Error('The retained Kiro close callback is absent.')
  await run({
    page: {},
    authenticatedEmptyWorkspace: { workspaceId: 'selected-workspace' },
    leapmuxServer: { hubUrl: 'http://localhost:30001', adminToken: 'private-token', workerId: 'selected-worker', agentEnv: { KIRO_DATA_DIR: '/private/shard-1/kiro-data' } },
    modelScript: {
      queue: vi.fn(async () => {}),
      prompt: (text: string) => text,
      waitForSteps: vi.fn(async () => {}),
    },
  })
}

describe('kiro close process ownership', () => {
  it('closes its own relay and engine while another shard keeps its Kiro tree alive', async () => {
    await runActualCloseCase()
    expect([...calls.ended]).toEqual(OWNED_PIDS)
    for (const pid of OTHER_PIDS)
      expect(calls.ended.has(pid)).toBe(false)
    expect(calls.ended.has(10)).toBe(false)
  })

  it('refuses a missing owned relay even when another shard has a relay', async () => {
    rows = rows.map(row => row.pid === 21 ? { ...row, command: '/installed/unrelated-driver' } : row)
    await expect(runActualCloseCase()).rejects.toThrow(/relay/)
    expect(calls.close).not.toHaveBeenCalled()
  })

  it('refuses a missing owned engine even when another shard has an engine', async () => {
    rows = rows.map(row => row.pid === 22 ? { ...row, command: '/installed/unrelated-helper' } : row)
    await expect(runActualCloseCase()).rejects.toThrow(/engine/)
    expect(calls.close).not.toHaveBeenCalled()
  })

  it('refuses a late reparented engine from its own private KAS directory', async () => {
    rows = rows.filter(row => !OTHER_PIDS.includes(row.pid))
    calls.close.mockImplementationOnce(async () => {
      for (const pid of OWNED_PIDS)
        calls.ended.add(pid)
      rows.push({ pid: 99, ppid: 1, command: '/installed/node /private/shard-1/kiro-data/kas/version/node_modules/@kiro/agent/dist/index.js' })
      return { failureMessage: '' }
    })
    await expect(runActualCloseCase()).rejects.toThrow(/Kiro/)
  })
})
