import process from 'node:process'
import { describe, expect, it } from 'vitest'
import { isAlive, listProcesses, newProcessesMatching, parseProcessRow, withDescendants } from './processTree'

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
})
