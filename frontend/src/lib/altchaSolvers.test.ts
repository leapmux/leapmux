/// <reference types="vitest/globals" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ensureAltchaSolver } from './altchaSolvers'

// The worker chunks are ?url assets; mock the modules so no real asset is
// emitted or fetched in the unit environment. vi.mock is hoisted above the
// import either way.
vi.mock('altcha/workers/scrypt?url', () => ({ default: 'scrypt-worker-url' }))
vi.mock('altcha/workers/argon2id?url', () => ({ default: 'argon2id-worker-url' }))

class FakeWorker {
  constructor(
    public url: string,
    public options?: WorkerOptions,
  ) {}
}

describe('ensureAltchaSolver', () => {
  let algorithms: Map<string, () => Worker | Promise<Worker>>

  beforeEach(() => {
    algorithms = new Map()
    vi.stubGlobal('$altcha', { algorithms })
    vi.stubGlobal('Worker', FakeWorker)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('dynamically registers the SCRYPT worker factory', async () => {
    await ensureAltchaSolver('SCRYPT')
    expect(algorithms.has('SCRYPT')).toBe(true)

    const worker = await algorithms.get('SCRYPT')!() as unknown as FakeWorker
    expect(worker.url).toBe('scrypt-worker-url')
    expect(worker.options?.name).toBe('altcha-scrypt')
  })

  it('dynamically registers the ARGON2ID worker factory', async () => {
    await ensureAltchaSolver('ARGON2ID')
    const worker = await algorithms.get('ARGON2ID')!() as unknown as FakeWorker
    expect(worker.url).toBe('argon2id-worker-url')
    expect(worker.options?.name).toBe('altcha-argon2id')
  })

  it('leaves the pre-registered families and unknown names alone', async () => {
    await ensureAltchaSolver('PBKDF2/SHA-256')
    await ensureAltchaSolver('SHA-256')
    await ensureAltchaSolver('NOT-AN-ALGORITHM')
    await ensureAltchaSolver(undefined)
    await ensureAltchaSolver('')
    expect(algorithms.size).toBe(0)
  })

  it('never replaces an already-registered solver', async () => {
    const sentinel = () => ({}) as unknown as Worker
    algorithms.set('SCRYPT', sentinel)
    await ensureAltchaSolver('SCRYPT')
    expect(algorithms.get('SCRYPT')).toBe(sentinel)
  })

  it('is a no-op without the widget global (widget not loaded yet)', async () => {
    vi.stubGlobal('$altcha', undefined)
    await expect(ensureAltchaSolver('SCRYPT')).resolves.toBeUndefined()
  })
})
