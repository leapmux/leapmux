import { Buffer } from 'node:buffer'
import { availableParallelism, release } from 'node:os'
import process from 'node:process'
import { cpuPercent, formatGigabytes, formatMegabytes } from './metrics.mjs'

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
    GetLastError: { args: [], returns: 'u32' },
    SetLastError: {
      args: ['u32'],
      returns: 'void',
    },
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
export async function keepStandardHandlesFromChildren() {
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

/** Reads the USER and GDI resource counts of a process. */
export function readGuiResources(user32, handle, { kernel32, targetSession, monitorSession }) {
  if (!(targetSession >= 0 && monitorSession >= 0 && targetSession === monitorSession))
    return { user: -1, gdi: -1 }
  // The linter interprets direct calls with these Win32 names as error
  // constructors. Lowercase bindings preserve native function calls.
  const { SetLastError: setLastError, GetLastError: getLastError } = kernel32
  const read = (kind) => {
    setLastError(0)
    const count = user32.GetGuiResources(handle, kind)
    return count === 0 && getLastError() !== 0
      ? -1
      : count
  }
  return {
    user: read(1),
    gdi: read(0),
  }
}

/** Formats the observed USER resource counts by session. */
export function formatUserResources(rows) {
  if (rows === null)
    return 'unavailable'
  const userBySession = new Map()
  for (const row of rows) {
    let counts = userBySession.get(row.session)
    if (!counts) {
      counts = { total: 0, known: 0, unknown: 0 }
      userBySession.set(row.session, counts)
    }
    const user = extraValue(row, 'user')
    if (user >= 0) {
      counts.total += user
      counts.known++
    }
    else {
      counts.unknown++
    }
  }
  return [...userBySession].sort((a, b) => a[0] - b[0]).map(([session, counts]) => {
    const label = session >= 0
      ? `s${session}`
      : 'unknown-session'
    const observed = counts.known
      ? `${counts.total}${counts.unknown
        ? `(unknown=${counts.unknown})`
        : ''}`
      : 'unknown'
    return `${label}:${observed}`
  }).join(',') || 'none'
}

export async function windowsProbe() {
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

  const monitorSession = sessionOf(process.pid)

  function details(pid, targetSession) {
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
      Object.assign(result, readGuiResources(user32, handle, { kernel32, targetSession, monitorSession }))
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
    parentStartTimesRequired: true,
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
        `desktop=${desktop()} session=${monitorSession}`,
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
          'user-observed',
          formatUserResources(rows),
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
          const session = sessionOf(pid)
          const facts = details(pid, session)
          rows.push({
            name,
            pid,
            parent: view.getUint32(32, true),
            memory: facts.memory,
            start: facts.start,
            session,
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
