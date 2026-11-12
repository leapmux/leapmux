import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { answerVerifyRequest } from './slhDsaVerifyWorker'

type VerifyModule = typeof import('./slhDsaVerify')

/** One signed transcript, made once: SLH-DSA-SHAKE-256f signs slowly. */
let keys: { publicKey: Uint8Array, secretKey: Uint8Array }
let message: Uint8Array
let signature: Uint8Array

beforeAll(() => {
  keys = slh_dsa_shake_256f.keygen()
  message = new TextEncoder().encode('handshake transcript')
  signature = slh_dsa_shake_256f.sign(message, keys.secretKey)
})

/** A tampered copy of the signature. */
function tampered(): Uint8Array {
  const copy = new Uint8Array(signature)
  copy[0] = (copy[0] ?? 0) ^ 0xFF
  return copy
}

/** A stand-in Worker that answers each request with `answer`, or crashes. */
function installWorker(behavior: { answer: (request: { id: number }) => boolean } | 'crash') {
  const spawned: unknown[] = []
  vi.stubGlobal('Worker', class {
    onmessage: ((event: { data: unknown }) => void) | null = null
    onerror: (() => void) | null = null
    terminate = vi.fn()
    constructor() {
      spawned.push(this)
    }

    postMessage(request: { id: number }) {
      queueMicrotask(() => {
        if (behavior === 'crash') {
          this.onerror?.()
        }
        else {
          this.onmessage?.({
            data: {
              id: request.id,
              valid: behavior.answer(request),
            },
          })
        }
      })
    }
  })
  return spawned
}

/** Import the client afresh, so that it spawns from this test's Worker. */
async function loadVerify(): Promise<VerifyModule> {
  vi.resetModules()
  return import('./slhDsaVerify')
}

describe('answerVerifyRequest', () => {
  it('answers a valid signature as valid, under the request id', () => {
    expect(answerVerifyRequest({
      id: 4,
      signature,
      message,
      publicKey: keys.publicKey,
    })).toEqual({ id: 4, valid: true })
  })

  it('answers a tampered signature as invalid', () => {
    expect(answerVerifyRequest({
      id: 5,
      signature: tampered(),
      message,
      publicKey: keys.publicKey,
    })).toEqual({ id: 5, valid: false })
  })

  it('answers a signature that the verifier cannot parse as invalid, rather than throwing', () => {
    expect(answerVerifyRequest({
      id: 6,
      signature: new Uint8Array(3),
      message,
      publicKey: new Uint8Array(1),
    })).toEqual({ id: 6, valid: false })
  })
})

describe('verifySlhDsaInWorker', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('verifies on this thread where no Worker exists', async () => {
    vi.stubGlobal('Worker', undefined)
    const { verifySlhDsaInWorker } = await loadVerify()
    await expect(verifySlhDsaInWorker(signature, message, keys.publicKey)).resolves.toBe(true)
    await expect(verifySlhDsaInWorker(tampered(), message, keys.publicKey)).resolves.toBe(false)
  })

  it('takes the verdict of the worker', async () => {
    // The worker's verdict is the answer, whatever this thread would compute.
    installWorker({
      answer: () => true,
    })
    const { verifySlhDsaInWorker } = await loadVerify()
    await expect(verifySlhDsaInWorker(tampered(), message, keys.publicKey)).resolves.toBe(true)
  })

  it('verifies on this thread when the worker crashes', async () => {
    installWorker('crash')
    const { verifySlhDsaInWorker } = await loadVerify()
    await expect(verifySlhDsaInWorker(signature, message, keys.publicKey)).resolves.toBe(true)
    await expect(verifySlhDsaInWorker(tampered(), message, keys.publicKey)).resolves.toBe(false)
  })

  it('spawns the worker once, ahead of the first request, when prepared', async () => {
    const spawned = installWorker({
      answer: () => true,
    })
    const { prepareSlhDsaVerifyWorker, verifySlhDsaInWorker } = await loadVerify()

    prepareSlhDsaVerifyWorker()
    expect(spawned).toHaveLength(1)
    prepareSlhDsaVerifyWorker()
    await verifySlhDsaInWorker(signature, message, keys.publicKey)

    expect(spawned).toHaveLength(1)
  })

  it('prepares nothing where no Worker exists', async () => {
    vi.stubGlobal('Worker', undefined)
    const { prepareSlhDsaVerifyWorker } = await loadVerify()
    expect(() => prepareSlhDsaVerifyWorker()).not.toThrow()
  })
})
