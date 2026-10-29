import { spawnSync } from 'node:child_process'
import { availableParallelism, release } from 'node:os'
import { basename } from 'node:path'
import { formatGigabytes } from './metrics.mjs'

export function runTool(command, args) {
  const result = spawnSync(command, args, { encoding: 'utf8', timeout: 15_000 })
  if (result.error)
    throw result.error
  if (result.signal || result.status !== 0) {
    const reason = result.signal
      ? `signal ${result.signal}`
      : `exit status ${result.status}`
    throw new Error(`${command} failed with ${reason}: ${result.stderr.trim()}`)
  }
  return result.stdout
}

/**
 * Parses `ps -A -o pid=,ppid=,rss=,%cpu=,comm=` output. macOS reparents orphans
 * to launchd. These diagnostic rows omit start times.
 */
export function parsePs(text) {
  const rows = []
  for (const line of text.split('\n')) {
    const match = /^\s*(\d+)\s+(\d+)\s+(\d+)\s+([\d.]+)\s+(\S.*)$/.exec(line)
    if (!match)
      continue
    rows.push({
      name: basename(match[5].trim()),
      pid: Number(match[1]),
      parent: Number(match[2]),
      memory: Number(match[3]) * 1024,
      cpu: Number(match[4]),
      start: 0,
      extra: [],
    })
  }
  return rows
}

/** Parses vm_stat output into the page size and page counts by name. */
export function parseVmStat(text) {
  const pageSize = Number(/page size of (\d+) bytes/.exec(text)?.[1] ?? 4096)
  const pages = new Map()
  for (const line of text.split('\n')) {
    const match = /^(.+?):\s+(\d+)\.?$/.exec(line.trim())
    if (match)
      pages.set(match[1], Number(match[2]))
  }
  return { pageSize, pages }
}

/** Parses `sysctl name...` output into values by name. */
export function parseSysctl(text) {
  const values = new Map()
  for (const line of text.split('\n')) {
    const separator = line.indexOf(': ')
    if (separator > 0)
      values.set(line.slice(0, separator), line.slice(separator + 2).trim())
  }
  return values
}

/** Reads a size such as "1024.00M" from vm.swapusage. */
export function parseSwapSize(usage, key) {
  const match = new RegExp(`${key} = ([\\d.]+)([KMG])`).exec(usage ?? '')
  if (!match)
    return 0
  return Number(match[1]) * { K: 2 ** 10, M: 2 ** 20, G: 2 ** 30 }[match[2]]
}

// The connection states that netstat prints.
const NETSTAT_TCP_STATES = {
  ESTABLISHED: 'Established',
  SYN_SENT: 'SynSent',
  SYN_RCVD: 'SynReceived',
  FIN_WAIT_1: 'FinWait1',
  FIN_WAIT_2: 'FinWait2',
  TIME_WAIT: 'TimeWait',
  CLOSED: 'Closed',
  CLOSE_WAIT: 'CloseWait',
  LAST_ACK: 'LastAck',
  LISTEN: 'Listen',
  CLOSING: 'Closing',
}

/** Counts the connections of `netstat -an -p tcp` by state. */
export function parseNetstat(text) {
  const counts = new Map()
  for (const line of text.split('\n')) {
    if (!line.startsWith('tcp'))
      continue
    const raw = line.trim().split(/\s+/).at(-1)
    const state = NETSTAT_TCP_STATES[raw] ?? raw
    counts.set(state, (counts.get(state) ?? 0) + 1)
  }
  return counts
}

const MACOS_SYSCTL_KEYS = [
  'hw.memsize',
  'vm.swapusage',
  'kern.memorystatus_level',
  'kern.num_files',
  'kern.maxfiles',
  'kern.maxproc',
]

export function macosProbe() {
  let processors = availableParallelism()
  return {
    memoryLabel: 'rss',
    counted: ['git', 'bash', 'sh'],
    canary: [
      '/bin/sh',
      ['-c', 'exit 0'],
    ],
    header() {
      const values = parseSysctl(runTool('sysctl', ['-i', ...MACOS_SYSCTL_KEYS]))
      processors = availableParallelism()
      return [
        `os=darwin ${release()}`,
        `processors=${processors} memory=${formatGigabytes(Number(values.get('hw.memsize') ?? 0))} max-files=${values.get('kern.maxfiles')} max-processes=${values.get('kern.maxproc')}`,
      ]
    },
    system(rows) {
      const values = parseSysctl(runTool('sysctl', ['-i', ...MACOS_SYSCTL_KEYS]))
      const { pageSize, pages } = parseVmStat(runTool('vm_stat', []))
      const page = name => (pages.get(name) ?? 0) * pageSize
      const swap = values.get('vm.swapusage')
      const cpu = rows === null
        ? 'unavailable'
        : `${Math.round(rows.reduce((sum, row) => sum + (row.cpu ?? 0), 0) / processors)}%`
      return [
        ['cpu', cpu],
        ['avail', `${formatGigabytes(page('Pages free') + page('Pages inactive') + page('Pages speculative'))}/${formatGigabytes(Number(values.get('hw.memsize') ?? 0))}`],
        [
          'wired',
          formatGigabytes(page('Pages wired down')),
        ],
        [
          'compressed',
          formatGigabytes(page('Pages occupied by compressor')),
        ],
        ['swap', `${formatGigabytes(parseSwapSize(swap, 'used'))}/${formatGigabytes(parseSwapSize(swap, 'total'))}`],
        ['pressure-free', `${values.get('kern.memorystatus_level') ?? 'n/a'}%`],
        [
          'procs',
          rows === null
            ? 'unavailable'
            : String(rows.length),
        ],
        ['files', values.get('kern.num_files') ?? 'n/a'],
      ]
    },
    processes() {
      return parsePs(runTool('ps', ['-A', '-o', 'pid=,ppid=,rss=,%cpu=,comm=']))
    },
    tcpStates() {
      const output = runTool('netstat', ['-an', '-p', 'tcp'])
      // netstat prints nothing at all where a sandbox hides the socket list.
      // That is no evidence of zero connections.
      if (!output.includes('Active Internet connections'))
        throw new Error('netstat listed no TCP table')
      return parseNetstat(output)
    },
  }
}
