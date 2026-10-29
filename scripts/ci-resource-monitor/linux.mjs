import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { availableParallelism } from 'node:os'
import { basename } from 'node:path'
import { cpuPercent, formatGigabytes } from './metrics.mjs'

/**
 * Parses /proc/<pid>/stat. The command name sits in parentheses and can hold
 * spaces and parentheses itself, so the fields start after the last `)`.
 */
export function parseProcStat(text) {
  const open = text.indexOf('(')
  const close = text.lastIndexOf(')')
  if (open < 0 || close < open)
    return null
  const fields = text.slice(close + 2).trim().split(/\s+/)
  return {
    comm: text.slice(open + 1, close),
    state: fields[0],
    parent: Number(fields[1]),
    rssPages: Number(fields[21]),
  }
}

/** Parses /proc/meminfo into bytes by key. */
export function parseMeminfo(text) {
  const values = new Map()
  for (const line of text.split('\n')) {
    const match = /^(\S+):\s+(\d+)(?:\s+(kB))?$/.exec(line.trim())
    if (match)
      values.set(match[1], Number(match[2]) * (match[3] ? 1024 : 1))
  }
  return values
}

/** Reads the first line of /proc/stat as the idle and total CPU time. */
export function parseProcStatCpu(text) {
  const fields = text.split('\n')[0].trim().split(/\s+/).slice(1).map(Number)
  // idle plus iowait, out of user, nice, system, idle, iowait, irq, softirq and steal.
  return {
    idle: fields[3] + (fields[4] ?? 0),
    total: fields.slice(0, 8).reduce((sum, value) => sum + value, 0),
  }
}

// The connection states of /proc/net/tcp, from include/net/tcp_states.h.
const LINUX_TCP_STATES = {
  '01': 'Established',
  '02': 'SynSent',
  '03': 'SynReceived',
  '04': 'FinWait1',
  '05': 'FinWait2',
  '06': 'TimeWait',
  '07': 'Closed',
  '08': 'CloseWait',
  '09': 'LastAck',
  '0A': 'Listen',
  '0B': 'Closing',
}

/** Counts the connections of /proc/net/tcp or /proc/net/tcp6 by state. */
export function parseProcNetTcp(text, counts = new Map()) {
  for (const line of text.split('\n').slice(1)) {
    const fields = line.trim().split(/\s+/)
    if (fields.length < 4)
      continue
    const state = LINUX_TCP_STATES[fields[3].toUpperCase()] ?? `0x${fields[3]}`
    counts.set(state, (counts.get(state) ?? 0) + 1)
  }
  return counts
}

export function linuxProbe() {
  const read = path => readFileSync(path, 'utf8')
  // /proc states the resident set in pages, and an arm64 kernel can use a page
  // larger than 4 KiB, so derive the size from this process's own figures.
  const ownKilobytes = Number(/^VmRSS:\s+(\d+)/m.exec(read('/proc/self/status'))[1])
  const ownPages = Number(read('/proc/self/statm').split(/\s+/)[1])
  const pageSize = 2 ** Math.round(Math.log2(ownKilobytes * 1024 / ownPages))
  let previousCpu = null
  return {
    memoryLabel: 'rss',
    counted: ['git', 'bash', 'sh'],
    canary: [
      '/bin/sh',
      ['-c', 'exit 0'],
    ],
    header() {
      const meminfo = parseMeminfo(read('/proc/meminfo'))
      return [
        `os=linux ${read('/proc/sys/kernel/osrelease').trim()} page=${pageSize}`,
        `processors=${availableParallelism()} memory=${formatGigabytes(meminfo.get('MemTotal'))} commit-limit=${formatGigabytes(meminfo.get('CommitLimit'))} overcommit=${read('/proc/sys/vm/overcommit_memory').trim()}`,
        `pid-max=${read('/proc/sys/kernel/pid_max').trim()} threads-max=${read('/proc/sys/kernel/threads-max').trim()} file-max=${read('/proc/sys/fs/file-max').trim()}`,
      ]
    },
    system(rows) {
      const cpu = parseProcStatCpu(read('/proc/stat'))
      const percent = cpuPercent(previousCpu, cpu)
      previousCpu = cpu
      const meminfo = parseMeminfo(read('/proc/meminfo'))
      const swapTotal = meminfo.get('SwapTotal') ?? 0
      return [
        ['cpu', percent],
        ['avail', `${formatGigabytes(meminfo.get('MemAvailable'))}/${formatGigabytes(meminfo.get('MemTotal'))}`],
        ['commit', `${formatGigabytes(meminfo.get('Committed_AS'))}/${formatGigabytes(meminfo.get('CommitLimit'))}`],
        ['swap', `${formatGigabytes(swapTotal - (meminfo.get('SwapFree') ?? 0))}/${formatGigabytes(swapTotal)}`],
        [
          'procs',
          rows === null
            ? 'unavailable'
            : String(rows.length),
        ],
        ['threads', read('/proc/loadavg').trim().split(/\s+/)[3]?.split('/')[1] ?? 'n/a'],
        ['files', read('/proc/sys/fs/file-nr').trim().split(/\s+/)[0]],
      ]
    },
    processes() {
      const rows = []
      for (const entry of readdirSync('/proc')) {
        if (!/^\d+$/.test(entry))
          continue
        try {
          const stat = parseProcStat(read(`/proc/${entry}/stat`))
          if (!stat)
            continue
          // The kernel truncates the command name to 15 characters, and a Go
          // test binary is named after its package, so read the full name.
          let name = stat.comm
          if (name.length === 15) {
            const command = read(`/proc/${entry}/cmdline`).split('\0')[0]
            if (command)
              name = basename(command)
          }
          // Linux reparents orphans instead of retaining an ended parent's PID.
          // This diagnostic probe records the parent without start times.
          rows.push({
            name,
            pid: Number(entry),
            parent: stat.parent,
            memory: stat.rssPages * pageSize,
            start: 0,
            extra: [],
          })
        }
        catch {
          // The process ended while the sampler read it.
        }
      }
      return rows
    },
    tcpStates() {
      const counts = new Map()
      for (const path of ['/proc/net/tcp', '/proc/net/tcp6']) {
        if (existsSync(path))
          parseProcNetTcp(read(path), counts)
      }
      return counts
    },
  }
}
