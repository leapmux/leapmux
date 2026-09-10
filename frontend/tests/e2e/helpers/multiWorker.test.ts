import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { startMultiWorkerHarness } from './multiWorker'
import { spawnTestProcess } from './processRegistry'
import { waitForServer } from './server'

let root: string

vi.mock('./processRegistry', () => ({ spawnTestProcess: vi.fn() }))
vi.mock('./e2e-channel', () => ({ createTestChannelManager: vi.fn() }))
vi.mock('./server', () => ({
  getGlobalState: () => ({ binaryPath: 'leapmux', tmpDir: root }),
  findFreePort: async () => 12345,
  hubSpawnEnv: () => ({}),
  waitForServer: vi.fn(),
}))

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
  vi.mocked(waitForServer).mockReset().mockResolvedValue(undefined)
  vi.mocked(spawnTestProcess).mockImplementation((_command, args) => {
    const stub = createProcessStub({ pid: 100 + children.length })
    children.push(stub)
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

describe('multi-worker lifetime', () => {
  it.each(['readiness', 'SignUp', 'GetCurrentUser', 'CreateRegistrationKey', 'registration'])('closes partial startup after %s fails', async (stage) => {
    failure = stage
    if (stage === 'readiness')
      vi.mocked(waitForServer).mockRejectedValueOnce(new Error('readiness failed'))
    await expect(startMultiWorkerHarness(1)).rejects.toThrow(`${stage} failed`)
    expect(children.length).toBeGreaterThan(0)
    for (const child of children)
      expect(child.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(readdirSync(root)).toEqual([])
  })

  it('makes concurrent stop callers wait for the same cleanup', async () => {
    const harness = await startMultiWorkerHarness(0)
    children[0].emitter.kill.mockImplementation(() => true)
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
      children[0].emitter.exitCode = 0
      children[0].emitter.emit('exit', 0, null)
      await Promise.all([first, second])
    }
    expect(children[0].emitter.kill).toHaveBeenCalledTimes(1)
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
      expect(children[0].emitter.kill).not.toHaveBeenCalled()
      expect(children[1].emitter.kill).not.toHaveBeenCalled()
      expect(children[2].emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
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
