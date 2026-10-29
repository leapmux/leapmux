// Tests for ci-resource-monitor.mjs. task test-ci-monitor runs them on each
// native CI runner, because each OS reads its resources through its own probe.

import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import {
  composeSample,
  cpuPercent,
  createProbe,
  formatTcpStates,
  freeDisk,
  isRunning,
  limitText,
  main,
  MAX_OUTPUT_CHARACTERS,
  monitorConfig,
  parseMeminfo,
  parseNetstat,
  parseProcNetTcp,
  parseProcStat,
  parseProcStatCpu,
  parsePs,
  parseSwapSize,
  parseSysctl,
  parseVmStat,
  runCanary,
} from './ci-resource-monitor.mjs'

const root = resolve(import.meta.dirname, '..')
const script = resolve(import.meta.dirname, 'ci-resource-monitor.mjs')
const cleanups = []

afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse())
    await cleanup()
})

function temporaryDirectory() {
  mkdirSync(join(root, '.tmp'), { recursive: true })
  const directory = mkdtempSync(join(root, '.tmp', 'ci-resource-monitor-'))
  cleanups.push(() => rmSync(directory, { recursive: true, force: true }))
  return directory
}

describe('parseProcStat', () => {
  it('reads the fields after a command name that holds spaces and parentheses', () => {
    const fields = [
      'S',
      '41',
      ...Array.from({ length: 19 }, (_, index) => String(index)),
      '345',
      '1',
    ]
    expect(parseProcStat(`1234 (a (weird) name) ${fields.join(' ')}`)).toEqual({ comm: 'a (weird) name', parent: 41, rssPages: 345 })
  })

  it('rejects text without a command name', () => {
    expect(parseProcStat('1234 S 41')).toBeNull()
  })
})

describe('parseMeminfo', () => {
  it('converts kilobyte values to bytes and keeps plain counts', () => {
    const values = parseMeminfo('MemTotal:       16384 kB\nMemAvailable:    8192 kB\nHugePages_Total:       0\n')
    expect(values.get('MemTotal')).toBe(16384 * 1024)
    expect(values.get('MemAvailable')).toBe(8192 * 1024)
    expect(values.get('HugePages_Total')).toBe(0)
  })
})

describe('cpuPercent', () => {
  it('reports the busy share between two readings of /proc/stat', () => {
    const first = parseProcStatCpu('cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3\n')
    const second = parseProcStatCpu('cpu  200 0 200 1300 300 0 0 0 0 0\n')
    expect(first).toEqual({ idle: 800, total: 1000 })
    expect(cpuPercent(first, second)).toBe('20%')
  })

  it('reports no figure for the first reading or an unchanged total', () => {
    const reading = { idle: 5, total: 10 }
    expect(cpuPercent(null, reading)).toBe('n/a')
    expect(cpuPercent(reading, reading)).toBe('n/a')
  })
})

describe('parseProcNetTcp', () => {
  it('counts IPv4 and IPv6 connections by state', () => {
    const header = '  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n'
    const counts = parseProcNetTcp(`${header}   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 1\n   1: 0100007F:A1B2 0100007F:1F90 06 00000000:00000000 00:00000000 00000000  1000 0 2\n`)
    parseProcNetTcp(`${header}   0: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 01 0 0 0 0 0 3\n`, counts)
    expect([...counts]).toEqual([
      ['Listen', 1],
      ['TimeWait', 1],
      ['Established', 1],
    ])
  })
})

describe('parsePs', () => {
  it('reads each row and keeps the base name of a path that holds spaces', () => {
    const rows = parsePs('    1     0  12000   0.5 /sbin/launchd\n  321     1   2048  12.0 /Applications/Some App.app/Contents/MacOS/Some App\n')
    expect(rows).toEqual([
      {
        name: 'launchd',
        pid: 1,
        parent: 0,
        memory: 12000 * 1024,
        cpu: 0.5,
        start: 0,
        extra: [],
      },
      {
        name: 'Some App',
        pid: 321,
        parent: 1,
        memory: 2048 * 1024,
        cpu: 12,
        start: 0,
        extra: [],
      },
    ])
  })
})

describe('parseVmStat', () => {
  it('reads the page size and the page counts', () => {
    const { pageSize, pages } = parseVmStat('Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                               12345.\nPages occupied by compressor:              678.\n')
    expect(pageSize).toBe(16384)
    expect(pages.get('Pages free')).toBe(12345)
    expect(pages.get('Pages occupied by compressor')).toBe(678)
  })
})

describe('parseSysctl', () => {
  it('reads named values, and parseSwapSize reads the swap sizes', () => {
    const values = parseSysctl('hw.memsize: 17179869184\nvm.swapusage: total = 2048.00M  used = 1536.50M  free = 511.50M  (encrypted)\n')
    expect(values.get('hw.memsize')).toBe('17179869184')
    expect(parseSwapSize(values.get('vm.swapusage'), 'total')).toBe(2048 * 2 ** 20)
    expect(parseSwapSize(values.get('vm.swapusage'), 'used')).toBe(1536.5 * 2 ** 20)
    expect(parseSwapSize(undefined, 'used')).toBe(0)
  })
})

describe('parseNetstat', () => {
  it('counts the TCP connections by state and skips other lines', () => {
    const counts = parseNetstat([
      'Active Internet connections (including servers)',
      'Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)',
      'tcp4       0      0  127.0.0.1.8080         127.0.0.1.50000        ESTABLISHED',
      'tcp6       0      0  *.22                   *.*                    LISTEN',
      'tcp4       0      0  127.0.0.1.50001        127.0.0.1.8080         TIME_WAIT',
      'udp4       0      0  *.5353                 *.*',
    ].join('\n'))
    expect([...counts]).toEqual([
      ['Established', 1],
      ['Listen', 1],
      ['TimeWait', 1],
    ])
  })
})

describe('formatTcpStates', () => {
  it('puts the most common state first, and states none for no connection', () => {
    expect(formatTcpStates(new Map([
      ['Listen', 1],
      ['TimeWait', 30],
    ]))).toBe('TimeWait:30,Listen:1')
    expect(formatTcpStates(new Map())).toBe('none')
  })
})

describe('limitText', () => {
  it('keeps text at the limit and keeps the end of longer text', () => {
    const exact = 'x'.repeat(MAX_OUTPUT_CHARACTERS)
    expect(limitText(exact)).toBe(exact)
    const longer = `head${'y'.repeat(MAX_OUTPUT_CHARACTERS)}`
    expect(limitText(longer)).toBe(`...${'y'.repeat(MAX_OUTPUT_CHARACTERS)}`)
  })
})

describe('monitorConfig', () => {
  it('requires RUNNER_TEMP', () => {
    expect(() => monitorConfig({})).toThrow('RUNNER_TEMP')
  })

  it('names the check run after the runner and falls back to the pushed commit', () => {
    const config = monitorConfig({
      RUNNER_TEMP: '/tmp/runner',
      RUNNER_OS: 'Linux',
      RUNNER_ARCH: 'ARM64',
      GITHUB_SHA: 'pushed',
      GITHUB_API_URL: 'https://api.example.test/',
    })
    expect(config.checkName).toBe('Linux ARM64 resource monitor')
    expect(config.headSha).toBe('pushed')
    expect(config.apiUrl).toBe('https://api.example.test')
    expect(config.logPath).toBe(join('/tmp/runner', 'leapmux-resource-monitor', 'monitor.log'))
  })

  it('prefers the pull request head and rejects a sample interval that is not positive', () => {
    const config = monitorConfig({
      RUNNER_TEMP: '/tmp/runner',
      MONITOR_HEAD_SHA: 'head',
      GITHUB_SHA: 'merge',
      MONITOR_SAMPLE_SECONDS: '0',
      MONITOR_PUBLISH_SECONDS: 'soon',
    })
    expect(config.headSha).toBe('head')
    expect(config.sampleMs).toBe(10_000)
    expect(config.publishMs).toBe(30_000)
  })
})

describe('composeSample', () => {
  const rows = [
    {
      name: 'Runner.Worker',
      pid: 10,
      parent: 1,
      memory: 50 * 2 ** 20,
      start: 100,
      extra: [],
    },
    {
      name: 'go',
      pid: 20,
      parent: 10,
      memory: 500 * 2 ** 20,
      start: 200,
      extra: [],
    },
    {
      name: 'amp.test',
      pid: 30,
      parent: 20,
      memory: 80 * 2 ** 20,
      start: 300,
      extra: [],
    },
    {
      name: 'amp.test',
      pid: 31,
      parent: 20,
      memory: 70 * 2 ** 20,
      start: 310,
      extra: [],
    },
    // winlogon started at boot, and pwsh later took the PID of its ended parent.
    {
      name: 'winlogon',
      pid: 40,
      parent: 50,
      memory: 2 ** 20,
      start: 1,
      extra: [],
    },
    {
      name: 'pwsh',
      pid: 50,
      parent: 10,
      memory: 90 * 2 ** 20,
      start: 400,
      extra: [],
    },
  ]
  const sample = detail => composeSample({
    time: '12:00:00',
    system: [
      ['cpu', '50%'],
    ],
    rows,
    counted: ['pwsh', 'git'],
    tcp: 'Established:2',
    free: '/:10.0G',
    canary: 'ok(5ms)',
    memoryLabel: 'rss',
    detail,
    extraDetail: (all, parentName) => [
      'Extra:',
      ...all.filter(row => row.name === 'winlogon').map(row => `  ${row.name} parent=${parentName(row)}`),
    ],
  })

  it('states each reading on one line', () => {
    const { line, detail } = sample(false)
    expect(line).toBe('12:00:00 cpu=50% pwsh=1 git=0 tcp=Established:2 free=/:10.0G canary=ok(5ms) runner=Worker:50M tests=amp')
    expect(detail).toBeNull()
  })

  it('names no parent that started after its child', () => {
    const { detail } = sample(true)
    expect(detail).toContain('By rss memory:')
    expect(detail).toContain('By count:')
    expect(detail).toMatch(/amp\.test\s+x2\s+parents=gox2/)
    expect(detail).toContain('  winlogon parent=pid50')
    expect(detail).toMatch(/pwsh\s+pid=50\s+rss=90M\s+parent=Runner\.Worker/)
  })

  it('reports an absent runner', () => {
    const { line } = composeSample({
      time: 't',
      system: [],
      rows: [],
      counted: [],
      tcp: 'none',
      free: '',
      canary: 'ok',
      memoryLabel: 'rss',
      detail: false,
    })
    expect(line).toContain('runner=absent tests=')
  })
})

describe('freeDisk', () => {
  it('reports each file system once and a missing path as a failure', () => {
    const directory = temporaryDirectory()
    const report = freeDisk([
      directory,
      join(directory, '.'),
      join(directory, 'missing'),
    ])
    expect(report.split(',')).toHaveLength(2)
    expect(report).toMatch(/:\d+\.\dG/)
    expect(report).toContain('fail(ENOENT)')
  })
})

describe('the probe of this machine', () => {
  it('reads the processes with their parents, the system and the TCP connections', async () => {
    const probe = await createProbe(process.platform)
    expect(probe.header().length).toBeGreaterThan(1)
    const rows = probe.processes()
    const self = rows.find(row => row.pid === process.pid)
    expect(self, 'the probe lists this process').toBeDefined()
    expect(self.parent).toBe(process.ppid)
    expect(self.memory).toBeGreaterThan(0)
    const system = new Map(probe.system(rows))
    expect(system.has('cpu')).toBe(true)
    expect(Number(system.get('procs'))).toBeGreaterThanOrEqual(1)
    let tcp
    try {
      tcp = probe.tcpStates()
    }
    catch (error) {
      // A sandboxed macOS hides the socket list from netstat.
      expect(process.platform).toBe('darwin')
      expect(error.message).toBe('netstat listed no TCP table')
      return
    }
    expect(tcp).toBeInstanceOf(Map)
  })

  it('starts a canary process', async () => {
    const probe = await createProbe(process.platform)
    expect(runCanary(probe.canary)).toMatch(/^ok\(\d+ms\)$/)
    expect(runCanary(['definitely-no-such-command-for-the-monitor', []])).toMatch(/^fail\(/)
  })
})

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

function fakeGitHub({ failCreate = false } = {}) {
  const requests = []
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    async fetch(request) {
      requests.push({
        method: request.method,
        path: new URL(request.url).pathname,
        authorization: request.headers.get('authorization'),
        body: await request.json(),
      })
      if (failCreate)
        return new Response('unavailable', { status: 503 })
      if (request.method === 'POST')
        return Response.json({ id: 42, html_url: 'https://github.test/checks/42' }, { status: 201 })
      return Response.json({ id: 42 })
    },
  })
  cleanups.push(() => server.stop(true))
  return { requests, url: `http://127.0.0.1:${server.port}` }
}

function monitorEnv(directory, apiUrl, token = 'test-token') {
  const env = {
    ...process.env,
    RUNNER_TEMP: directory,
    GITHUB_REPOSITORY: 'leapmux/leapmux',
    GITHUB_API_URL: apiUrl,
    GITHUB_SERVER_URL: 'https://github.test',
    GITHUB_RUN_ID: '7',
    RUNNER_OS: 'TestOS',
    RUNNER_ARCH: 'X64',
    MONITOR_HEAD_SHA: 'head-sha',
    MONITOR_SAMPLE_SECONDS: '1',
    MONITOR_PUBLISH_SECONDS: '1',
  }
  delete env.GH_TOKEN
  if (token)
    env.GH_TOKEN = token
  return env
}

/**
 * Runs one mode and returns its output. The output ends only when every
 * process that holds it ends, so a sampler that inherited the step's pipe
 * keeps this read open and fails the deadline.
 */
async function runMode(mode, env) {
  const child = spawn(process.execPath, [script, mode], {
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const collect = stream => new Promise((resolveText) => {
    let text = ''
    stream.setEncoding('utf8')
    stream.on('data', (chunk) => {
      text += chunk
    })
    stream.on('end', () => resolveText(text))
  })
  const finished = Promise.all([
    collect(child.stdout),
    collect(child.stderr),
    new Promise(resolveCode => child.on('close', resolveCode)),
  ])
  let timer
  const deadline = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${mode} did not release its output within 20 seconds; the sampler may hold the pipe`)), 20_000)
  })
  try {
    const [stdout, stderr, code] = await Promise.race([finished, deadline])
    return { stdout, stderr, code }
  }
  finally {
    clearTimeout(timer)
  }
}

async function waitFor(condition, message) {
  const deadline = Date.now() + 20_000
  while (!condition()) {
    if (Date.now() > deadline)
      throw new Error(message)
    await Bun.sleep(100)
  }
}

/** Stops a sampler that a failed test left running. */
function stopLater(env) {
  cleanups.push(async () => {
    const config = monitorConfig(env)
    if (existsSync(config.directory) && !existsSync(config.endedPath))
      await runMode('stop', env)
  })
}

describe('the monitor lifecycle', () => {
  it('detaches a sampler that publishes to its check run, and stop completes the run', async () => {
    const github = fakeGitHub()
    const env = monitorEnv(temporaryDirectory(), github.url)
    stopLater(env)
    const started = await runMode('start', env)
    expect(started.code).toBe(0)
    expect(started.stdout).toContain('publishes to check run 42')
    expect(github.requests[0]).toMatchObject({
      method: 'POST',
      path: '/repos/leapmux/leapmux/check-runs',
      authorization: 'Bearer test-token',
      body: {
        name: 'TestOS X64 resource monitor',
        head_sha: 'head-sha',
        status: 'in_progress',
        details_url: 'https://github.test/leapmux/leapmux/actions/runs/7',
      },
    })

    await waitFor(() => github.requests.filter(request => request.method === 'PATCH').length >= 2, 'the sampler published no samples')
    const update = github.requests.find(request => request.method === 'PATCH')
    expect(update.path).toBe('/repos/leapmux/leapmux/check-runs/42')
    expect(update.body.output.title).toMatch(/^Sample \d+ at \d\d:\d\d:\d\d UTC$/)
    expect(update.body.output.text).toMatch(/cpu=\S+ .*canary=ok\(\d+ms\)/)
    expect(update.body.status).toBeUndefined()

    const stopped = await runMode('stop', env)
    expect(stopped.code).toBe(0)
    expect(stopped.stdout).toContain('::group::Resource monitor log')
    expect(stopped.stdout).toMatch(/The monitor ended after \d+ samples\. Last publish: ok\(\d+ms\)/)
    expect(stopped.stdout).not.toContain('::warning::')
    const last = github.requests.at(-1)
    expect(last.method).toBe('PATCH')
    expect(last.body).toMatchObject({ status: 'completed', conclusion: 'neutral' })
  })

  it('keeps a local log when the job supplies no token', async () => {
    const github = fakeGitHub()
    const env = monitorEnv(temporaryDirectory(), github.url, '')
    stopLater(env)
    const started = await runMode('start', env)
    expect(started.stdout).toContain('keeps a local log only')
    await waitFor(() => existsSync(monitorConfig(env).logPath) && readFileSync(monitorConfig(env).logPath, 'utf8').includes('canary='), 'the sampler wrote no sample')
    const stopped = await runMode('stop', env)
    expect(stopped.stdout).toContain('canary=')
    expect(github.requests).toEqual([])
  })

  it('warns when GitHub refuses the check run, and still samples', async () => {
    const github = fakeGitHub({ failCreate: true })
    const env = monitorEnv(temporaryDirectory(), github.url)
    stopLater(env)
    const started = await runMode('start', env)
    expect(started.code).toBe(0)
    expect(started.stdout).toContain('::warning::The resource monitor could not create its check run')
    expect(started.stdout).toContain('503')
    await waitFor(() => existsSync(monitorConfig(env).logPath) && readFileSync(monitorConfig(env).logPath, 'utf8').includes('canary='), 'the sampler wrote no sample')
    const stopped = await runMode('stop', env)
    expect(stopped.code).toBe(0)
    expect(github.requests).toHaveLength(1)
  })

  it('completes the check run itself when the sampler ended without its marker', async () => {
    const github = fakeGitHub()
    const env = monitorEnv(temporaryDirectory(), github.url)
    stopLater(env)
    await runMode('start', env)
    await waitFor(() => github.requests.some(request => request.method === 'PATCH'), 'the sampler published no sample')
    const config = monitorConfig(env)
    const pid = Number(readFileSync(config.pidPath, 'utf8'))
    process.kill(pid, 'SIGKILL')
    await waitFor(() => !isRunning(pid), 'the killed sampler kept running')

    const stopped = await runMode('stop', env)
    expect(stopped.code).toBe(0)
    expect(stopped.stdout).toContain(`::warning::The resource monitor ${pid} did not end`)
    expect(github.requests.at(-1).body).toMatchObject({
      status: 'completed',
      conclusion: 'neutral',
      output: { title: 'The monitor ended early' },
    })
    expect(github.requests.at(-1).body.output.text).toContain('canary=')
  })

  it('reports a monitor that never started', async () => {
    const stopped = await runMode('stop', monitorEnv(temporaryDirectory(), 'http://127.0.0.1:9'))
    expect(stopped.code).toBe(0)
    expect(stopped.stdout).toBe('The resource monitor did not start.\n')
  })
})

describe('isRunning', () => {
  it('reports this process as running and an ended one as not', async () => {
    expect(isRunning(process.pid)).toBe(true)
    const child = spawn(process.execPath, ['-e', ''], { stdio: 'ignore' })
    await new Promise(resolveExit => child.on('exit', resolveExit))
    expect(isRunning(child.pid)).toBe(false)
  })
})

describe('main', () => {
  it('rejects an unknown mode, and only warns when RUNNER_TEMP is absent', async () => {
    expect(await main(['restart'], {})).toBe(2)
    expect(await main(['start'], {})).toBe(0)
    expect(await main(['stop'], {})).toBe(0)
    expect(await main(['run'], {})).toBe(1)
  })
})
