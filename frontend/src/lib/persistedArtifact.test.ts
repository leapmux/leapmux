import { beforeEach, describe, expect, it, vi } from 'vitest'

// Declared BEFORE the `vi.mock` factories that close over them. `vi.mock` is
// hoisted above the imports, but its factory does not run until the module is
// first imported, which is inside a test -- so the bindings exist by then. They
// are `let` rather than `const` because each test drives them.
let storeAvailable = true
let themeKey = 'light-a,dark-a'
let writes: Array<{ ns: string, source: string, value: unknown }> = []
let getArtifactMock: (ns: string, source: string) => Promise<unknown>

// The store and the theme module are both module state, so each test imports a
// fresh copy of the adapter and drives them through mocks.
vi.mock('./renderArtifactStore', () => ({
  RENDER_ARTIFACT_CACHE_VERSION: 1,
  isArtifactStoreAvailable: () => storeAvailable,
  getArtifact: (ns: string, source: string) => getArtifactMock(ns, source),
  putArtifact: (ns: string, source: string, value: unknown) => {
    writes.push({ ns, source, value })
    return Promise.resolve()
  },
}))

vi.mock('./shikiThemes', () => ({
  syntaxThemeKey: () => themeKey,
}))

async function importAdapter() {
  vi.resetModules()
  return await import('./persistedArtifact')
}

function makeArtifact(createPersistedArtifact: typeof import('./persistedArtifact').createPersistedArtifact) {
  return createPersistedArtifact<{ v: string }, string>({
    prefix: 'test',
    maxSourceLength: 1024,
    isValid: (stored): stored is { v: string } =>
      stored !== null && typeof stored === 'object' && typeof (stored as { v?: unknown }).v === 'string',
    decode: stored => stored.v,
  })
}

describe('createPersistedArtifact', () => {
  beforeEach(() => {
    storeAvailable = true
    themeKey = 'light-a,dark-a'
    writes = []
    getArtifactMock = () => Promise.resolve(undefined)
  })

  it('folds the cache version and the theme key into the namespace', async () => {
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)
    expect(artifact.ns()).toBe('test@1|light-a,dark-a')

    themeKey = 'light-b,dark-b'
    expect(artifact.ns()).toBe('test@1|light-b,dark-b')
  })

  it('serves a hit read under the namespace that is still live', async () => {
    getArtifactMock = () => Promise.resolve({ v: 'html' })
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)

    await expect(artifact.read('source')).resolves.toBe('html')
  })

  it('answers a miss when the theme moved while the lookup was in flight', async () => {
    // The bug this pins: the read resolves its namespace when it is CALLED, so
    // the value is the OLD theme's. The round trip resolves later, by which time
    // the user may have chosen another theme -- and every consumer feeds the
    // result to a cache keyed on the source alone, so a hit served here restores
    // exactly what the theme-change invalidator had just cleared.
    let release: (value: unknown) => void = () => {}
    getArtifactMock = () => new Promise((resolve) => {
      release = resolve
    })
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)

    const pending = artifact.read('source')
    themeKey = 'light-b,dark-b'
    release({ v: 'html-from-theme-a' })

    await expect(pending).resolves.toBeUndefined()
  })

  it('answers a miss for a stored value that fails validation', async () => {
    getArtifactMock = () => Promise.resolve({ nope: true })
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)

    await expect(artifact.read('source')).resolves.toBeUndefined()
  })

  it('returns undefined SYNCHRONOUSLY when the store cannot serve', async () => {
    storeAvailable = false
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)

    // Not a promise: the caller keeps its same-frame dispatch timing.
    expect(artifact.read('source')).toBeUndefined()
  })

  it('returns undefined SYNCHRONOUSLY for a source past the length cap', async () => {
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)

    expect(artifact.read('x'.repeat(1025))).toBeUndefined()
  })

  it('writes under the namespace it was GIVEN, not the one live at write time', async () => {
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)
    const dispatchNs = artifact.ns()

    themeKey = 'light-b,dark-b'
    artifact.write(dispatchNs, 'source', { v: 'html' })

    expect(writes).toEqual([{ ns: 'test@1|light-a,dark-a', source: 'source', value: { v: 'html' } }])
  })

  it('drops a write when the store cannot serve, and one past the length cap', async () => {
    const { createPersistedArtifact } = await importAdapter()
    const artifact = makeArtifact(createPersistedArtifact)

    artifact.write(artifact.ns(), 'x'.repeat(1025), { v: 'html' })
    expect(writes).toHaveLength(0)

    storeAvailable = false
    artifact.write(artifact.ns(), 'source', { v: 'html' })
    expect(writes).toHaveLength(0)
  })
})
