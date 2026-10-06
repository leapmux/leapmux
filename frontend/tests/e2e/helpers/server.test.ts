import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import ts from 'typescript'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { E2E_BROWSER_HOST, hubSpawnEnv, hubUrlFromStateJson, mockAgentEnv, resolvedHubTCPFromStateJson, waitForHubStart, waitForServer } from './server'

describe('E2E_BROWSER_HOST', () => {
  it('matches the browser session-cookie domain', () => {
    expect(E2E_BROWSER_HOST).toBe('localhost')
  })
})

describe('hubUrlFromStateJson', () => {
  it('takes the assigned port from the one TCP entry', () => {
    const state = JSON.stringify({ pid: 7, listen: ['127.0.0.1:44321', 'unix:/tmp/hub.sock'] })
    expect(resolvedHubTCPFromStateJson(state)).toBe('127.0.0.1:44321')
    expect(hubUrlFromStateJson(state)).toBe('http://localhost:44321')
    expect(hubUrlFromStateJson(state, '127.0.0.1')).toBe('http://127.0.0.1:44321')
  })

  it('rejects a bind set with no TCP entry', () => {
    expect(() => hubUrlFromStateJson(JSON.stringify({ listen: ['unix:/tmp/hub.sock'] })))
      .toThrow(/expected one TCP address/)
  })

  it('rejects a bind set with more than one TCP entry', () => {
    expect(() => hubUrlFromStateJson(JSON.stringify({ listen: ['127.0.0.1:1', '127.0.0.1:2'] })))
      .toThrow(/expected one TCP address/)
  })

  it.each(['127.0.0.1:', '127.0.0.1:0', '127.0.0.1:65536', '127.0.0.1:1e3'])('rejects a TCP address without a usable port: %s', (address) => {
    expect(() => hubUrlFromStateJson(JSON.stringify({ listen: [address] })))
      .toThrow(/names no usable port/)
  })

  it('rejects an unreadable listen set', () => {
    expect(() => hubUrlFromStateJson('{}')).toThrow(/expected one TCP address/)
    expect(() => hubUrlFromStateJson('{"listen":[7]}')).toThrow(/expected one TCP address/)
    expect(() => hubUrlFromStateJson('null')).toThrow(/expected one TCP address/)
  })
})

describe('hubSpawnEnv', () => {
  it('removes inherited plain HTTP proxies after applying the mock environment', () => {
    const proxyNames = ['HTTP_PROXY', 'http_proxy', 'ALL_PROXY', 'all_proxy'] as const
    const before = proxyNames.map(name => process.env[name])
    for (const name of proxyNames)
      process.env[name] = 'http://parent-proxy.invalid:8080'
    try {
      const mockProxy = 'http://127.0.0.1:43210'
      const env = hubSpawnEnv({
        HTTPS_PROXY: mockProxy,
        https_proxy: mockProxy,
        HTTP_PROXY: 'http://caller-proxy.invalid:8080',
        NO_PROXY: '127.0.0.1,localhost,::1',
      })
      for (const name of proxyNames)
        expect(env[name], name).toBeUndefined()
      expect(env.HTTPS_PROXY).toBe(mockProxy)
      expect(env.https_proxy).toBe(mockProxy)
      expect(env.NO_PROXY).toBe('127.0.0.1,localhost,::1')
    }
    finally {
      for (const [index, name] of proxyNames.entries()) {
        if (before[index] === undefined)
          delete process.env[name]
        else
          process.env[name] = before[index]
      }
    }
  })

  it('clears an inherited development frontend URL', () => {
    const before = process.env.LEAPMUX_HUB_DEV_FRONTEND
    process.env.LEAPMUX_HUB_DEV_FRONTEND = 'http://localhost:5173'
    try {
      expect(hubSpawnEnv().LEAPMUX_HUB_DEV_FRONTEND).toBeUndefined()
    }
    finally {
      if (before === undefined)
        delete process.env.LEAPMUX_HUB_DEV_FRONTEND
      else
        process.env.LEAPMUX_HUB_DEV_FRONTEND = before
    }
  })

  it('keeps explicit worker settings', () => {
    expect(hubSpawnEnv({ LEAPMUX_WORKER_NAME: 'Local' }).LEAPMUX_WORKER_NAME).toBe('Local')
  })

  it('refuses a caller override of the development frontend URL', () => {
    const env = hubSpawnEnv({ LEAPMUX_HUB_DEV_FRONTEND: 'http://localhost:5173' } as Record<string, string>)
    expect(env.LEAPMUX_HUB_DEV_FRONTEND).toBeUndefined()
  })
})

/**
 * The subcommands that start a process which can spawn an agent.
 *
 * `worker` belongs here as much as `hub` does: a worker is what launches the
 * agent, so a worker started without `hubSpawnEnv` sends its agents to the
 * developer's real provider. That hole existed, and nothing caught it, because
 * a real model answers a test prompt correctly.
 */
const AGENT_HOST_SUBCOMMANDS = ['hub', 'solo', 'dev', 'worker']

// Parse calls so comments and strings cannot affect the guard.
function scanHubSpawns(text: string) {
  const source = ts.createSourceFile('fixture.ts', text, ts.ScriptTarget.Latest, true)
  let count = 0
  const violations: number[] = []
  function visit(node: ts.Node) {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)
      && ['spawn', 'spawnTestProcess'].includes(node.expression.text)) {
      const args = node.arguments[1]
      if (args && ts.isArrayLiteralExpression(args)
        && args.elements.some(arg => ts.isStringLiteral(arg) && AGENT_HOST_SUBCOMMANDS.includes(arg.text))) {
        count++
        const options = node.arguments[2]
        const env = options && ts.isObjectLiteralExpression(options)
          ? options.properties.find(property => ts.isPropertyAssignment(property) && property.name.getText(source) === 'env')
          : undefined
        const guarded = env && ts.isPropertyAssignment(env) && ts.isCallExpression(env.initializer)
          && ts.isIdentifier(env.initializer.expression) && env.initializer.expression.text === 'hubSpawnEnv'
        if (!guarded)
          violations.push(source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1)
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(source)
  return { count, violations }
}

describe('agent host launch environment', () => {
  it('guards every hub and worker launch, and finds at least one real call', () => {
    let count = 0
    const violations: string[] = []
    for (const file of collectE2EFiles()) {
      const result = scanHubSpawns(readFileSync(file, 'utf8'))
      count += result.count
      violations.push(...result.violations.map(line => `${posixRelative(frontendRoot, file)}:${line}`))
    }
    // Two faults, both silent. An inherited development URL makes a test pass
    // against another checkout's frontend, and an inherited provider credential
    // sends the agent to the live endpoint instead of the mock.
    expect(violations, 'Use hubSpawnEnv for every hub and worker process').toEqual([])
    expect(count, 'The guard must inspect actual launch calls').toBeGreaterThan(0)
  })

  it.each(['spawn', 'spawnTestProcess'])('detects an unsafe environment through %s', (method) => {
    expect(scanHubSpawns(`${method}(binary, ['hub'], { env: process.env })`))
      .toEqual({ count: 1, violations: [1] })
    expect(scanHubSpawns(`${method}(binary, ['hub'], { env: hubSpawnEnv() })`))
      .toEqual({ count: 1, violations: [] })
  })

  it.each(AGENT_HOST_SUBCOMMANDS)('guards the %s subcommand', (subcommand) => {
    expect(scanHubSpawns(`spawn(binary, ['${subcommand}'], { env: process.env })`))
      .toEqual({ count: 1, violations: [1] })
  })

  it('rejects a missing environment and ignores comments and quoted calls', () => {
    expect(scanHubSpawns('spawnTestProcess(binary, ["solo"])')).toEqual({ count: 1, violations: [1] })
    expect(scanHubSpawns('// spawn(binary, ["hub"], { env: process.env })')).toEqual({ count: 0, violations: [] })
    expect(scanHubSpawns('const text = "spawn(binary, [\'hub\'])"')).toEqual({ count: 0, violations: [] })
  })
})

describe('mockAgentEnv', () => {
  it('reports nothing before global setup wrote the state', () => {
    const before = process.env.E2E_STATE_PATH
    delete process.env.E2E_STATE_PATH
    try {
      expect(mockAgentEnv()).toEqual({})
      // `startSuiteServer` spawns the shared hub at this moment and passes the
      // same map explicitly, so the empty result is correct rather than a gap.
      expect(hubSpawnEnv({ LEAPMUX_WORKER_NAME: 'Local' }).LEAPMUX_WORKER_NAME).toBe('Local')
    }
    finally {
      if (before !== undefined)
        process.env.E2E_STATE_PATH = before
    }
  })
})

describe('server readiness deadline', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, 2_147_483_648])('refuses an invalid timeout before a request: %s', async (timeout) => {
    const request = vi.fn()
    vi.stubGlobal('fetch', request)
    await expect(waitForServer('http://server.test', timeout)).rejects.toThrow(RangeError)
    expect(request).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('accepts a successful response without leaving timers', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('ready')))
    await waitForServer('http://server.test', 100)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('waits past an unsuccessful HTTP response', async () => {
    const request = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response('', { status: 503 }))
      .mockResolvedValue(new Response('ready'))
    vi.stubGlobal('fetch', request)
    let ready = false
    const result = waitForServer('http://server.test', 100).then(() => {
      ready = true
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(ready).toBe(false)
    await vi.advanceTimersByTimeAsync(25)
    await result
    expect(request).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('ends an in-flight request when the deadline expires', async () => {
    const request = vi.fn<typeof fetch>(() => new Promise(() => {}))
    vi.stubGlobal('fetch', request)
    let outcome: unknown
    void waitForServer('http://server.test', 100).catch((error) => {
      outcome = error
    })
    await vi.advanceTimersByTimeAsync(100)
    expect(outcome).toBeInstanceOf(Error)
    expect(String(outcome)).toContain('did not start within 100ms')
    const init = request.mock.calls[0]?.[1]
    expect(init?.signal?.aborted).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('waitForHubStart', () => {
  let directory: string
  let statePath: string

  beforeEach(() => {
    const scratch = resolve(process.cwd(), '../.tmp')
    mkdirSync(scratch, { recursive: true })
    directory = mkdtempSync(join(scratch, 'hub-start-'))
    statePath = join(directory, 'state.json')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    rmSync(directory, { recursive: true, force: true })
  })

  it('reads the bound address from the state file and waits for the browser URL to answer', async () => {
    writeFileSync(statePath, JSON.stringify({ listen: ['unix:/run/hub.sock', '127.0.0.1:43210'] }))
    const request = vi.fn(async () => new Response('ready'))
    vi.stubGlobal('fetch', request)
    const { proc } = createProcessStub()
    await expect(waitForHubStart(statePath, proc)).resolves.toEqual({ hubUrl: 'http://localhost:43210', listen: '127.0.0.1:43210' })
    expect(request).toHaveBeenLastCalledWith('http://localhost:43210', expect.anything())
    await expect(waitForHubStart(statePath, proc, '127.0.0.1')).resolves.toEqual({ hubUrl: 'http://127.0.0.1:43210', listen: '127.0.0.1:43210' })
    expect(request).toHaveBeenLastCalledWith('http://127.0.0.1:43210', expect.anything())
    expect(proc.listenerCount('exit')).toBe(0)
    expect(proc.listenerCount('error')).toBe(0)
  })

  it('fails with the exit of a hub that wrote no state file, and sends no request', async () => {
    const request = vi.fn()
    vi.stubGlobal('fetch', request)
    await expect(waitForHubStart(statePath, createProcessStub({ exitCode: 1 }).proc)).rejects.toThrow('The hub exited before it wrote')
    expect(request).not.toHaveBeenCalled()
  })

  it('refuses a state file with no TCP address before a request', async () => {
    writeFileSync(statePath, JSON.stringify({ listen: ['unix:/run/hub.sock'] }))
    const request = vi.fn()
    vi.stubGlobal('fetch', request)
    await expect(waitForHubStart(statePath, createProcessStub().proc)).rejects.toThrow('expected one TCP address')
    expect(request).not.toHaveBeenCalled()
  })

  it('fails when the hub exits after it wrote the state file and before it answers', async () => {
    writeFileSync(statePath, JSON.stringify({ listen: ['127.0.0.1:43210'] }))
    const request = vi.fn<typeof fetch>(() => new Promise(() => {}))
    vi.stubGlobal('fetch', request)
    const stub = createProcessStub()
    const started = waitForHubStart(statePath, stub.proc)
    await vi.waitFor(() => expect(request).toHaveBeenCalled())
    stub.emitter.exitCode = 1
    stub.emitter.emit('exit', 1, null)
    await expect(started).rejects.toThrow('exited before startup completed')
    expect(request.mock.calls[0]?.[1]?.signal?.aborted).toBe(true)
  })
})
