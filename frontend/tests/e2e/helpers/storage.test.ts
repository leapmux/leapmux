import type { Page } from '@playwright/test'
import { IDBFactory } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { countRows, waitForStoredEntry, writeEntry } from './storage'

// Run the real poll with a short unit-test deadline.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect: actual.expect.configure({ timeout: 500 }) }
})

/** A page whose `evaluate` runs the function here, against the fake IndexedDB of the test. */
const page = { evaluate: async (body: (arg: unknown) => unknown, arg: unknown) => body(arg) } as unknown as Page

/** An expiry that no test reaches. */
const NEVER = Number.MAX_SAFE_INTEGER

beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
})
afterEach(() => {
  vi.unstubAllGlobals()
})

/** Create the database `name` at a version of its own, with the object store `store` that holds `rows` rows. */
function createDatabase(name: string, store: string, rows: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(name, 10)
    request.onerror = () => reject(request.error)
    request.onupgradeneeded = () => {
      request.result.createObjectStore(store, { autoIncrement: true })
    }
    request.onsuccess = () => {
      const db = request.result
      const transaction = db.transaction(store, 'readwrite')
      for (let index = 0; index < rows; index++)
        transaction.objectStore(store).add({ owner: `client-${index}` })
      transaction.onerror = () => reject(transaction.error)
      transaction.oncomplete = () => {
        db.close()
        resolve()
      }
    }
  })
}

/** The names of the databases that exist, sorted. */
async function databaseNames(): Promise<string[]> {
  return (await indexedDB.databases()).map(database => database.name ?? '').sort()
}

describe('countRows', () => {
  it('counts the rows of an existing store', async () => {
    await createDatabase('leapmux-crdt-state', 'checkpoints', 2)
    await expect(countRows(page, 'leapmux-crdt-state', 'checkpoints')).resolves.toBe(2)
  })

  it('counts an empty store as zero, not as absent', async () => {
    await createDatabase('leapmux-crdt-state', 'checkpoints', 0)
    await expect(countRows(page, 'leapmux-crdt-state', 'checkpoints')).resolves.toBe(0)
  })

  it('reports an absent database as null and removes the database that its own open created', async () => {
    await expect(countRows(page, 'leapmux-crdt-state', 'checkpoints')).resolves.toBeNull()
    await vi.waitFor(async () => expect(await databaseNames()).toEqual([]))
  })

  it('reports an absent store as null and keeps the database', async () => {
    await createDatabase('leapmux-crdt-state', 'other', 1)
    await expect(countRows(page, 'leapmux-crdt-state', 'checkpoints')).resolves.toBeNull()
    expect(await databaseNames()).toEqual(['leapmux-crdt-state'])
  })
})

describe('waitForStoredEntry', () => {
  const prefix = 'leapmux:u:user-1:editor-draft:'
  const hasHello = (value: unknown) => typeof value === 'object' && value !== null && Reflect.get(value, 'content') === 'hello'

  it('accepts a row under the prefix whose value passes, and never reads a row outside the prefix', async () => {
    await writeEntry(page, 'leapmux:u:user-2:editor-draft:agent-a', { content: 'hello' }, NEVER)
    const accept = vi.fn(hasHello)
    await expect(waitForStoredEntry(page, prefix, accept, 'the draft of user-1 is stored')).rejects.toThrow('the draft of user-1 is stored')
    expect(accept).not.toHaveBeenCalled()

    await writeEntry(page, `${prefix}agent-a`, { content: 'hello' }, NEVER)
    await expect(waitForStoredEntry(page, prefix, accept, 'the draft of user-1 is stored')).resolves.toBeUndefined()
    expect(accept).toHaveBeenCalledWith({ content: 'hello' })
  })

  it('fails with the message while every row under the prefix holds a value that does not pass', async () => {
    await writeEntry(page, `${prefix}agent-a`, { content: 'other text' }, NEVER)
    await writeEntry(page, `${prefix}agent-b`, 'not an object', NEVER)
    await expect(waitForStoredEntry(page, prefix, hasHello, 'the draft is stored')).rejects.toThrow('the draft is stored')
  })

  it('waits for a row that lands after the poll starts', async () => {
    // The first write creates the store. A later write needs it, because a versionless reader creates no store.
    await writeEntry(page, 'leapmux:device:theme', 'dark', NEVER)
    const waiting = waitForStoredEntry(page, prefix, hasHello, 'the late draft is stored')
    await writeEntry(page, `${prefix}agent-late`, { content: 'hello' }, NEVER)
    await expect(waiting).resolves.toBeUndefined()
  })

  it('refuses an empty prefix, which every key matches', async () => {
    await expect(waitForStoredEntry(page, '', hasHello, 'unused')).rejects.toThrow('nonempty key prefix')
  })
})
