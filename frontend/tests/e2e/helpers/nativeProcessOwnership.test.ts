import { describe, expect, it } from 'vitest'
import { resolveNativeProcessOwnership } from './nativeProcessOwnership'

const WORKER = '/project/.tmp/bin/leapmux'
const PROCESS_TREE = [
  { pid: 10, ppid: 1, command: `${WORKER} worker` },
  { pid: 20, ppid: 10, command: '/native/provider runtime' },
  { pid: 21, ppid: 20, command: '/bin/sh held-tool' },
  { pid: 22, ppid: 20, command: '/native/mcp-server' },
  { pid: 30, ppid: 21, command: '/native/node held-script' },
  { pid: 31, ppid: 22, command: '/native/server-helper' },
  { pid: 40, ppid: 10, command: '/native/unrelated-provider' },
]

describe('resolveNativeProcessOwnership', () => {
  it('includes the full provider subtree and excludes the Worker and its other agent', () => {
    const result = resolveNativeProcessOwnership(PROCESS_TREE, 30, WORKER)
    expect(result.workerPid).toBe(10)
    expect(result.rootPid).toBe(20)
    expect([...result.ownedPids].sort((a, b) => a - b)).toEqual([20, 21, 22, 30, 31])
    expect(result.ownedPids).not.toContain(10)
    expect(result.ownedPids).not.toContain(40)
  })

  it('refuses an unresolved Worker rather than returning a tool-only fallback', () => {
    expect(() => resolveNativeProcessOwnership(PROCESS_TREE.filter(row => row.pid !== 10), 30, WORKER)).toThrow(/Worker|boundary/)
  })

  it('refuses a missing tool process row', () => {
    expect(() => resolveNativeProcessOwnership(PROCESS_TREE, 999, WORKER)).toThrow(/process|Worker|boundary/)
  })

  it('refuses a parent cycle', () => {
    const rows = [{ pid: 20, ppid: 30, command: 'provider' }, { pid: 30, ppid: 20, command: 'tool' }]
    expect(() => resolveNativeProcessOwnership(rows, 30, WORKER)).toThrow(/cycle|boundary|Worker/)
  })

  it('matches the exact Worker executable when its path contains spaces', () => {
    const executable = '/project with spaces/.tmp/bin/leapmux'
    const rows = [{ pid: 10, ppid: 1, command: `${executable} worker` }, { pid: 20, ppid: 10, command: 'native-provider' }]
    expect(resolveNativeProcessOwnership(rows, 20, executable)).toEqual({ workerPid: 10, rootPid: 20, ownedPids: [20] })
  })

  it('does not treat an unrelated executable with the same basename as the Worker', () => {
    const rows = [{ pid: 10, ppid: 1, command: '/unrelated/leapmux' }, { pid: 20, ppid: 10, command: 'native-provider' }]
    expect(() => resolveNativeProcessOwnership(rows, 20, WORKER)).toThrow(/Worker|boundary/)
  })

  it('uses the exact Windows executable metadata', () => {
    const executable = 'C:\\project with spaces\\leapmux.exe'
    const rows = [
      { pid: 10, ppid: 1, command: 'unrelated argv text', executable },
      { pid: 20, ppid: 10, command: 'native-provider' },
    ]
    expect(resolveNativeProcessOwnership(rows, 20, executable)).toEqual({ workerPid: 10, rootPid: 20, ownedPids: [20] })
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, 1.5])('refuses an invalid held tool PID: %s', (pid) => {
    expect(() => resolveNativeProcessOwnership(PROCESS_TREE, pid, WORKER)).toThrow(/PID|process/)
  })
})

describe('native process ownership across E2E shards', () => {
  it('excludes another shard with the same installed Kiro relay and engine paths', () => {
    const worker = '/project/.tmp/shard-1/leapmux'
    const otherWorker = '/project/.tmp/shard-2/leapmux'
    const rows = [
      { pid: 10, ppid: 1, command: `${worker} dev` },
      { pid: 20, ppid: 10, command: '/bin/zsh provider-wrapper' },
      { pid: 21, ppid: 20, command: '/installed/kiro-cli-chat acp' },
      { pid: 22, ppid: 21, command: '/installed/kiro-cli/engine' },
      { pid: 30, ppid: 22, command: '/installed/node own-held-tool' },
      { pid: 40, ppid: 1, command: `${otherWorker} dev` },
      { pid: 50, ppid: 40, command: '/installed/kiro-cli-chat acp' },
      { pid: 60, ppid: 50, command: '/installed/kiro-cli/engine' },
      { pid: 70, ppid: 60, command: '/installed/node other-held-tool' },
    ]
    expect(resolveNativeProcessOwnership(rows, 30, worker)).toEqual({ workerPid: 10, rootPid: 20, ownedPids: [20, 21, 22, 30] })
    expect(() => resolveNativeProcessOwnership(rows, 70, worker)).toThrow('Worker boundary')
    expect(resolveNativeProcessOwnership(rows, 70, otherWorker)).toEqual({ workerPid: 40, rootPid: 50, ownedPids: [50, 60, 70] })
  })
})
