import type { SettingsStorePorts } from './settingsStore'
import type { SettingDescriptor, SettingValue } from '~/generated/proto/leapmux/v1/settings_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createSettingsStore } from './settingsStore'

function value(key: string, effectiveJson: string): SettingValue {
  return { key, valueJson: effectiveJson, effectiveJson, customized: true, secretSet: {} } as unknown as SettingValue
}

function descriptor(key: string): SettingDescriptor {
  return { key, category: 'general', title: key, summary: '', order: 10, fields: [] } as unknown as SettingDescriptor
}

/** A store with controllable ports, built inside a reactive root. */
function withStore<T>(
  ports: Partial<SettingsStorePorts>,
  run: (store: ReturnType<typeof createSettingsStore>) => T,
): T {
  return createRoot((dispose) => {
    const store = createSettingsStore({
      list: async () => ({ descriptors: [], values: [] }),
      update: async () => undefined,
      updateSecret: async () => undefined,
      reset: async () => undefined,
      enabled: () => true,
      guardMessage: 'this scope is not available',
      loadErrorFallback: 'Failed to load',
      ...ports,
    })
    try {
      return run(store)
    }
    finally {
      dispose()
    }
  })
}

describe('createSettingsStore load', () => {
  it('applies descriptors and values, keyed by setting key', async () => {
    await withStore({
      list: async () => ({
        descriptors: [descriptor('smtp')],
        values: [value('smtp', '{"port":587}')],
      }),
    }, async (store) => {
      await store.load()
      expect(store.state.loaded).toBe(true)
      expect(store.state.error).toBeNull()
      expect(store.values().get('smtp')?.effectiveJson).toBe('{"port":587}')
    })
  })

  it('records a load failure for the surface to render', async () => {
    await withStore({
      list: async () => {
        throw new Error('network is unreachable')
      },
    }, async (store) => {
      await store.load()
      expect(store.state.error).toContain('network is unreachable')
      expect(store.state.loaded).toBe(true)
    })
  })

  // Two loads in flight must not let the earlier ANSWER win over the later
  // ASK: the reply that lands second is not necessarily the request that
  // was issued second.
  it('ignores a superseded load reply', async () => {
    let releaseFirst: (v: { descriptors: SettingDescriptor[], values: SettingValue[] }) => void = () => {}
    let call = 0
    await withStore({
      list: () => {
        call++
        if (call === 1)
          return new Promise((r) => { releaseFirst = r })
        return Promise.resolve({ descriptors: [descriptor('new')], values: [value('new', '2')] })
      },
    }, async (store) => {
      const first = store.load()
      const second = store.load()
      await second
      releaseFirst({ descriptors: [descriptor('stale')], values: [value('stale', '1')] })
      await first

      expect(store.values().has('new')).toBe(true)
      expect(store.values().has('stale')).toBe(false)
    })
  })

  // A session that lost access must not keep serving the previous one's
  // settings.
  it('clears state instead of asking while the guard denies access', async () => {
    const list = vi.fn(async () => ({ descriptors: [descriptor('smtp')], values: [] }))
    let allowed = true
    await withStore({ list, enabled: () => allowed, guardMessage: 'admin only' }, async (store) => {
      await store.load()
      expect(store.state.descriptors).toHaveLength(1)

      allowed = false
      await store.load()
      expect(list).toHaveBeenCalledTimes(1)
      expect(store.state.descriptors).toHaveLength(0)
      expect(store.state.error).toBeNull()
    })
  })
})

describe('createSettingsStore mutations', () => {
  it('merges a reply without disturbing the other keys', async () => {
    await withStore({
      list: async () => ({
        descriptors: [descriptor('a'), descriptor('b')],
        values: [value('a', '1'), value('b', '2')],
      }),
      update: async () => value('a', '9'),
    }, async (store) => {
      await store.load()
      await store.update('a', '9')
      expect(store.values().get('a')?.effectiveJson).toBe('9')
      expect(store.values().get('b')?.effectiveJson).toBe('2')
      expect(store.state.writeError).toBeNull()
    })
  })

  it('records a failed write against the key that failed', async () => {
    await withStore({
      update: async () => {
        throw new Error('cross-validation failed')
      },
    }, async (store) => {
      await expect(store.update('smtp', '{}')).rejects.toThrow('cross-validation failed')
      expect(store.state.writeError).toEqual({
        key: 'smtp',
        message: expect.stringContaining('cross-validation failed'),
      })
    })
  })

  // The reply guard is still needed beside the request queue, and what it
  // decides is the WINDOW between the two replies. The queue holds the
  // next REQUEST, so the replies now arrive in issue order and the value
  // left at the end is the newest one either way. But each write takes its
  // sequence when it is ISSUED, so the older reply lands while the newer
  // write is still in flight. Applied, it puts the value the user just
  // replaced back on the control until the newer reply arrives -- two fast
  // clicks on a toggle, and it snaps back for that whole window.
  it('ignores a superseded write reply while the newer write is in flight', async () => {
    let releaseFirst: (v: SettingValue) => void = () => {}
    let releaseSecond: (v: SettingValue) => void = () => {}
    let call = 0
    await withStore({
      update: () => {
        call++
        if (call === 1) {
          return new Promise<SettingValue>((r) => {
            releaseFirst = r
          })
        }
        // Held open as well, so the assertion below reads the window
        // between the two replies and not the state after both.
        return new Promise<SettingValue>((r) => {
          releaseSecond = r
        })
      },
    }, async (store) => {
      const first = store.update('flag', 'false')
      // Issued while the first is still in flight, so it takes sequence 2
      // and the first reply is stale before it even arrives.
      const second = store.update('flag', 'true')
      releaseFirst(value('flag', 'false'))
      await first

      // The superseded reply carries the value the user replaced. Nothing
      // has merged for the key yet, and nothing may.
      expect(store.values().has('flag')).toBe(false)

      releaseSecond(value('flag', 'true'))
      await second
      expect(store.values().get('flag')?.effectiveJson).toBe('true')
    })
  })

  // `writeError` is ONE slot for the whole store, so the same guard has a
  // second job: a superseded SUCCESS must not clear the slot either. The
  // failure it would erase belongs to a write the user made LATER, and it
  // is the text on screen under that row. Keys queue independently, so the
  // stale reply on one key and the failure on another are in flight
  // together.
  it('does not let a superseded reply clear a failure on another key', async () => {
    let releaseStaleFonts: (v: SettingValue) => void = () => {}
    let fontsCalls = 0
    await withStore({
      update: async (key, partialJson) => {
        if (key === 'flag')
          throw new Error('the hub refused the flag write')
        fontsCalls++
        if (fontsCalls === 1)
          return new Promise<SettingValue>((r) => { releaseStaleFonts = r })
        return value(key, partialJson)
      },
    }, async (store) => {
      const stale = store.update('fonts', '{"fonts":["A"]}')
      const newer = store.update('fonts', '{"fonts":["A","B"]}')
      await expect(store.update('flag', 'true')).rejects.toThrow('refused the flag write')
      expect(store.state.writeError?.key).toBe('flag')

      releaseStaleFonts(value('fonts', '{"fonts":["A"]}'))
      await stale
      // The stale reply is a success, but it is not the newest write for
      // `fonts`, so it must leave the failure the user is reading alone.
      expect(store.state.writeError?.key).toBe('flag')

      await newer
    })
  })

  // A superseded FAILURE must not state itself either: it would sit beside
  // the value a later write stored successfully.
  it('does not record a superseded write failure', async () => {
    let rejectFirst: (err: Error) => void = () => {}
    let releaseSecond: (v: SettingValue) => void = () => {}
    let call = 0
    await withStore({
      update: () => {
        call++
        if (call === 1)
          return new Promise<SettingValue>((_r, reject) => { rejectFirst = reject })
        return new Promise<SettingValue>((r) => {
          releaseSecond = r
        })
      },
    }, async (store) => {
      const first = store.update('flag', 'false').catch(() => {})
      const second = store.update('flag', 'true')
      rejectFirst(new Error('the hub refused the first write'))
      await first

      // The second write has NOT replied yet, so nothing could have
      // cleared a recorded error: the guard is the only reason there is
      // none.
      expect(store.state.writeError).toBeNull()

      releaseSecond(value('flag', 'true'))
      await second
    })
  })

  // Writes to DIFFERENT keys are independent and must not cancel each
  // other — the sequence is per key, not global.
  it('keeps two keys writing independently', async () => {
    await withStore({
      update: async (key, partial) => value(key, partial),
    }, async (store) => {
      await Promise.all([store.update('a', '1'), store.update('b', '2')])
      expect(store.values().get('a')?.effectiveJson).toBe('1')
      expect(store.values().get('b')?.effectiveJson).toBe('2')
    })
  })

  // The sequence decides which REPLY is applied; it cannot decide which
  // REQUEST the hub commits first. The hub merges a partial under a row
  // lock, so the request that COMMITS LAST is the one it keeps -- and both
  // replies report success, so no reply guard can detect the loss. The
  // second request must therefore not be issued until the first settles.
  it('commits two writes to one key in the order they were issued', async () => {
    let releaseFirst: (v: SettingValue) => void = () => {}
    const committed: string[] = []
    await withStore({
      update: (key, partialJson) => {
        committed.push(partialJson)
        if (committed.length === 1)
          return new Promise<SettingValue>((r) => { releaseFirst = r })
        return Promise.resolve(value(key, partialJson))
      },
    }, async (store) => {
      const first = store.update('fonts', '{"fonts":["A"]}')
      const second = store.update('fonts', '{"fonts":["A","B"]}')

      // The second write has not reached the hub while the first is in
      // flight, so the hub cannot commit them out of order.
      await Promise.resolve()
      expect(committed).toEqual(['{"fonts":["A"]}'])

      releaseFirst(value('fonts', '{"fonts":["A"]}'))
      await first
      await second
      expect(committed).toEqual(['{"fonts":["A"]}', '{"fonts":["A","B"]}'])
      expect(store.values().get('fonts')?.effectiveJson).toBe('{"fonts":["A","B"]}')
    })
  })

  // A refused write must not stall its key: the user's next edit is the one
  // thing that can repair the failure they just saw.
  it('issues the next write for a key after the previous one fails', async () => {
    let rejectFirst: (err: Error) => void = () => {}
    const issued: string[] = []
    await withStore({
      update: (key, partialJson) => {
        issued.push(partialJson)
        if (issued.length === 1)
          return new Promise<SettingValue>((_r, reject) => { rejectFirst = reject })
        return Promise.resolve(value(key, partialJson))
      },
    }, async (store) => {
      const first = store.update('flag', 'false').catch(() => {})
      const second = store.update('flag', 'true')
      rejectFirst(new Error('the hub refused the first write'))
      await first
      await second
      expect(issued).toEqual(['false', 'true'])
      expect(store.values().get('flag')?.effectiveJson).toBe('true')
    })
  })

  it('refuses every mutation while the guard denies access', async () => {
    await withStore({ enabled: () => false, guardMessage: 'admin only' }, async (store) => {
      await expect(store.update('a', '1')).rejects.toThrow('admin only')
      await expect(store.reset('a')).rejects.toThrow('admin only')
      await expect(store.updateSecret('a', '{}')).rejects.toThrow('admin only')
    })
  })
})
