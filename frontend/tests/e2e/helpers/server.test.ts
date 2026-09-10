import { readFileSync } from 'node:fs'
import process from 'node:process'
import ts from 'typescript'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { hubSpawnEnv, waitForServer } from './server'

describe('hubSpawnEnv', () => {
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
        && args.elements.some(arg => ts.isStringLiteral(arg) && ['hub', 'solo', 'dev'].includes(arg.text))) {
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

describe('hub launch environment', () => {
  it('guards every hub launch and finds at least one real call', () => {
    let count = 0
    const violations: string[] = []
    for (const file of collectE2EFiles()) {
      const result = scanHubSpawns(readFileSync(file, 'utf8'))
      count += result.count
      violations.push(...result.violations.map(line => `${posixRelative(frontendRoot, file)}:${line}`))
    }
    // An inherited development URL can make a test pass against another checkout's frontend.
    expect(violations, 'Use hubSpawnEnv for every hub process').toEqual([])
    expect(count, 'The guard must inspect actual launch calls').toBeGreaterThan(0)
  })

  it.each(['spawn', 'spawnTestProcess'])('detects an unsafe environment through %s', (method) => {
    expect(scanHubSpawns(`${method}(binary, ['hub'], { env: process.env })`))
      .toEqual({ count: 1, violations: [1] })
    expect(scanHubSpawns(`${method}(binary, ['hub'], { env: hubSpawnEnv() })`))
      .toEqual({ count: 1, violations: [] })
  })

  it('rejects a missing environment and ignores comments and quoted calls', () => {
    expect(scanHubSpawns('spawnTestProcess(binary, ["solo"])')).toEqual({ count: 1, violations: [1] })
    expect(scanHubSpawns('// spawn(binary, ["hub"], { env: process.env })')).toEqual({ count: 0, violations: [] })
    expect(scanHubSpawns('const text = "spawn(binary, [\'hub\'])"')).toEqual({ count: 0, violations: [] })
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
    expect(request.mock.calls[0][1]?.signal?.aborted).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })
})
