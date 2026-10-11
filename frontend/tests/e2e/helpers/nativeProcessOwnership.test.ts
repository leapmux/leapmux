import process from 'node:process'
import { describe, expect, it } from 'vitest'
import { commandStartsWithExecutable, resolveNativeProcessOwnership, sameExecutablePath, workerDataDirectory } from './nativeProcessOwnership'

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

describe('commandStartsWithExecutable', () => {
  it.each([
    WORKER,
    `${WORKER} worker --hub http://127.0.0.1:1`,
    `${WORKER}\tworker`,
    `"${WORKER}" worker`,
    `'${WORKER}' worker`,
  ])('accepts the executable as the whole first word: %s', (command) => {
    expect(commandStartsWithExecutable(command, WORKER)).toBe(true)
  })

  it.each([
    `${WORKER}-other worker`,
    `/other${WORKER} worker`,
    `"${WORKER} worker`,
    '',
  ])('refuses another first word: %s', (command) => {
    expect(commandStartsWithExecutable(command, WORKER)).toBe(false)
  })
})

describe('sameExecutablePath', () => {
  it('compares the paths exactly on POSIX and without case on Windows', () => {
    expect(sameExecutablePath(WORKER, WORKER)).toBe(true)
    expect(sameExecutablePath(`${WORKER}-other`, WORKER)).toBe(false)
    expect(sameExecutablePath('C:\\Bin\\LeapMux.exe', 'c:\\bin\\leapmux.exe')).toBe(process.platform === 'win32')
  })
})

describe('workerDataDirectory', () => {
  it.each([
    ['the long flag and a separate value', `${WORKER} worker --hub http://127.0.0.1:1 --data-dir /private/worker`],
    ['the short flag and a separate value', `${WORKER} dev -listen 127.0.0.1:0 -data-dir /private/worker`],
    ['the long flag with =', `${WORKER} worker --data-dir=/private/worker --hub http://127.0.0.1:1`],
    ['the short flag with =', `${WORKER} worker -data-dir=/private/worker`],
    // The order of spawnRegisteredWorker: the registration key and the extra arguments follow the data directory.
    ['an argument after the value', `${WORKER} worker --hub http://127.0.0.1:1 --data-dir /private/worker --registration-key abc --encryption-mode post-quantum`],
    ['a value in double quotes', `"${WORKER}" worker --data-dir "/private/worker" --registration-key abc`],
    ['a value in single quotes', `'${WORKER}' worker --data-dir '/private/worker'`],
  ])('reads %s', (_form, command) => {
    expect(workerDataDirectory(command)).toBe('/private/worker')
  })

  it.each([
    ['in double quotes', `${WORKER} worker --data-dir "/private/worker with spaces" --registration-key abc`],
    ['in double quotes after =', `${WORKER} worker --data-dir="/private/worker with spaces" --registration-key abc`],
    ['with no quoting, as ps reports it', `${WORKER} worker --data-dir /private/worker with spaces --registration-key abc`],
    ['with no quoting at the end', `${WORKER} worker -data-dir /private/worker with spaces`],
    ['with no quoting after =', `${WORKER} worker --data-dir=/private/worker with spaces --hub http://127.0.0.1:1`],
  ])('reads a path with spaces %s', (_form, command) => {
    expect(workerDataDirectory(command)).toBe('/private/worker with spaces')
  })

  it('keeps the exact whitespace inside an unquoted path', () => {
    expect(workerDataDirectory(`${WORKER} worker --data-dir /private/two  spaces --hub x`)).toBe('/private/two  spaces')
  })

  it('reads an escaped double quote inside double quotes', () => {
    expect(workerDataDirectory(`${WORKER} worker --data-dir "/private/a \\"quoted\\" b"`)).toBe('/private/a "quoted" b')
  })

  it.each([
    ['no flag', `${WORKER} worker --hub http://127.0.0.1:1`],
    ['an empty command line', ''],
    ['a flag with a longer name', `${WORKER} worker --data-dir-more /private/worker`],
    ['two flags', `${WORKER} worker -data-dir /private/one --data-dir /private/two`],
  ])('refuses %s', (_case, command) => {
    expect(() => workerDataDirectory(command)).toThrow('one data-directory argument')
  })

  it.each([
    ['a relative path', `${WORKER} worker --data-dir relative`],
    ['an empty value after =', `${WORKER} worker --data-dir= --hub x`],
    ['a path that spans two lines', `${WORKER} worker --data-dir /private/worker\nother`],
  ])('refuses %s', (_case, command) => {
    expect(() => workerDataDirectory(command)).toThrow('absolute path')
  })

  it('refuses a flag with no value', () => {
    expect(() => workerDataDirectory(`${WORKER} worker --data-dir`)).toThrow('no value after its --data-dir argument')
  })

  it('refuses an unbalanced quote', () => {
    expect(() => workerDataDirectory(`${WORKER} worker --data-dir "/private/worker`)).toThrow('unbalanced " quote')
  })
})
