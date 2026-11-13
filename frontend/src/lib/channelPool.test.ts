import type { PooledChannel } from './channelPool'
import { describe, expect, it, vi } from 'vitest'
import { ChannelPool } from './channelPool'

function ch(partial: Partial<PooledChannel> & Pick<PooledChannel, 'channelId' | 'workerId'>): PooledChannel {
  return {
    userId: 'user-a',
    state: 'verified',
    ...partial,
  }
}

describe('channelPool', () => {
  it('treats undefined expected as no check and empty as mismatch in identityMismatch', () => {
    const pool = new ChannelPool()
    expect(pool.identityMismatch(undefined, 'u1')).toBe(false)
    expect(pool.identityMismatch('u1', 'u1')).toBe(false)
    expect(pool.identityMismatch('u1', 'u2')).toBe(true)
    expect(pool.identityMismatch('', 'u1')).toBe(true)
  })

  it('mirrors identityMismatch in identityDrift against the pooled userId', () => {
    const pool = new ChannelPool()
    const verified = ch({ channelId: 'c1', workerId: 'w1', userId: 'user-a' })
    expect(pool.identityDrift(verified, undefined)).toBe(false)
    expect(pool.identityDrift(verified, 'user-a')).toBe(false)
    expect(pool.identityDrift(verified, 'user-b')).toBe(true)
    expect(pool.identityDrift(verified, '')).toBe(true)
  })

  it('skips opening and identity-drifted channels in hasOpenChannel', () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1', state: 'opening' }))
    expect(pool.hasOpenChannel('w1', 'user-a')).toBe(false)

    pool.set('c1', ch({
      channelId: 'c1',
      workerId: 'w1',
      state: 'verified',
      userId: 'user-a',
    }))
    expect(pool.hasOpenChannel('w1', 'user-a')).toBe(true)
    expect(pool.hasOpenChannel('w1', 'user-b')).toBe(false)
    expect(pool.hasOpenChannelForWorker('w1')).toBe(true)
  })

  it('reuses a verified channel in getOrOpenChannel', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1' }))
    const openChannel = vi.fn(async () => 'new')
    const id = await pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async () => {},
      ensureReady: async () => {},
      expectedUserId: () => 'user-a',
    })
    expect(id).toBe('c1')
    expect(openChannel).not.toHaveBeenCalled()
  })

  it('skips opening channels and dedups via openChannel in getOrOpenChannel', async () => {
    const pool = new ChannelPool()
    pool.set('c-open', ch({ channelId: 'c-open', workerId: 'w1', state: 'opening' }))
    let resolveOpen!: (id: string) => void
    const openPromise = new Promise<string>((resolve) => {
      resolveOpen = resolve
    })
    const openChannel = vi.fn(() => openPromise)

    const a = pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async () => {},
      ensureReady: async () => {},
      expectedUserId: () => undefined,
    })
    const b = pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async () => {},
      ensureReady: async () => {},
      expectedUserId: () => undefined,
    })
    expect(openChannel).toHaveBeenCalledOnce()
    resolveOpen('c-new')
    expect(await a).toBe('c-new')
    expect(await b).toBe('c-new')
  })

  it('closes and reopens on identity drift in getOrOpenChannel', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1', userId: 'user-a' }))
    const closeChannel = vi.fn(async () => {
      pool.delete('c1')
    })
    const openChannel = vi.fn(async () => {
      pool.set('c2', ch({ channelId: 'c2', workerId: 'w1', userId: 'user-b' }))
      return 'c2'
    })
    const id = await pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel,
      ensureReady: async () => {},
      expectedUserId: () => 'user-b',
    })
    expect(closeChannel).toHaveBeenCalledWith('c1')
    expect(openChannel).toHaveBeenCalledOnce()
    expect(id).toBe('c2')
  })

  // Past the hard ceiling the session worker ends the session, which closes the
  // channel, before it answers.
  it('reopens in getOrOpenChannel when ensureReady rejects because the session ended', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1' }))
    const closeChannel = vi.fn(async () => {})
    const openChannel = vi.fn(async () => 'c2')
    const id = await pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel,
      ensureReady: async () => {
        pool.delete('c1')
        throw new Error('session key past hard ceiling')
      },
      expectedUserId: () => undefined,
    })
    expect(closeChannel).not.toHaveBeenCalled()
    expect(openChannel).toHaveBeenCalledOnce()
    expect(id).toBe('c2')
  })

  it('closes a channel that ensureReady rejected but left in the pool, so the rescan cannot ask it again', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1' }))
    const closeChannel = vi.fn(async (id: string) => {
      pool.delete(id)
    })
    const ensureReady = vi.fn(async () => {
      throw new Error('channel session worker is not running')
    })
    const openChannel = vi.fn(async () => 'c2')
    const id = await pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel,
      ensureReady,
      expectedUserId: () => undefined,
    })
    expect(ensureReady).toHaveBeenCalledOnce()
    expect(closeChannel).toHaveBeenCalledExactlyOnceWith('c1')
    expect(id).toBe('c2')
  })

  it('falls through to reopen in getOrOpenChannel when ensureReady rejects after a rekey timeout', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1' }))
    const openChannel = vi.fn(async () => 'c2')
    const id = await pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async (id) => { pool.delete(id) },
      ensureReady: async () => {
        pool.delete('c1')
        throw new Error('rekey timeout')
      },
      expectedUserId: () => undefined,
    })
    expect(openChannel).toHaveBeenCalledOnce()
    expect(id).toBe('c2')
  })

  it('reopens in getOrOpenChannel when ensureReady leaves the channel unverified', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1' }))
    const openChannel = vi.fn(async () => 'c2')
    const id = await pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async () => {},
      ensureReady: async () => {
        // Soft close without throwing: still must not hand out a dead channel.
        pool.set('c1', ch({ channelId: 'c1', workerId: 'w1', state: 'closed' }))
      },
      expectedUserId: () => undefined,
    })
    expect(openChannel).toHaveBeenCalledOnce()
    expect(id).toBe('c2')
  })

  it('drops in-flight dedup in clearOpening so a later open may dial again', async () => {
    const pool = new ChannelPool()
    let resolveOpen!: (id: string) => void
    const openPromise = new Promise<string>((resolve) => {
      resolveOpen = resolve
    })
    const openChannel = vi.fn(() => openPromise)

    const first = pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async () => {},
      ensureReady: async () => {},
      expectedUserId: () => undefined,
    })
    expect(openChannel).toHaveBeenCalledOnce()

    pool.clearOpening()
    resolveOpen('c-stale')
    await expect(first).resolves.toBe('c-stale')

    const openChannel2 = vi.fn(async () => 'c-fresh')
    const second = await pool.getOrOpenChannel('w1', {
      openChannel: openChannel2,
      closeChannel: async () => {},
      ensureReady: async () => {},
      expectedUserId: () => undefined,
    })
    expect(openChannel2).toHaveBeenCalledOnce()
    expect(second).toBe('c-fresh')
  })

  it('increments the fence through bumpCloseGeneration', () => {
    const pool = new ChannelPool()
    expect(pool.closeGeneration).toBe(0)
    expect(pool.bumpCloseGeneration()).toBe(1)
    expect(pool.closeGeneration).toBe(1)
    // Getter-only: assignment must not corrupt the fence.
    expect(() => {
      ;(pool as { closeGeneration: number }).closeGeneration = 0
    }).toThrow(TypeError)
    expect(pool.closeGeneration).toBe(1)
  })

  it('re-scans after close in getOrOpenChannel so a concurrent verified channel is reused', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1', userId: 'user-a' }))
    let releaseClose!: () => void
    const closeGate = new Promise<void>((resolve) => {
      releaseClose = resolve
    })
    const openChannel = vi.fn(async () => 'c3')
    const pending = pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async (id) => {
        await closeGate
        pool.delete(id)
      },
      ensureReady: async () => {},
      expectedUserId: () => 'user-b', // drift on c1
    })
    await Promise.resolve()
    pool.delete('c1')
    pool.set('c2', ch({ channelId: 'c2', workerId: 'w1', userId: 'user-b' }))
    releaseClose()
    await expect(pending).resolves.toBe('c2')
    expect(openChannel).not.toHaveBeenCalled()
  })

  it('re-scans after ensureReady in getOrOpenChannel so a concurrent verified channel is reused', async () => {
    const pool = new ChannelPool()
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1' }))
    let releaseRekey!: () => void
    const rekeyGate = new Promise<void>((resolve) => {
      releaseRekey = resolve
    })
    const openChannel = vi.fn(async () => 'c3')
    const pending = pool.getOrOpenChannel('w1', {
      openChannel,
      closeChannel: async () => {},
      ensureReady: async () => {
        await rekeyGate
        // Concurrent open replaced c1 while we were parked on rekey.
        pool.delete('c1')
        pool.set('c2', ch({ channelId: 'c2', workerId: 'w1' }))
      },
      expectedUserId: () => undefined,
    })
    await Promise.resolve()
    releaseRekey()
    await expect(pending).resolves.toBe('c2')
    expect(openChannel).not.toHaveBeenCalled()
  })

  it('single-flights without consulting the verified cache in dedupeOpen', async () => {
    const pool = new ChannelPool()
    pool.set('existing', ch({ channelId: 'existing', workerId: 'w1', state: 'verified' }))
    let resolveOpen!: (id: string) => void
    const openPromise = new Promise<string>((resolve) => {
      resolveOpen = resolve
    })
    const factory = vi.fn(() => openPromise)

    const a = pool.dedupeOpen('w1', factory)
    const b = pool.dedupeOpen('w1', factory)
    expect(factory).toHaveBeenCalledOnce()
    resolveOpen('forced')
    await expect(Promise.all([a, b])).resolves.toEqual(['forced', 'forced'])
    // Verified cache entry is untouched — force-open is the caller's job to replace.
    expect(pool.get('existing')?.channelId).toBe('existing')
  })

  it('reflects the map state in isOpen', () => {
    const pool = new ChannelPool()
    expect(pool.isOpen('c1')).toBe(false)
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1' }))
    expect(pool.isOpen('c1')).toBe(true)
    expect(pool.get('c1')?.workerId).toBe('w1')
    pool.set('c1', ch({ channelId: 'c1', workerId: 'w1', state: 'closed' }))
    expect(pool.isOpen('c1')).toBe(false)
  })
})
