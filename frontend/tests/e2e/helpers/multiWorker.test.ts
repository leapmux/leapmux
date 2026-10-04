import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { startMultiWorkerHarness } from './multiWorker'
import { spawnTestProcess } from './processRegistry'
import { findFreePort, waitForHubReady, waitForHubStateFile, waitForServer } from './server'

let root: string

vi.mock('./processRegistry', () => ({ spawnTestProcess: vi.fn() }))
vi.mock('./e2e-channel', () => ({ createTestChannelManager: vi.fn() }))
vi.mock('./server', async original => ({
  ...await original<typeof import('./server')>(),
  getGlobalState: () => ({ binaryPath: 'leapmux', tmpDir: root }),
  findFreePort: vi.fn(async () => 12345),
  hubSpawnEnv: () => ({}),
  waitForServer: vi.fn(),
  waitForHubStateFile: vi.fn(),
  waitForHubReady: vi.fn(),
}))

let requireBoundPort = false
let hubCount = 0
const states = new Map<number, string>()
const nativeHubURLs = new Set<string>()

let failure = ''
let workerIDs: string[]
let children: ReturnType<typeof createProcessStub>[]

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  root = mkdtempSync(join(scratch, 'multi-worker-test-'))
  children = []
  workerIDs = []
  failure = ''
  requireBoundPort = false
  hubCount = 0
  states.clear()
  nativeHubURLs.clear()
  vi.mocked(findFreePort).mockReset().mockResolvedValue(12345)
  vi.mocked(waitForHubReady).mockReset().mockResolvedValue(undefined)
  vi.mocked(waitForHubStateFile).mockReset().mockImplementation(async (_path, proc) => {
    const state = states.get(proc.pid ?? -1)
    if (!state)
      throw new Error('The Hub has no bound port or state file.')
    return state
  })
  vi.mocked(waitForServer).mockReset().mockResolvedValue(undefined)
  vi.mocked(spawnTestProcess).mockReset().mockImplementation((_command, args) => {
    const stub = createProcessStub({ pid: 100 + children.length })
    children.push(stub)
    if (args[0] === 'hub' && args.includes('127.0.0.1:0')) {
      const port = 24680 + hubCount++
      nativeHubURLs.add(`http://localhost:${port}`)
      states.set(stub.emitter.pid ?? -1, JSON.stringify({ listen: [`127.0.0.1:${port}`] }))
    }
    if (args[0] === 'worker')
      workerIDs.push(`worker-${stub.emitter.pid}`)
    stub.emitter.kill.mockImplementation(() => {
      workerIDs = workerIDs.filter(id => id !== `worker-${stub.emitter.pid}`)
      stub.emitter.exitCode = 0
      stub.emitter.emit('exit', 0, null)
      return true
    })
    return stub.proc
  })
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    if (requireBoundPort && !nativeHubURLs.has(new URL(String(input)).origin))
      throw new Error('The provisional port belongs to another shard.')
    const method = String(input).split('/').at(-1)
    if (failure === method || (failure === 'registration' && method === 'ListWorkers' && workerIDs.length > 0))
      throw new Error(`${failure} failed`)
    if (failure === 'new registration' && method === 'ListWorkers' && workerIDs.length > 1)
      throw new Error('new registration failed')
    switch (method) {
      case 'GetAltchaChallenge': return Response.json({})
      case 'SignUp': return new Response('{}', { headers: { 'Set-Cookie': 'leapmux-session=session; HttpOnly' } })
      case 'GetCurrentUser': return Response.json({ user: { id: 'user' } })
      case 'CreateRegistrationKey': return Response.json({ registrationKey: 'registration' })
      case 'ListWorkers': return Response.json({ workers: workerIDs.map(id => ({ id, online: true })) })
      default: throw new Error(`Unexpected request: ${method}`)
    }
  }))
})

afterEach(() => {
  for (const child of children)
    child.emitter.emit('exit', 0, null)
  vi.unstubAllGlobals()
  rmSync(root, { recursive: true, force: true })
})

/** Read the child at index `i` after the harness starts it. */
function childAt(i: number) {
  const child = children[i]
  if (!child)
    throw new Error(`expected a spawned child process at index ${i}`)
  return child
}

describe('multi-worker lifetime', () => {
  it.each(['readiness', 'SignUp', 'GetCurrentUser', 'CreateRegistrationKey', 'registration'])('closes partial startup after %s fails', async (stage) => {
    failure = stage
    if (stage === 'readiness') {
      vi.mocked(waitForServer).mockRejectedValueOnce(new Error('readiness failed'))
      vi.mocked(waitForHubReady).mockRejectedValueOnce(new Error('readiness failed'))
    }
    await expect(startMultiWorkerHarness(1)).rejects.toThrow(`${stage} failed`)
    expect(children.length).toBeGreaterThan(0)
    for (const child of children)
      expect(child.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(readdirSync(root)).toEqual([])
  })

  it('makes concurrent stop callers wait for the same cleanup', async () => {
    const harness = await startMultiWorkerHarness(0)
    childAt(0).emitter.kill.mockImplementation(() => true)
    const first = harness.stop()
    let secondFinished = false
    const second = harness.stop().then(() => {
      secondFinished = true
    })
    try {
      await Promise.resolve()
      await Promise.resolve()
      expect(secondFinished).toBe(false)
    }
    finally {
      childAt(0).emitter.exitCode = 0
      childAt(0).emitter.emit('exit', 0, null)
      await Promise.all([first, second])
    }
    expect(childAt(0).emitter.kill).toHaveBeenCalledTimes(1)
  })

  it('assigns distinct worker identities to concurrent additions', async () => {
    const harness = await startMultiWorkerHarness(0)
    try {
      const added = await Promise.all([harness.addWorker('first'), harness.addWorker('second')])
      expect(added.map(worker => worker.id)).toEqual(['worker-101', 'worker-102'])
      expect(harness.workers).toHaveLength(2)
    }
    finally {
      await harness.stop()
    }
  })

  it('refuses additions after shutdown without allocating another directory', async () => {
    const harness = await startMultiWorkerHarness(0)
    await harness.stop()
    await expect(harness.addWorker('late')).rejects.toThrow('stopped')
    expect(children).toHaveLength(1)
    expect(readdirSync(root)).toEqual([])
  })

  it('cleans a failed later addition and still accepts another worker', async () => {
    const harness = await startMultiWorkerHarness(1)
    try {
      failure = 'new registration'
      await expect(harness.addWorker('failed')).rejects.toThrow('registration failed')
      expect(harness.workers).toHaveLength(1)
      expect(childAt(0).emitter.kill).not.toHaveBeenCalled()
      expect(childAt(1).emitter.kill).not.toHaveBeenCalled()
      expect(childAt(2).emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
      expect(readdirSync(root)).toHaveLength(2)
      failure = ''
      const next = await harness.addWorker('next')
      expect(next.id).toBe('worker-103')
      expect(harness.workers.map(worker => worker.id)).toEqual(['worker-101', 'worker-103'])
    }
    finally {
      await harness.stop()
    }
    expect(readdirSync(root)).toEqual([])
  })

  it('finishes accepted additions before shutdown closes their children', async () => {
    const harness = await startMultiWorkerHarness(0)
    const added = harness.addWorker('accepted')
    const stopped = harness.stop()
    await expect(harness.addWorker('rejected')).rejects.toThrow('stopped')
    const worker = await added
    await stopped
    expect(worker.id).toBe('worker-101')
    expect(children).toHaveLength(2)
    for (const child of children)
      expect(child.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(readdirSync(root)).toEqual([])
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('refuses an invalid worker count: %s', async (count) => {
    await expect(startMultiWorkerHarness(count)).rejects.toThrow(RangeError)
    expect(children).toEqual([])
    expect(readdirSync(root)).toEqual([])
  })
})

describe('multi-worker port ownership', () => {
  it('uses the bound Hub port while the old provisional port belongs to another shard', async () => {
    requireBoundPort = true
    const harness = await startMultiWorkerHarness(1)
    try {
      expect(harness.hubUrl).toBe('http://localhost:24680')
      expect(findFreePort).not.toHaveBeenCalled()
      expect(spawnTestProcess).toHaveBeenNthCalledWith(1, 'leapmux', [
        'hub',
        '-listen',
        '127.0.0.1:0',
        '-data-dir',
        harness.hubDataDir,
      ], expect.any(Object))
      expect(waitForHubStateFile).toHaveBeenCalledWith(join(harness.hubDataDir, 'state.json'), harness.hubProc)
      expect(waitForHubReady).toHaveBeenCalledWith(harness.hubUrl, harness.hubProc)
      expect(spawnTestProcess).toHaveBeenNthCalledWith(2, 'leapmux', expect.arrayContaining(['--hub', harness.hubUrl]), expect.any(Object))
    }
    finally {
      await harness.stop()
    }
    expect(readdirSync(root)).toEqual([])
  })

  it('assigns distinct bound ports to concurrent harnesses', async () => {
    requireBoundPort = true
    const started = await Promise.allSettled([startMultiWorkerHarness(0), startMultiWorkerHarness(0)])
    const harnesses = started.flatMap(result => result.status === 'fulfilled' ? [result.value] : [])
    try {
      expect(started.every(result => result.status === 'fulfilled')).toBe(true)
      expect(harnesses.map(harness => harness.hubUrl).sort()).toEqual(['http://localhost:24680', 'http://localhost:24681'])
      expect(harnesses[0]?.hubDataDir).not.toBe(harnesses[1]?.hubDataDir)
    }
    finally {
      await Promise.all(harnesses.map(harness => harness.stop()))
    }
    expect(readdirSync(root)).toEqual([])
  })

  it('stops the spawned Hub and removes its directory when the state file fails', async () => {
    const failed = new Error('The controlled Hub state file failed.')
    vi.mocked(waitForHubStateFile).mockRejectedValueOnce(failed)
    await expect(startMultiWorkerHarness(0)).rejects.toBe(failed)
    expect(children).toHaveLength(1)
    expect(childAt(0).emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(readdirSync(root)).toEqual([])
  })
})
