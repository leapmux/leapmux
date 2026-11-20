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
import type { KeyPinStateChange, WorkerKeyPinRecord } from './browserStorage'
import type { WorkerKeyBundle } from './workerKeyBundle'
import { bytesToHex } from '@noble/hashes/utils.js'
import {
  onKeyPinsInvalidated,
  onStorageAccountChange,
  readKeyPinState,
  updateKeyPinState,
} from './browserStorage'
import { concatBytes } from './bytes'

export type KeyPinDecision = 'accept' | 'reject'

/** Prompt invoked when a previously-pinned worker public key changes. */
export type KeyPinConfirmFn = (
  workerId: string,
  expectedFingerprint: string,
  actualFingerprint: string,
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
export async function readWorkerKeyPin(workerId: string): Promise<WorkerKeyPinSnapshot> {
  const { pins, removalRevision } = await readKeyPinState()
  return { pin: pins[workerId] ?? null, removalRevision }
}

/** List every pinned worker key, oldest first. Rejects on a failed read. */
export async function listKeyPins(): Promise<KeyPinEntry[]> {
  const { pins } = await readKeyPinState()
  return Object.entries(pins)
    .map(([workerId, pin]) => ({ workerId, publicKeyHex: pin.publicKeyHex, firstSeen: pin.firstSeen }))
    .sort((a, b) => a.firstSeen - b.firstSeen)
}

/**
 * Remove a pinned key for a worker. Idempotent: removing an absent pin
 * succeeds and changes nothing. Rejects on a failed write.
 */
export async function clearKeyPin(workerId: string): Promise<void> {
  await updateKeyPinState({ kind: 'remove', workerId })
}

/** Remove all pinned keys. Idempotent. Rejects on a failed write. */
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

/**
 * Session-scoped TOFU pin store. Persistence is shared across tabs via the
 * `key-pins` row, which `browserStorage` scopes to the signed-in account, so
 * two accounts on one browser pin independently. The rejected-worker set is
 * per-instance.
 */
export class KeyPinStore {
  private confirmKeyPin: KeyPinConfirmFn
  private rejectedWorkers = new Set<string>()
  /** Serializes mismatch prompts so concurrent opens cannot overwrite the dialog. */
  private confirmChain: Promise<void> = Promise.resolve()
  /**
   * Bumped when the mismatch prompt is replaced. A reject that started under an
   * older prompt (fail-closed stub) must not poison rejectedWorkers after the
   * real UI registers — clearing the set alone loses the race with an in-flight
   * await that still resolves to 'reject'.
   */
  private confirmEpoch = 0
  /** The fail-closed prompt this store was built with, restored on unregister. */
  private readonly defaultConfirmKeyPin: KeyPinConfirmFn

  constructor(opts: KeyPinStoreOpts) {
    this.confirmKeyPin = opts.confirmKeyPin
    this.defaultConfirmKeyPin = opts.confirmKeyPin
  }

  /**
   * Replace the mismatch prompt. Used by AppShell, which registers the UI
   * dialog after the singleton ChannelManager (and this store) is constructed.
   * Clears the session reject set: rejects that happened under the fail-closed
   * default stub must not poison the real UI for the rest of the tab.
   *
   * Returns a disposer restoring the fail-closed default, and the caller MUST
   * run it with its owner. This store is a module-level singleton; the UI mount
   * is not, and AppShell genuinely remounts inside one page lifetime (logout ->
   * /login -> login -> /). A prompt closed over an UNMOUNTED dialog never
   * settles -- nothing renders it, so its `resolve` is never called -- and
   * `resolve` awaits it with no timeout. Worse, `enqueueConfirm` chains every
   * prompt on `confirmChain`, which only advances when the previous one
   * settles: one mismatch in that window would deadlock TOFU confirmation for
   * the life of the page, including for the mount that came after. Restoring
   * the default makes that window fail closed instead of hang.
   */
  setConfirmKeyPin(fn: KeyPinConfirmFn): () => void {
    this.confirmKeyPin = fn
    this.rejectedWorkers.clear()
    this.confirmEpoch++
    return () => {
      // A newer registration already won: restoring now would clobber the LIVE
      // mount's prompt with the fail-closed stub.
      if (this.confirmKeyPin !== fn)
        return
      this.confirmKeyPin = this.defaultConfirmKeyPin
      this.confirmEpoch++
    }
  }

  /**
   * Resolve the TOFU key pin for a worker, prompting the user on a mismatch.
   *
   * Returns the `commit` the caller awaits once the channel is proven, which
   * records this worker's key. Splitting the decision from the write is what
   * keeps the write correct: the open awaits the prompt, the handshake, and
   * the WebSocket between the two. The commit therefore revalidates, inside
   * one storage transaction, both the removal revision and the pin this
   * decision observed -- so a removal or a replacement that landed meanwhile
   * (in this tab or another) refuses the commit instead of being overwritten
   * by it. A refused commit writes nothing and leaves the stored trust as it
   * is; the open that carried it fails and rolls back.
   *
   * Concurrent first contacts for the SAME key converge on one record: the
   * later commit observes the identical pin and keeps the first `firstSeen`.
   * A first contact that finds a DIFFERENT key installed fails the commit.
   *
   * A matching decision gets a commit too -- one that validates and writes
   * nothing -- so even "the key we already trust" fails to publish over a pin
   * that changed since the decision was taken.
   *
   * Throws KeyPinRejectedError when the user rejects the new key.
   */
  async resolve(workerId: string, keyBundle: KeyPinKeyBundle): Promise<() => Promise<void>> {
    const compositeKeyBytes = concatBytes(keyBundle.x25519PublicKey, keyBundle.mlkemPublicKey, keyBundle.slhdsaPublicKey)
    const publicKeyHex = bytesToHex(compositeKeyBytes)
    const { pin: pinned, removalRevision } = await readWorkerKeyPin(workerId)

    const commit = async () => {
      await updateKeyPinState({
        kind: 'record',
        workerId,
        pin: { publicKeyHex, firstSeen: Date.now() },
        expectRevision: removalRevision,
        expectPin: pinned,
      } satisfies KeyPinStateChange)
    }

    if (!pinned) {
      // First use: trust and record it.
      return commit
    }

    if (pinned.publicKeyHex === publicKeyHex) {
      // The key we already trust. Nothing to write -- but the commit still
      // validates, so a pin removed or replaced since this decision cannot be
      // published over by a channel that merely proved itself.
      return async () => {
        await updateKeyPinState({
          kind: 'revalidate',
          workerId,
          expectRevision: removalRevision,
          expectPin: { publicKeyHex: pinned.publicKeyHex, firstSeen: pinned.firstSeen },
        } satisfies KeyPinStateChange)
      }
    }

    // Auto-reject if the user already rejected this worker in this session.
    if (this.rejectedWorkers.has(workerId)) {
      throw new KeyPinRejectedError()
    }

    // Key mismatch — ask user. Serialize prompts so concurrent mismatches
    // cannot overwrite each other's dialog Promise (FOOTGUNS-5).
    const { keyFingerprintHex } = await import('./fingerprint')
    const epoch = this.confirmEpoch
    const decision = await this.enqueueConfirm(() => this.confirmKeyPin(
      workerId,
      keyFingerprintHex(pinned.publicKeyHex),
      keyFingerprintHex(publicKeyHex),
    ))
    if (decision === 'reject') {
      // Only remember rejects that still belong to the current prompt wiring.
      if (epoch === this.confirmEpoch)
        this.rejectedWorkers.add(workerId)
      throw new KeyPinRejectedError()
    }
    // User accepted the new key.
    return commit
  }

  private enqueueConfirm(fn: () => Promise<KeyPinDecision>): Promise<KeyPinDecision> {
    const run = this.confirmChain.then(fn, fn)
    this.confirmChain = run.then(() => undefined, () => undefined)
    return run
  }
}
