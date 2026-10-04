import type { ProcessRow } from '../helpers/processTree'
import { describe, expect, it } from 'vitest'
import { kiroEngineProcesses, kiroRunProcesses } from './processOwnership'

const DATA_DIR = '/project/.tmp/shard-1/kiro-data'
const OTHER_DATA_DIR = '/project/.tmp/shard-2/kiro-data'
const engine = (directory: string) => `/installed/node "${directory}/kas/version/node_modules/@kiro/agent/dist/index.js" --stdio`
const ROWS: ProcessRow[] = [
  { pid: 10, ppid: 1, command: '/project/.tmp/shard-1/leapmux dev' },
  { pid: 20, ppid: 10, command: '/installed/kiro-cli-chat acp' },
  { pid: 21, ppid: 20, command: engine(DATA_DIR) },
  { pid: 22, ppid: 21, command: '/installed/node held-tool' },
  { pid: 50, ppid: 1, command: '/project/.tmp/shard-2/leapmux dev' },
  { pid: 60, ppid: 50, command: '/installed/kiro-cli-chat acp' },
  { pid: 61, ppid: 60, command: engine(OTHER_DATA_DIR) },
  { pid: 62, ppid: 61, command: '/installed/node other-tool' },
  { pid: 99, ppid: 1, command: engine(DATA_DIR) },
]

describe('kiroEngineProcesses', () => {
  it('selects its private engines after one engine loses its relay parent', () => {
    expect(kiroEngineProcesses(ROWS, DATA_DIR).map(row => row.pid)).toEqual([21, 99])
  })

  it('keeps spaces and regex characters literal in a quoted private directory', () => {
    const directory = '/project [review]/.tmp/shard 1/kiro-data'
    const rows = [{ pid: 21, ppid: 20, command: engine(directory) }]
    expect(kiroEngineProcesses(rows, directory)).toEqual(rows)
  })

  it('reads the raw command when the process table normalizes repeated spaces', () => {
    const directory = '/project with  two spaces/.tmp/shard-1/kiro-data'
    const rawCommand = engine(directory)
    const rows = [{ pid: 21, ppid: 20, command: rawCommand.replaceAll('  ', ' '), rawCommand }]
    expect(kiroEngineProcesses(rows, directory)).toEqual(rows)
  })

  it('accepts the complete native Windows path without changing its ownership', () => {
    const directory = 'C:\\Project Files\\Shard-1\\kiro-data'
    const rows = [{ pid: 21, ppid: 20, command: '"C:\\Node\\node.exe" "c:\\PROJECT FILES\\SHARD-1\\KIRO-DATA\\kas\\version\\node_modules\\@kiro\\agent\\dist\\index.js"' }]
    expect(kiroEngineProcesses(rows, directory)).toEqual(rows)
  })

  it('excludes directory-prefix collisions and a path inside another directory', () => {
    const rows = [
      { pid: 21, ppid: 20, command: engine(`${DATA_DIR}-other`) },
      { pid: 22, ppid: 20, command: engine(`/outside${DATA_DIR}`) },
      { pid: 23, ppid: 20, command: engine(OTHER_DATA_DIR) },
    ]
    expect(kiroEngineProcesses(rows, DATA_DIR)).toEqual([])
  })

  it('does not use a script string that only mentions a private engine path', () => {
    const rows = [{ pid: 21, ppid: 20, command: `/installed/node -e "print('${DATA_DIR}/kas/version/node_modules/@kiro/agent/dist/index.js')"` }]
    expect(kiroEngineProcesses(rows, DATA_DIR)).toEqual([])
  })

  it.each(['', '  ', '../kiro-data', '/private\0/kiro-data', '/', 'C:\\'])('rejects an unsafe private directory: %j', (directory) => {
    expect(() => kiroEngineProcesses(ROWS, directory)).toThrow(/private.*directory/)
  })
})

describe('kiroRunProcesses', () => {
  it('finds late owned relays and reparented engines without selecting another shard', () => {
    const owner = { workerPid: 10, beforePids: new Set([10, 50]), dataDir: DATA_DIR }
    expect(kiroRunProcesses(ROWS, owner).map(row => row.pid)).toEqual([20, 21, 99])
  })

  it('excludes processes that existed before the selected agent opened', () => {
    const owner = { workerPid: 10, beforePids: new Set([10, 20, 21, 50, 99]), dataDir: DATA_DIR }
    expect(kiroRunProcesses(ROWS, owner)).toEqual([])
  })

  it('excludes an executable that only shares the relay filename prefix', () => {
    const rows = [{ pid: 20, ppid: 10, command: '/installed/kiro-cli-chat-other acp' }, ...ROWS.filter(row => row.pid === 10)]
    const owner = { workerPid: 10, beforePids: new Set([10]), dataDir: DATA_DIR }
    expect(kiroRunProcesses(rows, owner)).toEqual([])
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, 1.5])('rejects an invalid Worker PID: %s', (workerPid) => {
    const owner = { workerPid, beforePids: new Set<number>(), dataDir: DATA_DIR }
    expect(() => kiroRunProcesses(ROWS, owner)).toThrow('Worker PID')
  })
})
