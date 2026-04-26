import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { PREFIX_EDITOR_MIN_HEIGHT } from '~/lib/browserStorage'
import {
  clampEditorHeight,
  clearEditorMinHeight,
  EDITOR_MIN_HEIGHT,
  editorMinHeightKey,
  getStoredEditorMinHeight,
  persistEditorMinHeight,
} from './editorMinHeight'

const AGENT = 'agent-xyz'

/**
 * Read the stored value directly out of localStorage. The browserStorage
 * helpers wrap dynamic-key values as `{ v, e }`; this helper unwraps that
 * so tests can assert against the underlying value without coupling to the
 * envelope shape.
 */
function readRawStored(agentId: string): string | null {
  const raw = localStorage.getItem(`${PREFIX_EDITOR_MIN_HEIGHT}${agentId}`)
  if (!raw)
    return null
  try {
    const parsed = JSON.parse(raw)
    if (parsed && typeof parsed === 'object' && 'v' in parsed) {
      return String(parsed.v)
    }
  }
  catch {
    return raw
  }
  return raw
}

beforeEach(() => {
  localStorage.clear()
})
afterEach(() => {
  localStorage.clear()
})

describe('editorMinHeightKey', () => {
  it('builds a per-agent key under the editor-min-height prefix', () => {
    expect(editorMinHeightKey('agent-1')).toBe(`${PREFIX_EDITOR_MIN_HEIGHT}agent-1`)
  })
})

describe('clampEditorHeight', () => {
  it('returns the minimum when raw value is below it (drag past minimum)', () => {
    expect(clampEditorHeight(0, 200)).toBe(EDITOR_MIN_HEIGHT)
    expect(clampEditorHeight(-100, 200)).toBe(EDITOR_MIN_HEIGHT)
    expect(clampEditorHeight(EDITOR_MIN_HEIGHT - 1, 200)).toBe(EDITOR_MIN_HEIGHT)
  })

  it('returns the maximum when raw value is above it (drag past maximum)', () => {
    expect(clampEditorHeight(500, 200)).toBe(200)
    expect(clampEditorHeight(Number.POSITIVE_INFINITY, 540)).toBe(540)
  })

  it('returns the raw value unchanged when within bounds', () => {
    expect(clampEditorHeight(100, 200)).toBe(100)
    expect(clampEditorHeight(EDITOR_MIN_HEIGHT, 200)).toBe(EDITOR_MIN_HEIGHT)
    expect(clampEditorHeight(200, 200)).toBe(200)
  })

  it('clamps to minimum when min would exceed max (degenerate constraints)', () => {
    expect(clampEditorHeight(50, 10)).toBe(EDITOR_MIN_HEIGHT)
  })
})

describe('getStoredEditorMinHeight', () => {
  it('returns undefined when no value is stored', () => {
    expect(getStoredEditorMinHeight(AGENT)).toBeUndefined()
  })

  it('round-trips a value persisted via persistEditorMinHeight', () => {
    persistEditorMinHeight(AGENT, 100)
    expect(getStoredEditorMinHeight(AGENT)).toBe(100)
  })

  it('returns undefined when the stored value is below the minimum (corrupt data)', () => {
    // Simulate a stale write below MIN. The reader rejects it.
    localStorage.setItem(`${PREFIX_EDITOR_MIN_HEIGHT}${AGENT}`, JSON.stringify({ v: '20', e: Date.now() + 1000 * 60 * 60 }))
    expect(getStoredEditorMinHeight(AGENT)).toBeUndefined()
  })
})

describe('persistEditorMinHeight', () => {
  it('persists a value strictly greater than the minimum', () => {
    persistEditorMinHeight(AGENT, EDITOR_MIN_HEIGHT + 1)
    expect(readRawStored(AGENT)).toBe(String(EDITOR_MIN_HEIGHT + 1))
  })

  it('persists larger drag values', () => {
    persistEditorMinHeight(AGENT, 200)
    expect(readRawStored(AGENT)).toBe('200')
  })

  it('removes the key when the value equals the minimum (drag-back-to-min)', () => {
    persistEditorMinHeight(AGENT, 200)
    expect(readRawStored(AGENT)).toBe('200')
    persistEditorMinHeight(AGENT, EDITOR_MIN_HEIGHT)
    expect(readRawStored(AGENT)).toBeNull()
  })

  it('removes the key when the value is below the minimum', () => {
    persistEditorMinHeight(AGENT, 200)
    persistEditorMinHeight(AGENT, 10)
    expect(readRawStored(AGENT)).toBeNull()
  })

  it('removes the key when the value is undefined', () => {
    persistEditorMinHeight(AGENT, 200)
    persistEditorMinHeight(AGENT, undefined)
    expect(readRawStored(AGENT)).toBeNull()
  })

  it('does not write a key when persisting undefined into a clean state', () => {
    persistEditorMinHeight(AGENT, undefined)
    expect(readRawStored(AGENT)).toBeNull()
  })
})

describe('clearEditorMinHeight', () => {
  it('removes any persisted override', () => {
    persistEditorMinHeight(AGENT, 200)
    expect(readRawStored(AGENT)).toBe('200')
    clearEditorMinHeight(AGENT)
    expect(readRawStored(AGENT)).toBeNull()
  })

  it('is a no-op when no override exists', () => {
    expect(() => clearEditorMinHeight(AGENT)).not.toThrow()
    expect(readRawStored(AGENT)).toBeNull()
  })
})

describe('persistence isolation across agents', () => {
  it('per-agent keys do not collide', () => {
    persistEditorMinHeight('agent-a', 100)
    persistEditorMinHeight('agent-b', 200)
    expect(getStoredEditorMinHeight('agent-a')).toBe(100)
    expect(getStoredEditorMinHeight('agent-b')).toBe(200)

    clearEditorMinHeight('agent-a')
    expect(getStoredEditorMinHeight('agent-a')).toBeUndefined()
    expect(getStoredEditorMinHeight('agent-b')).toBe(200)
  })
})
