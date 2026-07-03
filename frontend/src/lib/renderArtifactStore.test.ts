import { IDBFactory, IDBObjectStore } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  _resetArtifactStoreForTest,
  ARTIFACT_TTL_MS,
  getArtifact,
  isArtifactStoreAvailable,
  putArtifact,
  sweepArtifacts,
} from './renderArtifactStore'

// Mirrors the store's internal schema — used only to tamper with records the
// way a digest collision or on-disk corruption would.
const DB_NAME = 'leapmux-render-cache'
const STORE_NAME = 'artifacts'

async function tamperAllSources(newSource: string): Promise<void> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open(DB_NAME)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  await new Promise<void>((resolve, reject) => {
    const store = db.transaction(STORE_NAME, 'readwrite').objectStore(STORE_NAME)
    const cursorReq = store.openCursor()
    cursorReq.onsuccess = () => {
      const cursor = cursorReq.result
      if (!cursor) {
        resolve()
        return
      }
      cursor.update({ ...(cursor.value as Record<string, unknown>), source: newSource })
      cursor.continue()
    }
    cursorReq.onerror = () => reject(cursorReq.error)
  })
  db.close()
}

describe('renderartifactstore', () => {
  beforeEach(() => {
    // A FRESH IndexedDB universe per test; the store must re-open against it.
    vi.stubGlobal('indexedDB', new IDBFactory())
    _resetArtifactStoreForTest()
  })

  afterEach(() => {
    _resetArtifactStoreForTest()
    vi.unstubAllGlobals()
  })

  it('round-trips an artifact and misses on unknown sources', async () => {
    await putArtifact('ns', 'source-text', '<p>html</p>')
    await expect(getArtifact('ns', 'source-text')).resolves.toBe('<p>html</p>')
    await expect(getArtifact('ns', 'other-text')).resolves.toBeUndefined()
  })

  it('separates namespaces for the same source', async () => {
    await putArtifact('md@1', 'shared', 'markdown-value')
    await putArtifact('tok@1', 'shared', { styles: [], lines: [] })
    await expect(getArtifact('md@1', 'shared')).resolves.toBe('markdown-value')
    await expect(getArtifact('tok@1', 'shared')).resolves.toEqual({ styles: [], lines: [] })
    await expect(getArtifact('md@2', 'shared')).resolves.toBeUndefined() // fingerprint bump orphans
  })

  it('refuses a record whose stored source does not match (digest collision / corruption)', async () => {
    await putArtifact('ns', 'the-real-source', 'value')
    await tamperAllSources('some-other-source')
    await expect(getArtifact('ns', 'the-real-source')).resolves.toBeUndefined()
  })

  it('sweeps entries past the TTL and keeps fresh ones', async () => {
    const t0 = 1_000_000
    await putArtifact('ns', 'old', 'old-value', t0)
    await putArtifact('ns', 'fresh', 'fresh-value', t0 + ARTIFACT_TTL_MS)
    const deleted = await sweepArtifacts({ now: t0 + ARTIFACT_TTL_MS })
    expect(deleted).toBe(1)
    await expect(getArtifact('ns', 'old')).resolves.toBeUndefined()
    await expect(getArtifact('ns', 'fresh')).resolves.toBe('fresh-value')
  })

  it('sweeps the oldest-used entries past the cap', async () => {
    for (let i = 0; i < 5; i++)
      await putArtifact('ns', `s-${i}`, i, 1000 + i)
    const deleted = await sweepArtifacts({ maxEntries: 3, now: 2000 })
    expect(deleted).toBe(2)
    await expect(getArtifact('ns', 's-0')).resolves.toBeUndefined()
    await expect(getArtifact('ns', 's-1')).resolves.toBeUndefined()
    await expect(getArtifact('ns', 's-2')).resolves.toBe(2)
    await expect(getArtifact('ns', 's-4')).resolves.toBe(4)
  })

  it('a read refreshes recency, so a hot entry outlives the cap sweep', async () => {
    await putArtifact('ns', 'first', 'a', 1000)
    await putArtifact('ns', 'second', 'b', 2000)
    // Touch 'first' AFTER 'second' was written: it becomes the most recent.
    // touchIntervalMs 0 forces the touch regardless of how recent the stamp is.
    await expect(getArtifact('ns', 'first', 3000, 0)).resolves.toBe('a')
    const deleted = await sweepArtifacts({ maxEntries: 1, now: 3000 })
    expect(deleted).toBe(1)
    await expect(getArtifact('ns', 'first')).resolves.toBe('a')
    await expect(getArtifact('ns', 'second')).resolves.toBeUndefined()
  })

  it('skips the recency touch-write while the stored stamp is still fresh', async () => {
    await putArtifact('ns', 's', 'v', 1000)
    const putSpy = vi.spyOn(IDBObjectStore.prototype, 'put')
    try {
      // Read within touchIntervalMs of the stamp: the payload must NOT be
      // re-serialized/re-written just to bump `at` by a hair.
      await expect(getArtifact('ns', 's', 1000 + 60_000, 3_600_000)).resolves.toBe('v')
      expect(putSpy).not.toHaveBeenCalled()
      // Read past the interval: the stamp is refreshed with one write.
      await expect(getArtifact('ns', 's', 1000 + 4_000_000, 3_600_000)).resolves.toBe('v')
      expect(putSpy).toHaveBeenCalledTimes(1)
    }
    finally {
      putSpy.mockRestore()
    }
  })

  it('still serves a valid hit when the recency-touch write fails', async () => {
    await putArtifact('ns', 'source-text', '<p>html</p>', 1000)
    // A failing recency refresh (quota / blocked write) must NOT turn a valid
    // read into a miss: the artifact was already read before the touch runs.
    // touchIntervalMs 0 forces the touch so the failing write is exercised.
    const putSpy = vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementation(() => {
      throw new Error('QuotaExceededError')
    })
    try {
      await expect(getArtifact('ns', 'source-text', 5000, 0)).resolves.toBe('<p>html</p>')
    }
    finally {
      putSpy.mockRestore()
    }
    // The store is intact and still serves the value after the failed touch.
    await expect(getArtifact('ns', 'source-text')).resolves.toBe('<p>html</p>')
  })

  it('degrades to no-ops without indexedDB', async () => {
    vi.stubGlobal('indexedDB', undefined)
    _resetArtifactStoreForTest()
    expect(isArtifactStoreAvailable()).toBe(false)
    await expect(putArtifact('ns', 's', 'v')).resolves.toBeUndefined()
    await expect(getArtifact('ns', 's')).resolves.toBeUndefined()
    await expect(sweepArtifacts()).resolves.toBe(0)
  })
})
