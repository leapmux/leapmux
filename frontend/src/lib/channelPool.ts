import type { StorageAccount } from './browserStorage'
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
import type { WorkerTrustScope } from '~/context/AuthContext'
import { ChannelError } from './channelError'
import { createInflightCache } from './inflightCache'
import { createLogger } from './logger'

const log = createLogger('channel')

export interface ChannelOpenScope {
  identity: WorkerTrustScope | undefined
  accountId: StorageAccount | null
  signal: AbortSignal
  poolGeneration: number
  assertCurrent: () => void
}

export type ChannelOpenMode = 'ordinary' | 'review' | 'first-contact-review'

function createWorkerOpenFlights() {
  return {
    ordinary: createInflightCache<string, string>(),
    review: createInflightCache<string, string>(),
    firstContactReview: createInflightCache<string, string>(),
  }
}

/**
 * The channel fields the pool uses for reuse and reporting.
 */
export interface PooledChannel {
  channelId: string
  workerId: string
  userId: string
  scope: ChannelOpenScope
  state: 'opening' | 'verified' | 'closed'
}

export interface ChannelPoolGetOrOpenDeps<T extends PooledChannel> {
  openChannel: (workerId: string) => Promise<string>
  closeChannel: (channelId: string) => Promise<void>
  /**
   * Make the channel's session ready to serve: close it past the hard ceiling,
   * or rekey it when a rekey is due. Rejects when the session cannot serve, which
   * closes the channel too.
   */
  ensureReady: (ch: T) => Promise<void>
}

/**
 * Owns the live-channel map, open single-flight cache, and closeGeneration fence.
 */
export class ChannelPool<T extends PooledChannel = PooledChannel> {
  private channels = new Map<string, T>()
  private openingChannels = new WeakMap<ChannelOpenScope, ReturnType<typeof createWorkerOpenFlights>>()
  /**
   * Bumped by closeAll so an open that was parked on an await when closeAll
   * snapshotted `channels` cannot register afterward and slip past the close.
   * Private + getter so callers cannot corrupt the fence by assignment.
   */
  private _closeGeneration = 0
  private closeController = new AbortController()

  get closeGeneration(): number {
    return this._closeGeneration
  }

  get closeSignal(): AbortSignal {
    return this.closeController.signal
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
    this.openingChannels = new WeakMap()
  }

  /**
   * Single-flight an open factory for `workerId` without consulting the
   * verified-channel cache. Used by ChannelManager.openChannel so parallel
   * direct opens cannot register two Hub IDs for one worker, while still
   * allowing a forced reopen (e.g. after the user accepts a changed key) when a verified
   * channel already exists.
   */
  dedupeOpen(
    workerId: string,
    scope: ChannelOpenScope,
    factory: () => Promise<string>,
    mode: ChannelOpenMode = 'ordinary',
  ): Promise<string> {
    scope.assertCurrent()
    let flights = this.openingChannels.get(scope)
    if (flights === undefined) {
      flights = createWorkerOpenFlights()
      this.openingChannels.set(scope, flights)
    }
    // Different policies serialize but never share a result. Each one must
    // read trust again after the preceding policy finishes. Calls with the
    // same policy still share one flight.
    const flightMode = mode === 'first-contact-review'
      ? 'firstContactReview'
      : mode
    const previous = Object.entries(flights)
      .filter(([candidate]) => candidate !== flightMode)
      .map(([, cache]) => cache.get(workerId))
      .filter((flight): flight is Promise<string> => flight !== undefined)
    return flights[flightMode].run(workerId, async () => {
      if (previous.length > 0) {
        await Promise.all(previous.map(flight => flight.catch(() => undefined)))
        scope.assertCurrent()
      }
      return factory()
    })
  }

  /** Retire the current generation before allowing successor opens. */
  bumpCloseGeneration(
    reason = new ChannelError('transport', 'open channel: superseded by a concurrent closeAll'),
  ): number {
    const previous = this.closeController
    this.closeController = new AbortController()
    const generation = ++this._closeGeneration
    this.clearOpening()
    previous.abort(reason)
    return generation
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

  private canReuse(ch: T, scope: ChannelOpenScope): boolean {
    return ch.scope === scope && !this.identityMismatch(scope.identity?.userId ?? undefined, ch.userId)
  }

  /** Check if a channel is open (present and not mid-teardown). */
  isOpen(channelId: string): boolean {
    const ch = this.channels.get(channelId)
    return ch !== undefined && ch.state !== 'closed'
  }

  /** Check whether the current scope has a verified channel to the Worker. */
  hasOpenChannel(workerId: string, scope: ChannelOpenScope): boolean {
    if (scope.signal.aborted)
      return false
    for (const ch of this.channels.values()) {
      if (ch.workerId === workerId && ch.state === 'verified' && this.canReuse(ch, scope)) {
        return true
      }
    }
    return false
  }

  /** Get an open channel for a worker, or open a new one via deps.openChannel. */
  async getOrOpenChannel(workerId: string, scope: ChannelOpenScope, deps: ChannelPoolGetOrOpenDeps<T>): Promise<string> {
    // After any await (close / ensureReady), re-scan: a concurrent open may have
    // registered another verified channel for this worker while we were parked.
    for (;;) {
      scope.assertCurrent()
      const pending = this.openingChannels.get(scope)?.ordinary.get(workerId)
      if (pending !== undefined) {
        const channelId = await pending
        scope.assertCurrent()
        return channelId
      }
      let rescan = false
      for (const [channelId, ch] of this.channels) {
        // `state === 'verified'` and not merely "not closed": an open in progress has
        // already put its channel here so the verification Ping's reply can route, but
        // that session is unproven. Skipping it drops through to openingChannels.run
        // below, which dedups this caller onto the very same in-flight open -- so a
        // racer waits for the ping instead of being handed the channel the ping might
        // yet reject.
        if (ch.workerId === workerId && ch.state === 'verified') {
          if (!this.canReuse(ch, scope)) {
            log.debug('reopening pooled channel after identity drift', { channel_id: channelId, worker_id: workerId })
            await deps.closeChannel(channelId)
            rescan = true
            break
          }
          try {
            await deps.ensureReady(ch)
          }
          catch (err) {
            scope.assertCurrent()
            // The session ended (the hard ceiling, an Ack timeout, a dead session
            // worker), which closes the channel; fall through to reopen rather
            // than surfacing a rotation error to the caller. Close a channel that
            // is still pooled here: the rescan would otherwise find it again and
            // ask the same session forever.
            log.debug('reopening pooled channel after its session could not serve', { channel_id: channelId, worker_id: workerId, error: err })
            if (this.channels.get(channelId) === ch)
              await deps.closeChannel(channelId)
            rescan = true
            break
          }
          scope.assertCurrent()
          if (this.channels.get(channelId) !== ch || ch.state !== 'verified' || this.openingChannels.get(scope)?.ordinary.has(workerId)) {
            rescan = true
            break
          }
          return channelId
        }
      }
      if (rescan)
        continue
      const channelId = await this.dedupeOpen(workerId, scope, () => deps.openChannel(workerId))
      scope.assertCurrent()
      return channelId
    }
  }
}
