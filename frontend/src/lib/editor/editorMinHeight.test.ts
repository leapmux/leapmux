import { describe, expect, it } from 'vitest'
import { localStorageLoad, localStorageStore, PREFIX_EDITOR_MIN_HEIGHT } from '~/lib/browserStorage'
import { useTestStorage } from '~/test-support/persistentStorage'
import {
  clampEditorHeight,
  clearEditorMinHeight,
  EDITOR_MIN_HEIGHT,
  editorMinHeightKey,
  getStoredEditorMinHeight,
  persistEditorMinHeight,
} from './editorMinHeight'

// The per-agent minimum height is on the ASYNCHRONOUS storage tier, so these
// round-trips need a database to round-trip through.
useTestStorage()

const AGENT = 'agent-xyz'

/**
 * The stored value, as text, or null when nothing is stored.
 *
 * Through the gateway rather than by poking the store: the value lives in
 * IndexedDB under a composed key, and restating that layout here is what goes
 * stale the moment it changes.
 */
async function readStored(agentId: string): Promise<string | null> {
  const stored = await localStorageLoad<number>(`${PREFIX_EDITOR_MIN_HEIGHT}${agentId}`)
  return stored === undefined ? null : String(stored)
}

describe('editorMinHeightKey', () => {
  it('builds a per-agent key under the editor-min-height prefix', async () => {
    expect(editorMinHeightKey('agent-1')).toBe(`${PREFIX_EDITOR_MIN_HEIGHT}agent-1`)
  })
})

describe('clampEditorHeight', () => {
  it('returns the minimum when raw value is below it (drag past minimum)', async () => {
    expect(clampEditorHeight(0, 200)).toBe(EDITOR_MIN_HEIGHT)
    expect(clampEditorHeight(-100, 200)).toBe(EDITOR_MIN_HEIGHT)
    expect(clampEditorHeight(EDITOR_MIN_HEIGHT - 1, 200)).toBe(EDITOR_MIN_HEIGHT)
  })

  it('returns the maximum when raw value is above it (drag past maximum)', async () => {
    expect(clampEditorHeight(500, 200)).toBe(200)
    expect(clampEditorHeight(Number.POSITIVE_INFINITY, 540)).toBe(540)
  })

  it('returns the raw value unchanged when within bounds', async () => {
    expect(clampEditorHeight(100, 200)).toBe(100)
    expect(clampEditorHeight(EDITOR_MIN_HEIGHT, 200)).toBe(EDITOR_MIN_HEIGHT)
    expect(clampEditorHeight(200, 200)).toBe(200)
  })

  it('clamps to minimum when min would exceed max (degenerate constraints)', async () => {
    expect(clampEditorHeight(50, 10)).toBe(EDITOR_MIN_HEIGHT)
  })
})

describe('getStoredEditorMinHeight', () => {
  it('returns undefined when no value is stored', async () => {
    expect(await getStoredEditorMinHeight(AGENT)).toBeUndefined()
  })

  it('round-trips a value persisted via persistEditorMinHeight', async () => {
    persistEditorMinHeight(AGENT, 100)
    expect(await getStoredEditorMinHeight(AGENT)).toBe(100)
  })

  it('returns undefined when the stored value is below the minimum (corrupt data)', async () => {
    // A stale write below MIN, stored through the gateway so it carries the
    // real key and expiry. The reader rejects it.
    localStorageStore(`${PREFIX_EDITOR_MIN_HEIGHT}${AGENT}`, 20)
    expect(await getStoredEditorMinHeight(AGENT)).toBeUndefined()
  })
})

describe('persistEditorMinHeight', () => {
  it('persists a value strictly greater than the minimum', async () => {
    persistEditorMinHeight(AGENT, EDITOR_MIN_HEIGHT + 1)
    expect(await readStored(AGENT)).toBe(String(EDITOR_MIN_HEIGHT + 1))
  })

  it('persists larger drag values', async () => {
    persistEditorMinHeight(AGENT, 200)
    expect(await readStored(AGENT)).toBe('200')
  })

  it('removes the key when the value equals the minimum (drag-back-to-min)', async () => {
    persistEditorMinHeight(AGENT, 200)
    expect(await readStored(AGENT)).toBe('200')
    persistEditorMinHeight(AGENT, EDITOR_MIN_HEIGHT)
    expect(await readStored(AGENT)).toBeNull()
  })

  it('removes the key when the value is below the minimum', async () => {
    persistEditorMinHeight(AGENT, 200)
    persistEditorMinHeight(AGENT, 10)
    expect(await readStored(AGENT)).toBeNull()
  })

  it('removes the key when the value is undefined', async () => {
    persistEditorMinHeight(AGENT, 200)
    persistEditorMinHeight(AGENT, undefined)
    expect(await readStored(AGENT)).toBeNull()
  })

  it('does not write a key when persisting undefined into a clean state', async () => {
    persistEditorMinHeight(AGENT, undefined)
    expect(await readStored(AGENT)).toBeNull()
  })
})

describe('clearEditorMinHeight', () => {
  it('removes any persisted override', async () => {
    persistEditorMinHeight(AGENT, 200)
    expect(await readStored(AGENT)).toBe('200')
    clearEditorMinHeight(AGENT)
    expect(await readStored(AGENT)).toBeNull()
  })

  it('is a no-op when no override exists', async () => {
    expect(() => clearEditorMinHeight(AGENT)).not.toThrow()
    expect(await readStored(AGENT)).toBeNull()
  })
})

describe('persistence isolation across agents', () => {
  it('per-agent keys do not collide', async () => {
    persistEditorMinHeight('agent-a', 100)
    persistEditorMinHeight('agent-b', 200)
    expect(await getStoredEditorMinHeight('agent-a')).toBe(100)
    expect(await getStoredEditorMinHeight('agent-b')).toBe(200)

    clearEditorMinHeight('agent-a')
    expect(await getStoredEditorMinHeight('agent-a')).toBeUndefined()
    expect(await getStoredEditorMinHeight('agent-b')).toBe(200)
  })
})
