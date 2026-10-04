import { execFileSync } from 'node:child_process'
import { rmSync } from 'node:fs'
import process from 'node:process'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isAlive, listProcesses, newProcessesMatching, parseProcessRow, parseWindowsProcessTable, withDescendants } from './processTree'

const privateDirectories = vi.hoisted(() => new Set<string>())
const platformDescriptor = Object.getOwnPropertyDescriptor(process, 'platform')

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  return { ...actual, execFileSync: vi.fn(actual.execFileSync) }
})

vi.mock('node:fs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:fs')>()
  return { ...actual, rmSync: vi.fn(actual.rmSync) }
})

afterEach(async () => {
  vi.restoreAllMocks()
  if (platformDescriptor)
    Object.defineProperty(process, 'platform', platformDescriptor)
  const fs = await vi.importActual<typeof import('node:fs')>('node:fs')
  const childProcess = await vi.importActual<typeof import('node:child_process')>('node:child_process')
  for (const directory of privateDirectories)
    fs.rmSync(directory, { recursive: true, force: true })
  privateDirectories.clear()
  vi.mocked(rmSync).mockReset().mockImplementation(fs.rmSync)
  vi.mocked(execFileSync).mockReset().mockImplementation(childProcess.execFileSync)
})

describe('parseProcessRow', () => {
  it('reads the id, the parent and the command line', () => {
    expect(parseProcessRow('  4242     1 /usr/bin/kiro-cli-chat acp --agent-engine v3'))
      .toEqual({ pid: 4242, ppid: 1, command: '/usr/bin/kiro-cli-chat acp --agent-engine v3' })
  })

  it('joins the words of the command with one space', () => {
    expect(parseProcessRow('7 3 node\t  server.js   --port 1')?.command).toBe('node server.js --port 1')
  })

  it('reads a row with no command', () => {
    expect(parseProcessRow('7 3')).toEqual({ pid: 7, ppid: 3, command: '' })
  })

  it('answers null for a row of another shape', () => {
    expect(parseProcessRow('')).toBeNull()
    expect(parseProcessRow('   ')).toBeNull()
    expect(parseProcessRow('PID PPID COMMAND')).toBeNull()
    expect(parseProcessRow('12')).toBeNull()
    expect(parseProcessRow('12 -1 x')).toBeNull()
    expect(parseProcessRow('1.5 2 x')).toBeNull()
  })
})

describe('withDescendants', () => {
  const rows = [
    { pid: 10, ppid: 1, command: 'relay' },
    { pid: 11, ppid: 10, command: 'engine' },
    { pid: 12, ppid: 11, command: 'mcp server' },
    { pid: 20, ppid: 1, command: 'other' },
  ]

  it('finds every descendant of each root, however deep', () => {
    expect(withDescendants(rows, [10]).sort((a, b) => a - b)).toEqual([10, 11, 12])
  })

  it('keeps a root that the snapshot does not hold', () => {
    expect(withDescendants(rows, [99])).toEqual([99])
  })

  it('answers nothing for no root', () => {
    expect(withDescendants(rows, [])).toEqual([])
  })

  it('ends on rows whose parents form a cycle', () => {
    expect(withDescendants([{ pid: 1, ppid: 2, command: '' }, { pid: 2, ppid: 1, command: '' }], [1]).sort()).toEqual([1, 2])
  })

  it('finds the rows in any order', () => {
    expect(withDescendants([...rows].reverse(), [10]).sort((a, b) => a - b)).toEqual([10, 11, 12])
  })
})

describe('newProcessesMatching', () => {
  it('finds a process that started after the snapshot and that another parent took', () => {
    const rows = [
      { pid: 10, ppid: 1, command: '/home/e2e/kiro-cli/node kas/acp-server.js' },
      { pid: 11, ppid: 1, command: 'git status' },
      { pid: 5, ppid: 1, command: '/usr/bin/kiro-cli-chat acp' },
    ]
    expect(newProcessesMatching(rows, new Set([5]), 'kiro-cli').map(row => row.pid)).toEqual([10])
  })
})

describe('listProcesses', () => {
  it.skipIf(process.platform === 'win32')('lists this process with its parent', () => {
    const self = listProcesses().find(row => row.pid === process.pid)
    expect(self?.ppid).toBe(process.ppid)
  })

  it.each(['query', 'parse'])('preserves Windows %s and private cleanup failures together', (stage) => {
    Object.defineProperty(process, 'platform', { value: 'win32', configurable: true })
    const queryFailure = new Error('The native process query failed.')
    const cleanupFailure = new Error('The private query directory could not be removed.')
    if (stage === 'query') {
      vi.mocked(execFileSync).mockImplementationOnce(() => {
        throw queryFailure
      })
    }
    else {
      vi.mocked(execFileSync).mockReturnValueOnce('invalid-json')
    }
    vi.mocked(rmSync).mockImplementationOnce((directory) => {
      if (typeof directory === 'string')
        privateDirectories.add(directory)
      throw cleanupFailure
    })
    let failure: unknown
    try {
      listProcesses()
    }
    catch (error) {
      failure = error
    }
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The native query did not preserve both failures.')
    expect(failure.errors).toHaveLength(2)
    if (stage === 'query')
      expect(failure.errors[0]).toBe(queryFailure)
    else
      expect(failure.errors[0]).toBeInstanceOf(SyntaxError)
    expect(failure.errors[1]).toBe(cleanupFailure)
  })
})

describe('isAlive', () => {
  it('knows this process', () => {
    expect(isAlive(process.pid)).toBe(true)
  })

  // The signal check fails with EPERM for a process of another user. The process
  // exists, so the answer is true. Process 1 belongs to root, so the case needs a
  // user other than root to reach that path.
  it.skipIf(process.platform === 'win32' || process.getuid?.() === 0)('knows a process that belongs to another user', () => {
    expect(isAlive(1)).toBe(true)
  })

  it('answers false for an id that no process holds', () => {
    // The largest process id of Linux and macOS is far below this value.
    expect(isAlive(2 ** 30)).toBe(false)
  })

  it.each(['ESRCH', 'EPERM'])('keeps the native %s meaning', (code) => {
    vi.spyOn(process, 'kill').mockImplementationOnce(() => {
      throw Object.assign(new Error('The controlled signal query failed.'), { code })
    })
    expect(isAlive(123)).toBe(code === 'EPERM')
  })

  it.each(['EIO', 'EINVAL'])('propagates an unexpected %s signal-query failure', (code) => {
    const failure = Object.assign(new Error('The controlled signal query failed.'), { code })
    vi.spyOn(process, 'kill').mockImplementationOnce(() => {
      throw failure
    })
    expect(() => isAlive(123)).toThrow(failure)
  })
})

describe('parseWindowsProcessTable', () => {
  it.each(['', '  ', 'null', '[]'])('reads an empty native process table: %s', (text) => {
    expect(parseWindowsProcessTable(text)).toEqual([])
  })

  it('preserves a singleton executable path and command with spaces', () => {
    const record = { ProcessId: 42, ParentProcessId: 7, CommandLine: '"C:\\project with spaces\\leapmux.exe" worker', ExecutablePath: 'C:\\project with spaces\\leapmux.exe' }
    expect(parseWindowsProcessTable(JSON.stringify(record))).toEqual([{ pid: 42, ppid: 7, command: record.CommandLine, executable: record.ExecutablePath }])
  })

  it('reads arrays and keeps a process whose parent is zero', () => {
    const rows = [
      { ProcessId: 0, ParentProcessId: 0, CommandLine: null, ExecutablePath: null },
      { ProcessId: 1, ParentProcessId: 0, CommandLine: 'worker', ExecutablePath: null },
      { ProcessId: 2, ParentProcessId: 1, CommandLine: null, ExecutablePath: 'C:\\native.exe' },
    ]
    expect(parseWindowsProcessTable(JSON.stringify(rows))).toEqual([{ pid: 1, ppid: 0, command: 'worker' }, { pid: 2, ppid: 1, command: 'C:\\native.exe', executable: 'C:\\native.exe' }])
  })

  it('keeps an unknown command without inventing an executable', () => {
    expect(parseWindowsProcessTable(JSON.stringify({ ProcessId: 2, ParentProcessId: 1 }))).toEqual([{ pid: 2, ppid: 1, command: '' }])
  })

  it('preserves an exact Windows creation identity above the safe integer range', () => {
    const row = { ProcessId: 42, ParentProcessId: 7, CommandLine: 'private command', CreationTime: '134000000000000001' }
    expect(parseWindowsProcessTable(JSON.stringify(row))).toEqual([{ pid: 42, ppid: 7, command: 'private command', creationTime: '134000000000000001' }])
  })

  it('keeps an inaccessible creation identity absent', () => {
    expect(parseWindowsProcessTable(JSON.stringify({ ProcessId: 42, ParentProcessId: 7, CreationTime: null }))).toEqual([{ pid: 42, ppid: 7, command: '' }])
  })

  it.each([0, '', '0', '-1', '1.5', '9223372036854775808', [], {}])('refuses a malformed Windows creation identity: %j', (creationTime) => {
    expect(() => parseWindowsProcessTable(JSON.stringify({ ProcessId: 42, ParentProcessId: 7, CreationTime: creationTime }))).toThrow('creation identity')
  })

  it.each([
    [],
    null,
    'not-a-record',
    {},
    { ProcessId: -1, ParentProcessId: 1 },
    { ProcessId: 1.5, ParentProcessId: 1 },
    { ProcessId: Number.MAX_SAFE_INTEGER + 1, ParentProcessId: 1 },
    { ProcessId: 1, ParentProcessId: -1 },
    { ProcessId: 1, ParentProcessId: 1.5 },
    { ProcessId: 1, ParentProcessId: Number.MAX_SAFE_INTEGER + 1 },
    { ProcessId: '1', ParentProcessId: 1 },
    { ProcessId: 1, ParentProcessId: '1' },
    { ProcessId: 1, ParentProcessId: 0, CommandLine: 42 },
    { ProcessId: 1, ParentProcessId: 0, ExecutablePath: [] },
  ].map(record => ({ record })))('refuses an invalid native row: %j', ({ record }) => {
    expect(() => parseWindowsProcessTable(JSON.stringify([record]))).toThrow(/Windows process table/)
  })

  it('refuses malformed JSON', () => {
    expect(() => parseWindowsProcessTable('not-json')).toThrow()
  })
})

describe('parseProcessRow numeric limits', () => {
  it('keeps a valid process whose parent is zero', () => {
    expect(parseProcessRow('1 0 init')).toEqual({ pid: 1, ppid: 0, command: 'init' })
  })

  it('refuses a zero process identity', () => {
    expect(parseProcessRow('0 0 kernel')).toBeNull()
  })

  it.each(['9007199254740993 1 native', '1 9007199254740993 native'])('refuses an unsafe process identity: %s', (line) => {
    expect(parseProcessRow(line)).toBeNull()
  })
})
