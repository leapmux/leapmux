// Tests for ci-resource-monitor.mjs. task test-ci-monitor runs them on each
// native CI runner, because each OS reads its resources through its own probe.

import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import {
  appendTextTail,
  composeSample,
  createProbe,
  formatTcpStates,
  freeDisk,
  isRunning,
  limitText,
  main,
  MAX_OUTPUT_CHARACTERS,
  monitorConfig,
  run,
  runCanary,
  takeSample,
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

describe('appendTextTail', () => {
  it('preserves short, empty and exact-limit text', () => {
    expect(appendTextTail('', '')).toBe('')
    expect(appendTextTail('first\n', 'last\n')).toBe('first\nlast\n')
    const exact = 'x'.repeat(MAX_OUTPUT_CHARACTERS)
    expect(appendTextTail('', exact)).toBe(exact)
    expect(appendTextTail(exact, '')).toBe(exact)
  })

  it('keeps the same published suffix while limiting retained history', () => {
    let tail = ''
    let complete = ''
    for (let index = 0; index < 2160; index++) {
      const line = `${String(index).padStart(4, '0')}:${'x'.repeat(460)}\n`
      complete += line
      tail = appendTextTail(tail, line)
      expect(tail.length).toBeLessThanOrEqual(MAX_OUTPUT_CHARACTERS)
      if (index % 100 === 0 || index === 2159)
        expect(limitText(`\`\`\`\n${tail}\`\`\``)).toBe(limitText(`\`\`\`\n${complete}\`\`\``))
    }
    expect(tail).not.toContain('0000:')
    expect(tail).toContain('2159:')
    expect(appendTextTail('old', 'y'.repeat(MAX_OUTPUT_CHARACTERS + 1))).toBe('y'.repeat(MAX_OUTPUT_CHARACTERS))
    expect(appendTextTail(tail, 'sample failed\n')).toEndWith('sample failed\n')
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
    // One separator follows the label: a Windows drive key such as C: ends with it.
    expect(report).toMatch(/^[^,]*[^:]:\d+\.\dG,/)
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
    expect(probe.parentStartTimesRequired === true).toBe(process.platform === 'win32')
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

function fakeGitHub({ failCreate = false, failCompletion = 0 } = {}) {
  const requests = []
  let state = 'in_progress'
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
      const body = requests.at(-1).body
      if (body.status === 'completed' && failCompletion-- > 0)
        return new Response('completion unavailable', { status: 503 })
      if (body.status)
        state = body.status
      if (request.method === 'POST')
        return Response.json({ id: 42, html_url: 'https://github.test/checks/42' }, { status: 201 })
      return Response.json({ id: 42 })
    },
  })
  cleanups.push(() => server.stop(true))
  return {
    requests,
    get state() { return state },
    url: `http://127.0.0.1:${server.port}`,
  }
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

/** Runs instrumentation without changing the test runner's built-ins. */
async function runIsolatedProbe(source) {
  const child = Bun.spawn([process.execPath, '--eval', source], { stdin: 'ignore', stdout: 'pipe', stderr: 'pipe' })
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ])
  expect(code, stderr).toBe(0)
  return JSON.parse(stdout)
}

function sampleRows(rows, options = {}) {
  return composeSample({
    time: 't',
    system: [],
    rows,
    counted: [],
    tcp: 'none',
    free: '',
    canary: 'ok',
    memoryLabel: 'rss',
    detail: true,
    ...options,
  })
}

function preparedMonitor(github, stopping = false) {
  const env = monitorEnv(temporaryDirectory(), github.url)
  const config = monitorConfig(env)
  mkdirSync(config.directory, { recursive: true })
  writeFileSync(config.checkRunPath, '42')
  if (stopping)
    writeFileSync(config.stopPath, '')
  return { env, config }
}

describe('monitor completion recovery', () => {
  it.each(['factory', 'header'])('completes the check after the probe %s fails', async (failure) => {
    const github = fakeGitHub()
    const { env, config } = preparedMonitor(github)
    const unavailable = () => {
      throw new Error('probe unavailable')
    }
    await expect(run(config, failure === 'factory'
      ? unavailable
      : () => ({ header: unavailable }))).rejects.toThrow('probe unavailable')
    expect(existsSync(config.endedPath)).toBe(true)
    expect(await main(['stop'], env)).toBe(0)
    expect(github.state).toBe('completed')
    expect(github.requests.at(-1).body.output.text).toContain('probe unavailable')
  })

  it('retries a rejected final publication from the stop handler', async () => {
    const github = fakeGitHub({ failCompletion: 1 })
    const { env, config } = preparedMonitor(github, true)
    await run(config)
    expect(github.state).toBe('in_progress')
    expect(await main(['stop'], env)).toBe(0)
    expect(github.state).toBe('completed')
    expect(github.requests.filter(request => request.body.status === 'completed')).toHaveLength(2)
    const count = github.requests.length
    expect(await main(['stop'], env)).toBe(0)
    expect(github.requests).toHaveLength(count)
  })

  it('warns without recording a PID when the detached sampler cannot start', async () => {
    const directory = temporaryDirectory()
    const env = monitorEnv(directory, 'http://127.0.0.1:9', '')
    const source = `import { main } from ${JSON.stringify(script)}; process.execPath = ${JSON.stringify(join(directory, 'absent-bun'))}; process.exit(await main(['start'], process.env))`
    const child = Bun.spawn([process.execPath, '--eval', source], { env, stdout: 'pipe', stderr: 'pipe' })
    const [stdout, stderr, code] = await Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
      child.exited,
    ])
    expect(code, stderr).toBe(0)
    expect(stdout).toContain('::warning::The resource monitor did not start cleanly')
    expect(stdout).not.toContain('Started the resource monitor')
    expect(existsSync(monitorConfig(env).pidPath)).toBe(false)
  })
})

describe('composeSample process grouping', () => {
  it('groups same-name processes without repeatedly traversing prior members', async () => {
    const rows = Array.from({ length: 64 }, (_, index) => ({
      name: 'worker',
      pid: 1000 + index,
      parent: index < 32
        ? 0
        : 100,
      memory: 1,
      start: 0,
      extra: [],
    }))
    const sample = await runIsolatedProbe(`
      import { composeSample } from ${JSON.stringify(script)};
      const options = ${JSON.stringify({
        time: 't',
        system: [],
        counted: [],
        tcp: 'none',
        free: '',
        canary: 'ok',
        memoryLabel: 'rss',
        detail: true,
      })};
      const rows = ${JSON.stringify(rows)};
      const objects = new WeakSet(rows);
      const iterate = Array.prototype[Symbol.iterator];
      let visits = 0;
      Array.prototype[Symbol.iterator] = function* () {
        for (const value of iterate.call(this)) {
          if (objects.has(value)) visits++;
          yield value;
        }
      };
      const sample = composeSample({ ...options, rows });
      process.stdout.write(JSON.stringify({ ...sample, visits }));
    `)
    expect(sample.detail).toMatch(/worker\s+x64\s+parents=pid0x32,pid100x32/)
    expect(sample.visits).toBeGreaterThan(0)
    expect(sample.visits).toBeLessThanOrEqual(4 * rows.length)
  })

  it.each([
    [0, 400],
    [1, 0],
    [0, 0],
    [undefined, 400],
    [1, undefined],
    [-1, 400],
  ])('keeps the PID when required creation times are unavailable: %j', (child, parent) => {
    const rows = [
      {
        name: 'pwsh',
        pid: 50,
        parent: 1,
        memory: 1,
        start: parent,
        extra: [],
      },
      {
        name: 'winlogon',
        pid: 60,
        parent: 50,
        memory: 1,
        start: child,
        extra: [],
      },
    ]
    const result = sampleRows(rows, {
      parentStartTimesRequired: true,
      extraDetail: (items, parentName) => [`observed=${parentName(items[1])}`],
    })
    expect(result.detail).toMatch(/winlogon\s+pid=60\s+rss=0M\s+parent=pid50/)
    expect(result.detail).toMatch(/winlogon\s+x1\s+parents=pid50x1/)
    expect(result.detail).toContain('observed=pid50')
  })

  it.each([
    [200, 100, true],
    [100, 100, true],
    [0, 0, false],
  ])('keeps established parent names: %j', (child, parent, required) => {
    const rows = [
      {
        name: 'shell',
        pid: 50,
        parent: 1,
        memory: 1,
        start: parent,
        extra: [],
      },
      {
        name: 'work',
        pid: 60,
        parent: 50,
        memory: 1,
        start: child,
        extra: [],
      },
    ]
    expect(sampleRows(rows, { parentStartTimesRequired: required }).detail).toMatch(/work\s+pid=60\s+rss=0M\s+parent=shell/)
  })
})

describe('takeSample', () => {
  it('forwards the requirement for known parent creation times', () => {
    const probe = {
      processes: () => [
        {
          name: 'pwsh',
          pid: 50,
          parent: 1,
          start: 400,
          memory: 1,
          extra: [],
        },
        {
          name: 'winlogon',
          pid: 60,
          parent: 50,
          start: 0,
          memory: 1,
          extra: [],
        },
      ],
      system: () => [],
      tcpStates: () => new Map(),
      canary: [
        process.execPath,
        ['-e', ''],
      ],
      counted: [],
      memoryLabel: 'rss',
      parentStartTimesRequired: true,
    }
    expect(takeSample(probe, { diskPaths: [] }, 0).detail).toMatch(/winlogon\s+pid=60\s+rss=0M\s+parent=pid50/)
  })

  it.each([
    'processes',
    'system',
    'both',
    'tcp',
    'none',
  ])('retains independent readings when %s collection fails', (failure) => {
    const directory = temporaryDirectory()
    const marker = join(directory, 'canary-ran')
    const probe = {
      processes() {
        if (failure === 'processes' || failure === 'both')
          throw new Error('process table unavailable')
        return []
      },
      system(rows) {
        if (failure === 'system' || failure === 'both')
          throw new Error('system unavailable')
        expect(rows).toEqual(failure === 'processes'
          ? null
          : [])
        return [
          ['avail', '1G'],
        ]
      },
      tcpStates() {
        if (failure === 'tcp')
          throw new Error('TCP unavailable')
        return new Map([
          ['Listen', 2],
        ])
      },
      counted: ['worker'],
      memoryLabel: 'rss',
      canary: [
        process.execPath,
        ['-e', `require('node:fs').writeFileSync(${JSON.stringify(marker)}, '')`],
      ],
    }
    const sample = takeSample(probe, {
      diskPaths: [directory],
    }, 0)
    expect(existsSync(marker)).toBe(true)
    expect(sample.line).toMatch(/canary=ok\(\d+ms\)/)
    expect(sample.line).toMatch(/free=\S+\d\.\dG/)
    if (failure === 'processes' || failure === 'both') {
      expect(sample.line).toContain('worker=unavailable')
      expect(sample.line).toContain('runner=unavailable')
      expect(sample.detail).toContain('Process table unavailable')
    }
    else {
      expect(sample.line).toContain('worker=0')
      expect(sample.line).toContain('runner=absent')
    }
    if (failure !== 'system' && failure !== 'both')
      expect(sample.line).toContain('avail=1G')
    else
      expect(sample.line).toContain('system=fail(system unavailable)')
    expect(sample.line).toContain(failure === 'tcp'
      ? 'tcp=fail(TCP unavailable)'
      : 'tcp=Listen:2')
  })
})

describe('run publication history', () => {
  it('keeps the full disk log without joining obsolete publication history', async () => {
    const github = fakeGitHub()
    const { config } = preparedMonitor(github)
    config.sampleMs = 1
    config.publishMs = 1
    const { largestJoin, controlJoin } = await runIsolatedProbe(`
      import { run } from ${JSON.stringify(script)};
      import { writeFileSync } from 'node:fs';
      const config = ${JSON.stringify(config)};
      let samples = 0;
      const probe = {
        header: () => ['history probe'],
        processes() {
          samples++;
          if (samples === 8) writeFileSync(config.stopPath, '');
          return [{ name: 'Runner.tail-probe-' + samples + '-' + 'x'.repeat(20000), pid: 10, parent: 1, memory: 1, start: 1, extra: [] }];
        },
        system: () => [], tcpStates: () => new Map(), counted: [], memoryLabel: 'rss', canary: [process.execPath, ['-e', '']],
      };
      const joinValues = Array.prototype.join;
      let largestJoin = 0;
      Array.prototype.join = function (separator) {
        if (separator === '\\n' && typeof this[0] === 'string' && this[0].includes('runner=tail-probe-'))
          largestJoin = Math.max(largestJoin, this.reduce((size, value) => size + value.length + 1, 0));
        return joinValues.call(this, separator);
      };
      ['runner=tail-probe-control'].join('\\n');
      const controlJoin = largestJoin;
      largestJoin = 0;
      await run(config, () => probe);
      process.stdout.write(JSON.stringify({ largestJoin, controlJoin }));
    `)
    expect(controlJoin).toBeGreaterThan(0)
    expect(largestJoin).toBeLessThanOrEqual(MAX_OUTPUT_CHARACTERS + 21000)
    const log = readFileSync(config.logPath, 'utf8')
    expect(log).toContain('runner=tail-probe-1-')
    expect(log).toContain('runner=tail-probe-9-')
    expect(github.state).toBe('completed')
    expect(github.requests.at(-1).body.output.text).not.toContain('runner=tail-probe-1-')
    expect(github.requests.at(-1).body.output.text).toContain('runner=tail-probe-9-')
  })
})
