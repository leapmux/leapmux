/**
 * TOFU (trust-on-first-use) key-pinning policy for worker public keys.
 *
 * Mirrors the Go client's KeyPinStore seam (backend/tunnel/channel.go) /
 * tofupins.Store, plus the browser's interactive mismatch prompt and the
 * deferred-commit contract: never pin an unproven key (the caller runs
 * `commit` only after the open-time Ping verifies the session).
 *
 * The persistence is STRICT and sits behind the storage gateway
 * (`readKeyPinState` / `updateKeyPinState` in `~/lib/browserStorage`): a read
 * that fails is a failure and never an empty pin map, and every commit runs in
 * one transaction that revalidates the decision before it writes. That
 * compare-and-swap is what keeps a decision taken against one state from
 * committing over a removal or a replacement that landed since -- in this tab
 * or another. The removal revision (`KEY_KEY_PINS_REMOVAL_REVISION`) travels
 * with every decision for exactly that check.
 *
 * Extracted from ChannelManager so the policy is unit-testable without a
 * WebSocket / Noise harness. See https://github.com/leapmux/leapmux/issues/283.
 */
import type { KeyPinStateChange, KeyPinStorageScope, WorkerKeyPinRecord } from './browserStorage'
import type { WorkerKeyBundle } from './workerKeyBundle'
import { bytesToHex } from '@noble/hashes/utils.js'
import {
  onKeyPinsInvalidated,
  onStorageAccountChange,
  readKeyPinState,
  storageAccountId,
  updateKeyPinState,
} from './browserStorage'
import { concatBytes } from './bytes'

export type KeyPinDecision = 'accept' | 'reject'

/** Prompt invoked when a previously-pinned worker public key changes. */
export type KeyPinConfirmFn = (
  workerId: string,
  expectedFingerprint: string,
  actualFingerprint: string,
  signal: AbortSignal,
) => Promise<KeyPinDecision>

/** The public-key material resolve compares against the stored pin. */
export type KeyPinKeyBundle = WorkerKeyBundle

/** One pinned worker key, as the settings surface reads it. */
export interface KeyPinEntry {
  workerId: string
  publicKeyHex: string
  firstSeen: number
}

/**
 * The durable pin state observed for one worker, and the removal revision it
 * was read under. Both come from ONE transaction, so a commit built from this
 * snapshot has a consistent pair to validate against.
 */
export interface WorkerKeyPinSnapshot {
  /** The stored pin, or null for a confirmed unpinned worker. */
  pin: WorkerKeyPinRecord | null
  /** The removal revision observed with the pin. */
  removalRevision: number
}

/** Thrown when the user rejects a key change (or auto-rejects in-session). */
export class KeyPinRejectedError extends Error {
  constructor(message = 'Worker public key rejected by user') {
    super(message)
    this.name = 'KeyPinRejectedError'
  }
}

/**
 * Read the current durable pin for one worker.
 *
 * Resolves `pin: null` for a confirmed missing record, which is first-contact
 * state and not a failure. A failed read REJECTS -- "the worker is unpinned"
 * and "storage could not answer" are different trust decisions, and collapsing
 * them is how a failed read becomes a silent first contact.
 */
export async function readWorkerKeyPin(workerId: string, scope?: KeyPinStorageScope): Promise<WorkerKeyPinSnapshot> {
  const { pins, removalRevision } = await readKeyPinState(scope)
  return {
    pin: Object.hasOwn(pins, workerId)
      ? (pins[workerId] ?? null)
      : null,
    removalRevision,
  }
}

/** List every pinned worker key, oldest first. Rejects on a failed read. */
export async function listKeyPins(): Promise<KeyPinEntry[]> {
  const { pins } = await readKeyPinState()
  return Object.entries(pins)
    .map(([workerId, pin]) => ({ workerId, publicKeyHex: pin.publicKeyHex, firstSeen: pin.firstSeen }))
    .sort((a, b) => a.firstSeen - b.firstSeen)
}

/**
 * Remove a pinned key for a worker. A success invalidates pending decisions,
 * even when the worker has no pin. Rejects on a failed write.
 */
export async function clearKeyPin(workerId: string): Promise<void> {
  await updateKeyPinState({ kind: 'remove', workerId })
}

/** Remove all pins and invalidate pending decisions. Rejects on a failed write. */
export async function clearAllKeyPins(): Promise<void> {
  await updateKeyPinState({ kind: 'removeAll' })
}

/**
 * Run `listener` whenever the durable pin state may have changed: a committed
 * update or removal (this tab or another), a noticed expiry, or a move of the
 * account namespace. Returns the unsubscribe.
 *
 * For a settings surface that shows durable pins: re-read on this, and
 * disregard any read that a concurrent account change overtook.
 */
export function onKeyPinsChanged(listener: () => void): () => void {
  const offInvalidation = onKeyPinsInvalidated(listener)
  const offAccount = onStorageAccountChange(listener)
  return () => {
    offInvalidation()
    offAccount()
  }
}

export interface KeyPinStoreOpts {
  confirmKeyPin: KeyPinConfirmFn
}

interface KeyPinResolveOpts extends KeyPinStorageScope {
  identitySignal?: AbortSignal
  review?: boolean
  assertCurrent?: () => void
}

export interface ResolvedKeyPin {
  publicKeyHex: string
  commit: () => Promise<void>
}

function createConfirmation(confirm: KeyPinConfirmFn) {
  return {
    confirm,
    lifetime: new AbortController(),
    rejectedWorkers: new WeakMap<AbortSignal, Set<string>>(),
    rejectedWorkersByAccount: new Map<string | null, Set<string>>(),
  }
}

/** Observe cancellation even when a callback holds a promise indefinitely. */
function waitForConfirmation<T>(pending: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const abort = () => reject(signal.reason)
    signal.addEventListener('abort', abort, { once: true })
    void pending.then(
      (value) => {
        signal.removeEventListener('abort', abort)
        resolve(value)
      },
      (err: unknown) => {
        signal.removeEventListener('abort', abort)
        reject(err)
      },
    )
    if (signal.aborted) {
      signal.removeEventListener('abort', abort)
      abort()
    }
  })
}

/** The browser's durable TOFU policy and its in-memory mismatch prompt queue. */
export class KeyPinStore {
  private confirmation: ReturnType<typeof createConfirmation>
  private confirmChain: Promise<void> = Promise.resolve()
  private readonly defaultConfirmKeyPin: KeyPinConfirmFn

  constructor(opts: KeyPinStoreOpts) {
    this.defaultConfirmKeyPin = opts.confirmKeyPin
    this.confirmation = createConfirmation(opts.confirmKeyPin)
  }

  /**
   * Replace the prompt and retire the previous registration. Disposing a UI
   * owner retires its displayed and queued prompts and restores the default.
   */
  setConfirmKeyPin(fn: KeyPinConfirmFn): () => void {
    const registration = createConfirmation(fn)
    const previous = this.confirmation
    this.confirmation = registration
    previous.lifetime.abort()
    return () => {
      if (this.confirmation !== registration)
        return
      this.confirmation = createConfirmation(this.defaultConfirmKeyPin)
      registration.lifetime.abort()
    }
  }

  /**
   * Resolve a candidate key against durable trust. The caller commits only
   * after proving the session. The commit revalidates the observed pin and
   * removal revision in one transaction, including when the bytes match.
   */
  async resolve(
    workerId: string,
    keyBundle: KeyPinKeyBundle,
    { accountId = storageAccountId(), signal, identitySignal, review, assertCurrent }: KeyPinResolveOpts = {},
  ): Promise<ResolvedKeyPin> {
    const registration = this.confirmation
    const storageScope = signal === undefined
      ? { accountId }
      : { accountId, signal }
    assertCurrent?.()
    const compositeKeyBytes = concatBytes(keyBundle.x25519PublicKey, keyBundle.mlkemPublicKey, keyBundle.slhdsaPublicKey)
    const publicKeyHex = bytesToHex(compositeKeyBytes)
    const { pin, removalRevision } = await readWorkerKeyPin(workerId, storageScope)
    assertCurrent?.()

    const result = {
      publicKeyHex,
      commit: async () => {
        assertCurrent?.()
        const change: KeyPinStateChange = pin?.publicKeyHex === publicKeyHex
          ? {
              kind: 'revalidate',
              workerId,
              expectRevision: removalRevision,
              expectPin: pin,
            }
          : {
              kind: 'record',
              workerId,
              pin: {
                publicKeyHex,
                firstSeen: Date.now(),
              },
              expectRevision: removalRevision,
              expectPin: pin,
            }
        await updateKeyPinState(change, storageScope)
      },
    }

    if (pin === null) {
      if (review)
        throw new Error('The Worker has no stored key to review.')
      return result
    }

    if (pin.publicKeyHex === publicKeyHex)
      return result

    const promptSignal = signal === undefined
      ? registration.lifetime.signal
      : AbortSignal.any([signal, registration.lifetime.signal])
    promptSignal.throwIfAborted()
    let refused = identitySignal === undefined
      ? registration.rejectedWorkersByAccount.get(accountId)
      : registration.rejectedWorkers.get(identitySignal)
    if (refused === undefined) {
      refused = new Set<string>()
      if (identitySignal === undefined)
        registration.rejectedWorkersByAccount.set(accountId, refused)
      else
        registration.rejectedWorkers.set(identitySignal, refused)
    }
    const rejectedWorkers = refused
    if (!review && rejectedWorkers.has(workerId))
      throw new KeyPinRejectedError()

    const { keyFingerprintHex } = await waitForConfirmation(import('./fingerprint'), promptSignal)
    await this.enqueueConfirm(promptSignal, async () => {
      assertCurrent?.()
      if (!review && rejectedWorkers.has(workerId))
        throw new KeyPinRejectedError()
      const decision = await waitForConfirmation(
        registration.confirm(workerId, keyFingerprintHex(pin.publicKeyHex), keyFingerprintHex(publicKeyHex), promptSignal),
        promptSignal,
      )
      promptSignal.throwIfAborted()
      assertCurrent?.()
      if (decision === 'reject') {
        rejectedWorkers.add(workerId)
        throw new KeyPinRejectedError()
      }
    })
    promptSignal.throwIfAborted()
    assertCurrent?.()
    return result
  }

  private enqueueConfirm(signal: AbortSignal, confirm: () => Promise<void>): Promise<void> {
    const run = this.confirmChain.then(() => {
      signal.throwIfAborted()
      return confirm()
    })
    this.confirmChain = run.then(() => undefined, () => undefined)
    // Settle this caller even while an earlier prompt still owns the queue.
    return waitForConfirmation(run, signal)
  }
}
