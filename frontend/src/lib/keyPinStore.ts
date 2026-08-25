/**
 * TOFU (trust-on-first-use) key-pinning policy for worker public keys.
 *
 * Mirrors the Go client's KeyPinStore seam (backend/tunnel/channel.go) /
 * tofupins.Store, plus the browser's interactive mismatch prompt and the
 * deferred-commit contract: never pin an unproven key (the caller runs
 * `commit` only after the open-time Ping verifies the session).
 *
 * Extracted from ChannelManager so the policy is unit-testable without a
 * WebSocket / Noise harness. See https://github.com/leapmux/leapmux/issues/283.
 */
import type { WorkerKeyBundle } from './workerKeyBundle'
import { bytesToHex } from '@noble/hashes/utils.js'
import { KEY_KEY_PINS, localStorageGet, localStorageRemove, localStorageSet } from './browserStorage'
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

interface KeyPin { publicKeyHex: string, firstSeen: number }
type KeyPinMap = Record<string, KeyPin>

/** One pinned worker key, as the settings surface reads it. */
export interface KeyPinEntry {
  workerId: string
  publicKeyHex: string
  firstSeen: number
}

/** Thrown when the user rejects a key change (or auto-rejects in-session). */
export class KeyPinRejectedError extends Error {
  constructor(message = 'Worker public key rejected by user') {
    super(message)
    this.name = 'KeyPinRejectedError'
  }
}

/** List every pinned worker key, oldest first. */
export function listKeyPins(): KeyPinEntry[] {
  const pins = localStorageGet<KeyPinMap>(KEY_KEY_PINS) ?? {}
  return Object.entries(pins)
    .map(([workerId, pin]) => ({ workerId, publicKeyHex: pin.publicKeyHex, firstSeen: pin.firstSeen }))
    .sort((a, b) => a.firstSeen - b.firstSeen)
}

/** Remove a pinned key for a worker from browser storage. */
export function clearKeyPin(workerId: string): void {
  const allPins = localStorageGet<KeyPinMap>(KEY_KEY_PINS) ?? {}
  delete allPins[workerId]
  localStorageSet(KEY_KEY_PINS, allPins)
}

/** Remove all pinned keys from browser storage. */
export function clearAllKeyPins(): void {
  localStorageRemove(KEY_KEY_PINS)
}

export interface KeyPinStoreOpts {
  confirmKeyPin: KeyPinConfirmFn
}

/**
 * Session-scoped TOFU pin store. Persistence is shared across tabs via the
 * `key-pins` key, which `browserStorage` scopes to the signed-in account, so
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
   * Returns the `commit` the caller runs once the channel is proven, which records
   * this worker's key. Splitting the decision from the write is what keeps the write
   * correct: the open awaits the prompt, the handshake, and the WebSocket between
   * the two, and KEY_KEY_PINS holds EVERY worker's pin in one value. Reading the
   * whole map before those awaits and writing it back after would make the open
   * an unserialized read-modify-write over shared state -- and opens to different
   * workers are not serialized, so two interleaving opens would each write back a
   * map snapshot taken before the other's pin existed, silently dropping it. A
   * dropped pin is not a lost preference: the next open reads no pin, takes the
   * first-use branch, and re-pins whatever key the Hub serves WITHOUT prompting --
   * exactly the substitution the prompt defends against. So `commit` re-reads the
   * map and mutates only this worker's entry, all synchronously, and no snapshot
   * ever crosses an await.
   *
   * This closes the intra-tab race only; localStorage offers no compare-and-swap, so
   * two browser TABS opening channels at the same instant can still clobber each
   * other's pin. Narrowing the window to a single synchronous block is as far as this
   * API goes.
   *
   * Throws KeyPinRejectedError when the user rejects the new key.
   */
  async resolve(workerId: string, keyBundle: KeyPinKeyBundle): Promise<() => void> {
    const compositeKeyBytes = concatBytes(keyBundle.x25519PublicKey, keyBundle.mlkemPublicKey, keyBundle.slhdsaPublicKey)
    const publicKeyHex = bytesToHex(compositeKeyBytes)
    const pinned = localStorageGet<KeyPinMap>(KEY_KEY_PINS)?.[workerId] ?? null

    const commit = () => {
      const pins = localStorageGet<KeyPinMap>(KEY_KEY_PINS) ?? {}
      pins[workerId] = { publicKeyHex, firstSeen: Date.now() }
      localStorageSet(KEY_KEY_PINS, pins)
    }

    if (!pinned) {
      // First use: trust and record it.
      return commit
    }

    if (pinned.publicKeyHex === publicKeyHex) {
      // The key we already trust; nothing to write.
      return () => {}
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
