/**
 * Frontend E2EE channel pool: the channels Map, per-worker open dedup,
 * closeGeneration fence, identity cross-check helpers, and getOrOpenChannel
 * reuse/reopen policy. Extracted from ChannelManager so the pool state machine
 * is unit-testable without a Noise/WebSocket harness.
 *
 * Open orchestration (pin → handshake → register `'opening'` → Ping → commit →
 * `'verified'`) stays on ChannelManager; this type only caches and hands out
 * already-opened channels.
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import { createInflightCache } from './inflightCache'
import { createLogger } from './logger'

const log = createLogger('channel')

/**
 * The slice of an ActiveChannel the pool cares about for reuse / identity /
 * observability gates.
 */
export interface PooledChannel {
  channelId: string
  workerId: string
  userId: string
  state: 'opening' | 'verified' | 'closed'
}

export interface ChannelPoolGetOrOpenDeps<T extends PooledChannel> {
  openChannel: (workerId: string) => Promise<string>
  closeChannel: (channelId: string) => Promise<void>
  pastHardCeiling: (ch: T) => boolean
  shouldInitiateRekey: (ch: T) => boolean
  ensureRekeyed: (ch: T) => Promise<void>
  expectedUserId: () => string | undefined
}

/**
 * Owns the live-channel map, open single-flight cache, and closeGeneration fence.
 */
export class ChannelPool<T extends PooledChannel = PooledChannel> {
  private channels = new Map<string, T>()
  /** In-flight openChannel promises per worker, for deduplication. */
  private openingChannels = createInflightCache<string, string>()
  /**
   * Bumped by closeAll so an open that was parked on an await when closeAll
   * snapshotted `channels` cannot register afterward and slip past the close.
   * Private + getter so callers cannot corrupt the fence by assignment.
   */
  private _closeGeneration = 0

  get closeGeneration(): number {
    return this._closeGeneration
  }

  get(channelId: string): T | undefined {
    return this.channels.get(channelId)
  }

  set(channelId: string, ch: T): void {
    this.channels.set(channelId, ch)
  }

  delete(channelId: string): boolean {
    return this.channels.delete(channelId)
  }

  has(channelId: string): boolean {
    return this.channels.has(channelId)
  }

  get size(): number {
    return this.channels.size
  }

  values(): IterableIterator<T> {
    return this.channels.values()
  }

  keys(): IterableIterator<string> {
    return this.channels.keys()
  }

  entries(): IterableIterator<[string, T]> {
    return this.channels.entries()
  }

  /** Underlying map — ChannelManager re-exposes this as a private `channels` test seam. */
  asMap(): Map<string, T> {
    return this.channels
  }

  clearOpening(): void {
    this.openingChannels.clear()
  }

  /**
   * Single-flight an open factory for `workerId` without consulting the
   * verified-channel cache. Used by ChannelManager.openChannel so parallel
   * direct opens cannot register two Hub IDs for one worker, while still
   * allowing a forced reopen (e.g. after a key rotation) when a verified
   * channel already exists.
   */
  dedupeOpen(workerId: string, factory: () => Promise<string>): Promise<string> {
    return this.openingChannels.run(workerId, factory)
  }

  /** Bump the close fence; returns the new generation. */
  bumpCloseGeneration(): number {
    this._closeGeneration++
    return this._closeGeneration
  }

  /**
   * Whether the identity this page expects disagrees with the one the Hub
   * authenticated. An `undefined` `expected` is NOT a mismatch: the page has no
   * expectation yet (e.g. before the auth context resolves) and the Hub stays
   * authoritative. An EMPTY-STRING `expected` IS a mismatch against any non-empty
   * Hub identity: only the "not resolved yet" case (undefined) may skip the check,
   * whereas `''` is a degenerate/corrupt id we must not silently treat as "no
   * expectation" and serve a channel bound to a different user for.
   */
  identityMismatch(expected: string | undefined, actual: string): boolean {
    return expected !== undefined && expected !== actual
  }

  /**
   * Whether a verified pooled channel must be closed and reopened for identity
   * drift. Age uses in-band rekey until the hard ceiling, then close.
   */
  identityDrift(ch: T, expectedUserId: string | undefined): boolean {
    return this.identityMismatch(expectedUserId, ch.userId)
  }

  /** Check if a channel is open (present and not mid-teardown). */
  isOpen(channelId: string): boolean {
    const ch = this.channels.get(channelId)
    return ch !== undefined && ch.state !== 'closed'
  }

  /**
   * Whether a usable channel to this worker already exists for indicator /
   * subscription-retire callers. Mirrors getOrOpenChannel's reuse test,
   * `verified` included; also skips identity-drifted channels.
   */
  hasOpenChannel(workerId: string, expectedUserId: string | undefined): boolean {
    for (const ch of this.channels.values()) {
      if (ch.workerId === workerId && ch.state === 'verified'
        && !this.identityMismatch(expectedUserId, ch.userId)) {
        return true
      }
    }
    return false
  }

  /**
   * Whether a verified channel to this worker exists (no identity check).
   * For callers that only have something to say IF a channel is already up.
   */
  hasOpenChannelForWorker(workerId: string): boolean {
    for (const ch of this.channels.values()) {
      if (ch.workerId === workerId && ch.state === 'verified')
        return true
    }
    return false
  }

  /** Get an open channel for a worker, or open a new one via deps.openChannel. */
  async getOrOpenChannel(workerId: string, deps: ChannelPoolGetOrOpenDeps<T>): Promise<string> {
    // After any await (close / rekey), re-scan: a concurrent open may have
    // registered another verified channel for this worker while we were parked.
    for (;;) {
      let rescan = false
      for (const [channelId, ch] of this.channels) {
        // `state === 'verified'` and not merely "not closed": an open in progress has
        // already put its channel here so the verification Ping's reply can route, but
        // that session is unproven. Skipping it drops through to openingChannels.run
        // below, which dedups this caller onto the very same in-flight open -- so a
        // racer waits for the ping instead of being handed the channel the ping might
        // yet reject.
        if (ch.workerId === workerId && ch.state === 'verified') {
          if (this.identityDrift(ch, deps.expectedUserId())) {
            log.debug('reopening pooled channel after identity drift', { channel_id: channelId, worker_id: workerId })
            await deps.closeChannel(channelId)
            rescan = true
            break
          }
          if (deps.pastHardCeiling(ch)) {
            log.debug('reopening pooled channel past session-key hard ceiling', { channel_id: channelId, worker_id: workerId })
            await deps.closeChannel(channelId)
            rescan = true
            break
          }
          try {
            if (deps.shouldInitiateRekey(ch))
              await deps.ensureRekeyed(ch)
          }
          catch {
            // Ack timeout closes the channel and rejects; fall through to reopen
            // rather than surfacing a rotation error to the caller.
            rescan = true
            break
          }
          // ensureRekeyed may close the channel on Ack timeout (caught above),
          // hard-ceiling reject, or a send failure.
          if (!this.channels.has(channelId) || this.channels.get(channelId)?.state !== 'verified') {
            rescan = true
            break
          }
          return channelId
        }
      }
      if (rescan)
        continue
      return this.openingChannels.run(workerId, () => deps.openChannel(workerId))
    }
  }
}
