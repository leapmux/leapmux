import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { localStorageGet, localStorageSet, PREFIX_FILES_SHOW_HIDDEN } from '~/lib/browserStorage'
import { createPersistedSignal, persistedBoolean } from '~/lib/createPersistedSignal'
import { flush } from '~/test-support/async'

const KEY_A = `${PREFIX_FILES_SHOW_HIDDEN}w1:/a`
const KEY_B = `${PREFIX_FILES_SHOW_HIDDEN}w1:/b`

describe('createPersistedSignal', () => {
  it('seeds from the stored value under the initial key', async () => {
    localStorageSet(KEY_A, false)
    await createRoot(async (dispose) => {
      const [value] = createPersistedSignal(() => KEY_A, persistedBoolean(true))
      expect(value()).toBe(false)
      dispose()
    })
  })

  it('falls back to the parse default when nothing is stored', async () => {
    await createRoot(async (dispose) => {
      const [value] = createPersistedSignal(() => `${PREFIX_FILES_SHOW_HIDDEN}w1:/never-written`, persistedBoolean(true))
      expect(value()).toBe(true)
      dispose()
    })
  })

  it('does NOT write on mount', async () => {
    const key = `${PREFIX_FILES_SHOW_HIDDEN}w1:/mount-only`
    await createRoot(async (dispose) => {
      createPersistedSignal(() => key, persistedBoolean(true))
      await flush()
      // A write on mount would refresh the key's TTL and resurrect a key the
      // storage sweep was about to drop.
      expect(localStorageGet(key)).toBeUndefined()
      dispose()
    })
  })

  it('persists a changed value under the current key', async () => {
    const key = `${PREFIX_FILES_SHOW_HIDDEN}w1:/persist`
    await createRoot(async (dispose) => {
      const [, setValue] = createPersistedSignal(() => key, persistedBoolean(true))
      // Let both effects establish their baseline first. `on(..., {defer:true})`
      // skips its first run, so a change made before that run looks like the
      // initial value and is never persisted. A mounted component always
      // flushes before a user can click.
      await flush()
      setValue(false)
      await flush()
      expect(localStorageGet(key)).toBe(false)
      dispose()
    })
  })

  it('re-reads when the key changes, and writes back under the new key', async () => {
    localStorageSet(KEY_A, false)
    localStorageSet(KEY_B, true)
    await createRoot(async (dispose) => {
      const [key, setKey] = createSignal(KEY_A)
      const [value, setValue] = createPersistedSignal(key, persistedBoolean(true))
      await flush()
      expect(value()).toBe(false)

      setKey(KEY_B)
      await flush()
      expect(value(), 'switching scope must load that scope\'s value').toBe(true)

      setValue(false)
      await flush()
      expect(localStorageGet(KEY_B)).toBe(false)
      // The previous scope keeps what it had.
      expect(localStorageGet(KEY_A)).toBe(false)
      dispose()
    })
  })

  it('re-reads the default when the new key has nothing stored', async () => {
    localStorageSet(KEY_A, false)
    await createRoot(async (dispose) => {
      const [key, setKey] = createSignal(KEY_A)
      const [value] = createPersistedSignal(key, persistedBoolean(true))
      await flush()
      expect(value()).toBe(false)

      setKey(`${PREFIX_FILES_SHOW_HIDDEN}w1:/fresh`)
      await flush()
      // Not carried over from the previous scope.
      expect(value()).toBe(true)
      dispose()
    })
  })

  it('routes a malformed stored value through parse', async () => {
    const key = `${PREFIX_FILES_SHOW_HIDDEN}w1:/malformed`
    localStorageSet(key, 'not-a-boolean')
    await createRoot(async (dispose) => {
      const [value] = createPersistedSignal(() => key, persistedBoolean(true))
      expect(value()).toBe(true)
      dispose()
    })
  })

  it('supports a functional setter', async () => {
    const key = `${PREFIX_FILES_SHOW_HIDDEN}w1:/toggle`
    await createRoot(async (dispose) => {
      const [value, setValue] = createPersistedSignal(() => key, persistedBoolean(true))
      await flush()
      setValue(prev => !prev)
      await flush()
      expect(value()).toBe(false)
      expect(localStorageGet(key)).toBe(false)
      dispose()
    })
  })
})

describe('persistedBoolean', () => {
  it('accepts a stored boolean and rejects anything else', () => {
    const parse = persistedBoolean(true)
    expect(parse(false)).toBe(false)
    expect(parse(true)).toBe(true)
    expect(parse(undefined)).toBe(true)
    expect(parse(null)).toBe(true)
    expect(parse('false')).toBe(true)
    expect(parse(0)).toBe(true)
  })
})
