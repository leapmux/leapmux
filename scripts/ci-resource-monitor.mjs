#!/usr/bin/env bun
// ci-resource-monitor.mjs -- Sample the resources of a CI runner while the job
// runs.
//
// Usage:
//   bun scripts/ci-resource-monitor.mjs start   # create the check run, detach the sampler
//   bun scripts/ci-resource-monitor.mjs run     # sample until stop asks to end (start runs this)
//   bun scripts/ci-resource-monitor.mjs stop    # end the sampler, complete the check run, print the log
//
// The ci.yaml jobs run it when the repository variable CI_RESOURCE_MONITOR is
// true.
//
// GitHub uploads no log for a job whose runner it lost, so the sampler
// publishes its samples to a check run of its own. That output stays readable
// after GitHub declares the runner lost, and as long as the machine can still
// reach GitHub, it shows what happened to the runner afterwards.
//
// Each OS has a probe:
//
// - Windows calls the Win32 API through bun:ffi, inside the sampler's own
//   process. When Windows can no longer start a process, the sampler keeps
//   sampling, and its canary process records when process creation failed.
// - Linux reads /proc, also inside the sampler's own process.
// - macOS has no /proc, so it runs ps, sysctl, vm_stat and netstat.

import { Buffer } from 'node:buffer'
import { spawn, spawnSync } from 'node:child_process'
import { appendFileSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statfsSync, statSync, writeFileSync } from 'node:fs'
import { availableParallelism, hostname, release } from 'node:os'
import { basename, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const SCRIPT = fileURLToPath(import.meta.url)

// A check run accepts at most 65535 characters in each output field.
export const MAX_OUTPUT_CHARACTERS = 60000
// Every sample whose index is a multiple of this count also records the
// process tables.
export const DETAIL_EVERY = 6
// stop waits this long for the sampler to publish its last sample and end.
const STOP_WAIT_MS = 60_000

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

export function formatGigabytes(bytes) {
  return `${(bytes / 2 ** 30).toFixed(1)}G`
}

export function formatMegabytes(bytes) {
  return `${Math.round(bytes / 2 ** 20)}M`
}

/** Keeps the end of `text` within the size that a check run field accepts. */
export function limitText(text) {
  if (text.length <= MAX_OUTPUT_CHARACTERS)
    return text
  return `...${text.slice(text.length - MAX_OUTPUT_CHARACTERS)}`
}

/** Formats TCP connection counts by state, the most common state first. */
export function formatTcpStates(counts) {
  if (counts.size === 0)
    return 'none'
  return [...counts].sort((a, b) => b[1] - a[1]).map(([state, count]) => `${state}:${count}`).join(',')
}

export function utcTime(date = new Date()) {
  return date.toISOString().slice(11, 19)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

function positiveSeconds(value, fallback) {
  const seconds = Number(value)
  return Number.isFinite(seconds) && seconds > 0
    ? seconds
    : fallback
}

/**
 * Reads the monitor's configuration from the environment of a GitHub Actions
 * step. MONITOR_SAMPLE_SECONDS and MONITOR_PUBLISH_SECONDS exist for tests.
 */
export function monitorConfig(env) {
  if (!env.RUNNER_TEMP)
    throw new Error('The resource monitor requires RUNNER_TEMP.')
  const directory = join(env.RUNNER_TEMP, 'leapmux-resource-monitor')
  return {
    directory,
    logPath: join(directory, 'monitor.log'),
    pidPath: join(directory, 'monitor.pid'),
    checkRunPath: join(directory, 'check-run-id'),
    stopPath: join(directory, 'stop'),
    endedPath: join(directory, 'ended'),
    sampleMs: positiveSeconds(env.MONITOR_SAMPLE_SECONDS, 10) * 1000,
    publishMs: positiveSeconds(env.MONITOR_PUBLISH_SECONDS, 30) * 1000,
    token: env.GH_TOKEN ?? '',
    repository: env.GITHUB_REPOSITORY ?? '',
    apiUrl: (env.GITHUB_API_URL || 'https://api.github.com').replace(/\/+$/, ''),
    checkName: `${env.RUNNER_OS || process.platform} ${env.RUNNER_ARCH || process.arch} resource monitor`,
    // A pull request's check run belongs to its head commit, not to the merge commit.
    headSha: env.MONITOR_HEAD_SHA || env.GITHUB_SHA || '',
    detailsUrl: `${env.GITHUB_SERVER_URL}/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}`,
    diskPaths: process.platform === 'win32'
      ? [`${env.SystemDrive || 'C:'}\\`, env.RUNNER_TEMP, env.GITHUB_WORKSPACE]
      : ['/', env.RUNNER_TEMP, env.GITHUB_WORKSPACE],
    image: `${env.ImageOS ?? 'unknown'} ${env.ImageVersion ?? ''}`.trim(),
  }
}

// ---------------------------------------------------------------------------
// GitHub
// ---------------------------------------------------------------------------

/**
 * Sends one REST request. It returns null when the job supplied no token, so
 * a run without a token (a fork) still keeps the local log.
 */
async function callGitHub(config, method, path, body) {
  if (!config.token || !config.repository)
    return null
  const response = await fetch(`${config.apiUrl}/repos/${config.repository}${path}`, {
    method,
    headers: {
      'Authorization': `Bearer ${config.token}`,
      'Accept': 'application/vnd.github+json',
      'Content-Type': 'application/json',
      'X-GitHub-Api-Version': '2022-11-28',
    },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(20_000),
  })
  if (!response.ok)
    throw new Error(`GitHub answered ${response.status} to ${method} ${path}: ${(await response.text()).slice(0, 200)}`)
  return response.json()
}

function publishOutput(config, checkRunId, { title, summary, text, complete }) {
  const body = {
    output: {
      title,
      summary: limitText(summary),
      text: limitText(text),
    },
  }
  if (complete) {
    body.status = 'completed'
    body.conclusion = 'neutral'
  }
  return callGitHub(config, 'PATCH', `/check-runs/${checkRunId}`, body)
}

// ---------------------------------------------------------------------------
// Linux
// ---------------------------------------------------------------------------

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

/** The CPU use between two readings of the idle and total CPU time. */
export function cpuPercent(previous, current) {
  if (!previous)
    return 'n/a'
  const total = current.total - previous.total
  if (total <= 0)
    return 'n/a'
  return `${Math.round(100 * (1 - (current.idle - previous.idle) / total))}%`
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

function linuxProbe() {
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
          String(rows.length),
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
          // Linux gives an orphan to init or a subreaper, so a recorded parent
          // always runs, and a row needs no start time.
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

// ---------------------------------------------------------------------------
// macOS
// ---------------------------------------------------------------------------

function runTool(command, args) {
  const result = spawnSync(command, args, { encoding: 'utf8', timeout: 15_000 })
  if (result.error)
    throw result.error
  return result.stdout
}

/**
 * Parses `ps -A -o pid=,ppid=,rss=,%cpu=,comm=` output. macOS gives an orphan
 * to launchd, so a recorded parent always runs, and a row needs no start time.
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

function macosProbe() {
  let processors = availableParallelism()
  return {
    memoryLabel: 'rss',
    counted: ['git', 'bash', 'sh'],
    canary: [
      '/bin/sh',
      ['-c', 'exit 0'],
    ],
    header() {
      const values = parseSysctl(runTool('sysctl', MACOS_SYSCTL_KEYS))
      processors = availableParallelism()
      return [
        `os=darwin ${release()}`,
        `processors=${processors} memory=${formatGigabytes(Number(values.get('hw.memsize') ?? 0))} max-files=${values.get('kern.maxfiles')} max-processes=${values.get('kern.maxproc')}`,
      ]
    },
    system(rows) {
      const values = parseSysctl(runTool('sysctl', MACOS_SYSCTL_KEYS))
      const { pageSize, pages } = parseVmStat(runTool('vm_stat', []))
      const page = name => (pages.get(name) ?? 0) * pageSize
      const swap = values.get('vm.swapusage')
      const cpu = rows.reduce((sum, row) => sum + (row.cpu ?? 0), 0) / processors
      return [
        ['cpu', `${Math.round(cpu)}%`],
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
          String(rows.length),
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

// ---------------------------------------------------------------------------
// Windows
// ---------------------------------------------------------------------------

// The connection states of MIB_TCP_STATE.
const WINDOWS_TCP_STATES = [
  '',
  'Closed',
  'Listen',
  'SynSent',
  'SynReceived',
  'Established',
  'FinWait1',
  'FinWait2',
  'CloseWait',
  'Closing',
  'LastAck',
  'TimeWait',
  'DeleteTcb',
]

const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
const TH32CS_SNAPPROCESS = 0x2
const ERROR_INSUFFICIENT_BUFFER = 122
// The sizes and field offsets below are those of the 64-bit Windows ABI.
const PERFORMANCE_INFORMATION_SIZE = 104
const PROCESSENTRY32W_SIZE = 568
const PROCESS_MEMORY_COUNTERS_EX_SIZE = 80
const MIB_TCPROW_SIZE = 20
const MIB_TCP6ROW_SIZE = 52

async function loadWin32() {
  const { dlopen, ptr } = await import('bun:ffi')
  const handle = 'i64_fast'
  const kernel32 = dlopen('kernel32.dll', {
    GetSystemTimes: {
      args: ['ptr', 'ptr', 'ptr'],
      returns: 'i32',
    },
    CreateToolhelp32Snapshot: {
      args: ['u32', 'u32'],
      returns: handle,
    },
    Process32FirstW: {
      args: ['i64', 'ptr'],
      returns: 'i32',
    },
    Process32NextW: {
      args: ['i64', 'ptr'],
      returns: 'i32',
    },
    OpenProcess: {
      args: ['u32', 'i32', 'u32'],
      returns: handle,
    },
    CloseHandle: {
      args: ['i64'],
      returns: 'i32',
    },
    GetProcessHandleCount: {
      args: ['i64', 'ptr'],
      returns: 'i32',
    },
    GetProcessTimes: {
      args: ['i64', 'ptr', 'ptr', 'ptr', 'ptr'],
      returns: 'i32',
    },
    ProcessIdToSessionId: {
      args: ['u32', 'ptr'],
      returns: 'i32',
    },
    GetCurrentThreadId: { args: [], returns: 'u32' },
    GetStdHandle: {
      args: ['u32'],
      returns: handle,
    },
    SetHandleInformation: {
      args: ['i64', 'u32', 'u32'],
      returns: 'i32',
    },
  }).symbols
  const psapi = dlopen('psapi.dll', {
    GetPerformanceInfo: {
      args: ['ptr', 'u32'],
      returns: 'i32',
    },
    GetProcessMemoryInfo: {
      args: ['i64', 'ptr', 'u32'],
      returns: 'i32',
    },
  }).symbols
  const user32 = dlopen('user32.dll', {
    GetGuiResources: {
      args: ['i64', 'u32'],
      returns: 'u32',
    },
    GetProcessWindowStation: { args: [], returns: handle },
    GetThreadDesktop: {
      args: ['u32'],
      returns: handle,
    },
    GetUserObjectInformationW: {
      args: ['i64', 'i32', 'ptr', 'u32', 'ptr'],
      returns: 'i32',
    },
  }).symbols
  const iphlpapi = dlopen('iphlpapi.dll', {
    GetTcpTable: {
      args: ['ptr', 'ptr', 'i32'],
      returns: 'u32',
    },
    GetTcp6Table: {
      args: ['ptr', 'ptr', 'i32'],
      returns: 'u32',
    },
  }).symbols
  return {
    kernel32,
    psapi,
    user32,
    iphlpapi,
    ptr,
  }
}

/**
 * Makes the standard handles of this process uninheritable. Bun starts a
 * child with handle inheritance on, so without this the sampler would hold
 * the step's output pipe, and the step would wait for the sampler to exit.
 */
async function keepStandardHandlesFromChildren() {
  if (process.platform !== 'win32')
    return
  const { kernel32 } = await loadWin32()
  const HANDLE_FLAG_INHERIT = 1
  // STD_INPUT_HANDLE, STD_OUTPUT_HANDLE and STD_ERROR_HANDLE as DWORD values.
  for (const standard of [-10, -11, -12]) {
    const handle = kernel32.GetStdHandle(standard >>> 0)
    if (handle !== 0 && handle !== -1)
      kernel32.SetHandleInformation(handle, HANDLE_FLAG_INHERIT, 0)
  }
}

/** The value of `key` among the extra readings of a process row, or -1. */
function extraValue(row, key) {
  return row.extra.find(([name]) => name === key)?.[1] ?? -1
}

async function windowsProbe() {
  const { kernel32, psapi, user32, iphlpapi, ptr } = await loadWin32()
  const fileTimeToUnixMs = ticks => Number(ticks / 10000n) - 11644473600000
  let previousCpu = null

  function performanceInfo() {
    const buffer = new Uint8Array(PERFORMANCE_INFORMATION_SIZE)
    const view = new DataView(buffer.buffer)
    view.setUint32(0, PERFORMANCE_INFORMATION_SIZE, true)
    if (!psapi.GetPerformanceInfo(ptr(buffer), PERFORMANCE_INFORMATION_SIZE))
      throw new Error('GetPerformanceInfo failed')
    const size = offset => Number(view.getBigUint64(offset, true))
    const page = size(80)
    return {
      commit: size(8) * page,
      commitLimit: size(16) * page,
      physical: size(32) * page,
      available: size(40) * page,
      kernelPaged: size(64) * page,
      kernelNonpaged: size(72) * page,
      handles: view.getUint32(88, true),
      processes: view.getUint32(92, true),
      threads: view.getUint32(96, true),
    }
  }

  function userObjectText(object, index) {
    const needed = new Uint32Array(1)
    user32.GetUserObjectInformationW(object, index, null, 0, ptr(needed))
    if (needed[0] === 0)
      return null
    const buffer = new Uint8Array(needed[0])
    if (!user32.GetUserObjectInformationW(object, index, ptr(buffer), needed[0], ptr(needed)))
      return null
    return buffer
  }

  function desktop() {
    const UOI_NAME = 2
    const UOI_HEAPSIZE = 5
    const name = object => Buffer.from(userObjectText(object, UOI_NAME) ?? []).toString('utf16le').replace(/\0+$/, '') || 'unknown'
    const desk = user32.GetThreadDesktop(kernel32.GetCurrentThreadId())
    const heap = userObjectText(desk, UOI_HEAPSIZE)
    const heapSize = heap && heap.length >= 4
      ? `${new DataView(heap.buffer).getUint32(0, true)}KB`
      : 'unknown'
    return `${name(user32.GetProcessWindowStation())}\\${name(desk)} heap=${heapSize}`
  }

  function sessionOf(pid) {
    const session = new Uint32Array(1)
    return kernel32.ProcessIdToSessionId(pid, ptr(session))
      ? session[0]
      : -1
  }

  function details(pid) {
    const result = {
      memory: -1,
      handles: -1,
      user: -1,
      gdi: -1,
      start: 0,
    }
    const handle = kernel32.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid)
    if (handle === 0 || handle === -1)
      return result
    try {
      const counters = new Uint8Array(PROCESS_MEMORY_COUNTERS_EX_SIZE)
      const view = new DataView(counters.buffer)
      view.setUint32(0, PROCESS_MEMORY_COUNTERS_EX_SIZE, true)
      if (psapi.GetProcessMemoryInfo(handle, ptr(counters), PROCESS_MEMORY_COUNTERS_EX_SIZE))
        result.memory = Number(view.getBigUint64(72, true))
      const handles = new Uint32Array(1)
      if (kernel32.GetProcessHandleCount(handle, ptr(handles)))
        result.handles = handles[0]
      result.user = user32.GetGuiResources(handle, 1)
      result.gdi = user32.GetGuiResources(handle, 0)
      const times = new BigUint64Array(4)
      if (kernel32.GetProcessTimes(handle, ptr(times), ptr(times.subarray(1)), ptr(times.subarray(2)), ptr(times.subarray(3))))
        result.start = fileTimeToUnixMs(times[0])
    }
    finally {
      kernel32.CloseHandle(handle)
    }
    return result
  }

  function tcpTable(read, rowSize, counts) {
    const size = new Uint32Array(1)
    let status = read(null, ptr(size), 0)
    if (status !== ERROR_INSUFFICIENT_BUFFER && status !== 0)
      throw new Error(`the TCP table read failed with ${status}`)
    const table = new Uint8Array(Math.max(size[0], 4))
    status = read(ptr(table), ptr(size), 0)
    if (status !== 0)
      throw new Error(`the TCP table read failed with ${status}`)
    const view = new DataView(table.buffer)
    const entries = view.getUint32(0, true)
    for (let index = 0; index < entries; index++) {
      const state = WINDOWS_TCP_STATES[view.getUint32(4 + index * rowSize, true)] ?? 'Unknown'
      counts.set(state, (counts.get(state) ?? 0) + 1)
    }
  }

  return {
    memoryLabel: 'private',
    counted: [
      'conhost',
      'pwsh',
      'powershell',
      'cmd',
      'git',
    ],
    canary: [
      process.env.ComSpec || 'cmd.exe',
      ['/d', '/c', 'exit 0'],
    ],
    header() {
      const info = performanceInfo()
      return [
        `os=windows ${release()}`,
        `processors=${availableParallelism()} physical=${formatGigabytes(info.physical)} commit-limit=${formatGigabytes(info.commitLimit)}`,
        `desktop=${desktop()} session=${sessionOf(process.pid)}`,
      ]
    },
    system(rows) {
      const times = new BigUint64Array(3)
      if (!kernel32.GetSystemTimes(ptr(times), ptr(times.subarray(1)), ptr(times.subarray(2))))
        throw new Error('GetSystemTimes failed')
      // The kernel time includes the idle time.
      const cpu = {
        idle: Number(times[0]),
        total: Number(times[1] + times[2]),
      }
      const percent = cpuPercent(previousCpu, cpu)
      previousCpu = cpu
      const info = performanceInfo()
      const userBySession = new Map()
      for (const row of rows) {
        const user = extraValue(row, 'user')
        if (user >= 0)
          userBySession.set(row.session, (userBySession.get(row.session) ?? 0) + user)
      }
      return [
        ['cpu', percent],
        ['commit', `${formatGigabytes(info.commit)}/${formatGigabytes(info.commitLimit)}`],
        [
          'avail',
          formatGigabytes(info.available),
        ],
        [
          'kpaged',
          formatMegabytes(info.kernelPaged),
        ],
        [
          'knp',
          formatMegabytes(info.kernelNonpaged),
        ],
        [
          'procs',
          String(info.processes),
        ],
        [
          'threads',
          String(info.threads),
        ],
        [
          'handles',
          String(info.handles),
        ],
        [
          'user',
          [...userBySession].sort((a, b) => a[0] - b[0]).map(([session, user]) => `s${session}:${user}`).join(','),
        ],
      ]
    },
    processes() {
      const snapshot = kernel32.CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0)
      if (snapshot === -1 || snapshot === 0)
        throw new Error('CreateToolhelp32Snapshot failed')
      const rows = []
      try {
        const entry = new Uint8Array(PROCESSENTRY32W_SIZE)
        const view = new DataView(entry.buffer)
        view.setUint32(0, PROCESSENTRY32W_SIZE, true)
        for (let more = kernel32.Process32FirstW(snapshot, ptr(entry)); more; more = kernel32.Process32NextW(snapshot, ptr(entry))) {
          const pid = view.getUint32(8, true)
          const exe = Buffer.from(entry.buffer, 44, 520).toString('utf16le')
          const end = exe.indexOf('\0')
          const name = exe.slice(0, end < 0
            ? exe.length
            : end).replace(/\.exe$/i, '')
          const facts = details(pid)
          rows.push({
            name,
            pid,
            parent: view.getUint32(32, true),
            memory: facts.memory,
            start: facts.start,
            session: sessionOf(pid),
            extra: [
              ['handles', facts.handles],
              ['user', facts.user],
              ['gdi', facts.gdi],
            ],
          })
        }
      }
      finally {
        kernel32.CloseHandle(snapshot)
      }
      return rows
    },
    tcpStates() {
      const counts = new Map()
      tcpTable(iphlpapi.GetTcpTable, MIB_TCPROW_SIZE, counts)
      tcpTable(iphlpapi.GetTcp6Table, MIB_TCP6ROW_SIZE, counts)
      return counts
    },
    extraDetail(rows, parentName) {
      return [
        'By USER objects:',
        ...rows.filter(row => extraValue(row, 'user') > 0)
          .sort((a, b) => extraValue(b, 'user') - extraValue(a, 'user'))
          .slice(0, 8)
          .map(row => [
            `  ${row.name.padEnd(28)}`,
            `pid=${String(row.pid).padEnd(6)}`,
            `user=${String(extraValue(row, 'user')).padEnd(5)}`,
            `gdi=${String(extraValue(row, 'gdi')).padEnd(5)}`,
            `session=${row.session}`,
            `parent=${parentName(row)}`,
          ].join(' ')),
      ]
    },
  }
}

/** Builds the probe of `platform`, the value of process.platform. */
export async function createProbe(platform) {
  switch (platform) {
    case 'win32': return windowsProbe()
    case 'linux': return linuxProbe()
    case 'darwin': return macosProbe()
    default: throw new Error(`The resource monitor has no probe for ${platform}.`)
  }
}

// ---------------------------------------------------------------------------
// Samples
// ---------------------------------------------------------------------------

/** Starts one trivial process and reports how long it took, or how it failed. */
export function runCanary([command, args]) {
  const started = performance.now()
  const result = spawnSync(command, args, { stdio: 'ignore', timeout: 15_000, windowsHide: true })
  const elapsed = Math.round(performance.now() - started)
  if (result.error) {
    return result.error.code === 'ETIMEDOUT'
      ? 'hung'
      : `fail(${result.error.message})`
  }
  if (result.status === 0)
    return `ok(${elapsed}ms)`
  if (result.status === null)
    return `signal=${result.signal}(${elapsed}ms)`
  return `exit=0x${(result.status >>> 0).toString(16).toUpperCase().padStart(8, '0')}(${elapsed}ms)`
}

/** Reports the free space of each file system that holds one of `paths`. */
export function freeDisk(paths) {
  const parts = new Map()
  for (const path of paths) {
    if (!path)
      continue
    try {
      // statSync also rejects a missing path, on every OS.
      const { dev } = statSync(path)
      const key = process.platform === 'win32'
        ? resolve(path).slice(0, 2).toUpperCase()
        : String(dev)
      if (parts.has(key))
        continue
      const stats = statfsSync(path)
      // A drive key such as C: already ends with the separator.
      const label = process.platform === 'win32'
        ? key
        : `${path}:`
      parts.set(key, `${label}${formatGigabytes(Number(stats.bavail) * Number(stats.bsize))}`)
    }
    catch (error) {
      parts.set(path, `${path}:fail(${error.code ?? error.message})`)
    }
  }
  return [
    ...parts.values(),
  ].join(',')
}

/**
 * Composes one sample line, and on request the process tables. It takes every
 * reading as input, so a test can state each one.
 *
 * A row's parent is named only when that parent started no later than the row.
 * Windows keeps the parent PID of a process whose parent ended and gives that
 * PID to a new process, so the PID alone can name an unrelated process.
 */
export function composeSample({ time, system, rows, counted, tcp, free, canary, memoryLabel, detail, extraDetail }) {
  const byPid = new Map(rows.map(row => [row.pid, row]))
  const parentName = (row) => {
    const parent = byPid.get(row.parent)
    if (!parent || (row.start > 0 && parent.start > 0 && row.start < parent.start))
      return `pid${row.parent}`
    return parent.name
  }
  const count = name => rows.filter(row => row.name === name).length
  const tests = [
    ...new Set(rows.filter(row => row.name.endsWith('.test')).map(row => row.name.slice(0, -5))),
  ].sort().join(',')
  const runner = rows.filter(row => row.name.startsWith('Runner.')).map(row => `${row.name.slice(7)}:${formatMegabytes(row.memory)}`).join(',') || 'absent'
  const fields = [
    ...system,
    ...counted.map(name => [
      name,
      String(count(name)),
    ]),
    ['tcp', tcp],
    ['free', free],
    ['canary', canary],
    ['runner', runner],
    ['tests', tests],
  ]
  const line = [
    time,
    ...fields.map(([key, value]) => `${key}=${value}`),
  ].join(' ')
  if (!detail)
    return { line, detail: null }
  const byMemory = [...rows].sort((a, b) => b.memory - a.memory).slice(0, 12).map(row => [
    `  ${row.name.padEnd(28)}`,
    `pid=${String(row.pid).padEnd(6)}`,
    `${memoryLabel}=${formatMegabytes(row.memory).padEnd(6)}`,
    ...row.extra.map(([key, value]) => `${key}=${String(value).padEnd(6)}`),
    `parent=${parentName(row)}`,
  ].join(' '))
  const groups = new Map()
  for (const row of rows) {
    groups.set(row.name, [
      ...(groups.get(row.name) ?? []),
      row,
    ])
  }
  const byCount = [...groups].sort((a, b) => b[1].length - a[1].length).slice(0, 10).map(([name, members]) => {
    const parents = new Map()
    for (const member of members) parents.set(parentName(member), (parents.get(parentName(member)) ?? 0) + 1)
    const top = [...parents].sort((a, b) => b[1] - a[1]).slice(0, 4).map(([parent, total]) => `${parent}x${total}`).join(',')
    return `  ${name.padEnd(28)} x${String(members.length).padEnd(4)} parents=${top}`
  })
  return {
    line,
    detail: [
      `By ${memoryLabel} memory:`,
      ...byMemory,
      'By count:',
      ...byCount,
      ...(extraDetail
        ? extraDetail(rows, parentName)
        : []),
    ].join('\n'),
  }
}

function takeSample(probe, config, index) {
  const rows = probe.processes()
  let tcp
  try {
    tcp = formatTcpStates(probe.tcpStates())
  }
  catch (error) {
    tcp = `fail(${error.message})`
  }
  return composeSample({
    time: utcTime(),
    system: probe.system(rows),
    rows,
    counted: probe.counted,
    tcp,
    free: freeDisk(config.diskPaths),
    canary: runCanary(probe.canary),
    memoryLabel: probe.memoryLabel,
    detail: index % DETAIL_EVERY === 0,
    extraDetail: probe.extraDetail,
  })
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

function appendLog(config, text) {
  appendFileSync(config.logPath, `${text}\n`)
}

function readCheckRunId(config) {
  if (!existsSync(config.checkRunPath))
    return null
  return readFileSync(config.checkRunPath, 'utf8').trim() || null
}

function warn(message) {
  process.stdout.write(`::warning::${message}\n`)
}

async function start(config) {
  mkdirSync(config.directory, { recursive: true })
  for (const path of [
    config.stopPath,
    config.endedPath,
    config.checkRunPath,
    config.pidPath,
    config.logPath,
  ])
    rmSync(path, { force: true })
  try {
    const created = await callGitHub(config, 'POST', '/check-runs', {
      name: config.checkName,
      head_sha: config.headSha,
      status: 'in_progress',
      details_url: config.detailsUrl,
      output: { title: 'Starting', summary: 'The monitor publishes its first sample shortly.' },
    })
    if (created) {
      writeFileSync(config.checkRunPath, String(created.id))
      process.stdout.write(`The resource monitor publishes to check run ${created.id}: ${created.html_url}\n`)
    }
    else {
      process.stdout.write('The resource monitor has no token, so it keeps a local log only.\n')
    }
  }
  catch (error) {
    warn(`The resource monitor could not create its check run, so it keeps a local log only: ${error.message}`)
  }
  await keepStandardHandlesFromChildren()
  const sampler = spawn(process.execPath, [SCRIPT, 'run'], { detached: true, stdio: 'ignore', windowsHide: true })
  sampler.unref()
  writeFileSync(config.pidPath, String(sampler.pid))
  process.stdout.write(`Started the resource monitor as process ${sampler.pid}. Its log is ${config.logPath}.\n`)
}

const sleep = ms => new Promise(resolveSleep => setTimeout(resolveSleep, ms))

async function run(config) {
  try {
    const probe = await createProbe(process.platform)
    const header = [
      ...probe.header(),
      `image=${config.image} machine=${hostname()}`,
    ].join('\n')
    appendLog(config, header)
    const checkRunId = readCheckRunId(config)
    const lines = []
    let lastDetail = ''
    let lastPublish = 0
    let publish = 'pending'
    for (let index = 0; ; index++) {
      const stopping = existsSync(config.stopPath)
      try {
        const sample = takeSample(probe, config, index)
        const line = `${sample.line} pub=${publish}`
        appendLog(config, line)
        lines.push(line)
        if (sample.detail) {
          appendLog(config, sample.detail)
          lastDetail = sample.detail
        }
      }
      catch (error) {
        const line = `${utcTime()} sample failed: ${error.message}`
        appendLog(config, line)
        lines.push(line)
      }
      if (checkRunId && (stopping || Date.now() - lastPublish >= config.publishMs)) {
        const started = performance.now()
        try {
          const fence = '```'
          await publishOutput(config, checkRunId, {
            title: `Sample ${index + 1} at ${utcTime()} UTC`,
            summary: `${fence}\n${header}\n\n${lines.at(-1)}\n${lastDetail}\n${fence}`,
            text: `${fence}\n${lines.join('\n')}\n${fence}`,
            complete: stopping,
          })
          publish = `ok(${Math.round(performance.now() - started)}ms)`
        }
        catch (error) {
          publish = `fail(${Math.round(performance.now() - started)}ms: ${error.message})`
        }
        lastPublish = Date.now()
      }
      if (stopping) {
        appendLog(config, `The monitor ended after ${index + 1} samples. Last publish: ${publish}`)
        return
      }
      await sleep(config.sampleMs)
    }
  }
  catch (error) {
    appendLog(config, `The monitor failed: ${error.message}`)
    throw error
  }
  finally {
    writeFileSync(config.endedPath, '')
  }
}

/**
 * Reports whether a process with `pid` runs. On Linux a process that ended
 * stays a zombie until its parent reaps it, and the zombie still answers a
 * signal, so the state in /proc decides.
 */
export function isRunning(pid) {
  try {
    process.kill(pid, 0)
  }
  catch (error) {
    return error.code === 'EPERM'
  }
  if (process.platform !== 'linux')
    return true
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, 'utf8')
    return !/^[ZX]$/.test(stat.slice(stat.lastIndexOf(')') + 2).split(' ')[0])
  }
  catch {
    return false
  }
}

/**
 * Asks the sampler to end and waits for its ended marker. It never kills the
 * sampler by PID: the PID can belong to an unrelated process by then, and the
 * runner ends a leftover process when the job ends.
 */
async function stop(config) {
  if (!existsSync(config.directory)) {
    process.stdout.write('The resource monitor did not start.\n')
    return
  }
  writeFileSync(config.stopPath, '')
  const pid = existsSync(config.pidPath)
    ? Number(readFileSync(config.pidPath, 'utf8'))
    : 0
  const deadline = Date.now() + STOP_WAIT_MS
  // A sampler that no longer runs and left no marker crashed, so the wait
  // ends at once. A PID that a new process took only makes the wait longer.
  const samplerRuns = () => pid > 0 && isRunning(pid)
  while (!existsSync(config.endedPath) && Date.now() < deadline && samplerRuns())
    await sleep(250)
  const ended = existsSync(config.endedPath)
  const log = existsSync(config.logPath)
    ? readFileSync(config.logPath, 'utf8')
    : ''
  // A sampler that ended by itself completed its check run.
  const checkRunId = readCheckRunId(config)
  if (!ended) {
    warn(`The resource monitor ${pid} did not end within ${STOP_WAIT_MS / 1000} seconds of the request.`)
    if (checkRunId) {
      try {
        const fence = '```'
        await publishOutput(config, checkRunId, {
          title: 'The monitor ended early',
          summary: 'The monitor did not end when the job asked it to stop. The text holds the end of its log.',
          text: `${fence}\n${log.split('\n').slice(-200).join('\n')}\n${fence}`,
          complete: true,
        })
      }
      catch (error) {
        warn(`The resource monitor could not complete its check run: ${error.message}`)
      }
    }
  }
  process.stdout.write(`::group::Resource monitor log\n${log || 'The resource monitor wrote no log.\n'}::endgroup::\n`)
}

/**
 * Runs one mode. start and stop report a failure as a warning and succeed,
 * because diagnostics must never fail the job.
 */
export async function main(argv, env) {
  const mode = argv[0]
  if (!['start', 'run', 'stop'].includes(mode)) {
    process.stderr.write('usage: bun scripts/ci-resource-monitor.mjs start|run|stop\n')
    return 2
  }
  let config
  try {
    config = monitorConfig(env)
  }
  catch (error) {
    warn(error.message)
    return mode === 'run'
      ? 1
      : 0
  }
  if (mode === 'run') {
    await run(config)
    return 0
  }
  try {
    await (mode === 'start'
      ? start(config)
      : stop(config))
  }
  catch (error) {
    warn(`The resource monitor did not ${mode} cleanly: ${error.message}`)
  }
  return 0
}

if (import.meta.main)
  process.exit(await main(process.argv.slice(2), process.env))
