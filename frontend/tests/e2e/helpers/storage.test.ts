import type { Page } from '@playwright/test'
import { IDBFactory } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { accountStorageKey, KEY_KEY_PINS, readKeyPinState, resetBrowserStorageForTests, storageAccountFor, TEST_HUB } from '~/lib/browserStorage'
import { IDB_NATIVE_SCHEMA_VERSION, IDB_TRUST_DATABASE_NAME } from '~/lib/idb'
import { testKeyPinHex } from '~/test-support/keyPins'
import { countRows, databaseStores, deleteEntries, deleteStorageDatabase, readEntry, storageKeys, waitForStoredEntry, writeEntry } from './storage'

// Run the real poll with a short unit-test deadline.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: actual.expect.configure({ timeout: 500 }),
  }
})

/** A page whose `evaluate` runs the function here, against the fake IndexedDB of the test. */
const page = {
  evaluate: async (body: (arg: unknown) => unknown, arg: unknown) => body(arg),
} as unknown as Page

/** An expiry that no test reaches. */
const NEVER = Number.MAX_SAFE_INTEGER

beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
})
afterEach(() => {
  resetBrowserStorageForTests()
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

/** Read one physical database without using the helper's key router. */
function rawEntry(dbName: string, key: string): Promise<{ v: unknown, e: number } | null> {
  return new Promise((resolve) => {
    const request = indexedDB.open(dbName)
    request.onerror = () => resolve(null)
    request.onupgradeneeded = () => request.transaction?.abort()
    request.onsuccess = () => {
      const db = request.result
      if (!db.objectStoreNames.contains('entries')) {
        db.close()
        resolve(null)
        return
      }
      const get = db.transaction('entries', 'readonly').objectStore('entries').get(key)
      get.onerror = () => {
        db.close()
        resolve(null)
      }
      get.onsuccess = () => {
        const row = get.result as { v: unknown, e: number } | undefined
        db.close()
        resolve(row ?? null)
      }
    }
  })
}

describe('pin storage fixture', () => {
  const accountId = storageAccountFor(TEST_HUB, 'user-1')
  const key = accountStorageKey(accountId, KEY_KEY_PINS)

  it('reads confirmed absence without leaving a database that blocks the first pin open', async () => {
    await expect(readEntry(page, key)).resolves.toBeNull()
    await expect(readKeyPinState({ accountId })).resolves.toEqual({ pins: {}, removalRevision: 0 })
  })

  it('lists no keys without leaving a database that blocks the first pin open', async () => {
    await expect(storageKeys(page)).resolves.toEqual([])
    await expect(readKeyPinState({ accountId })).resolves.toEqual({ pins: {}, removalRevision: 0 })
  })

  it('inspects an absent database without creating it or relying on a later delete', async () => {
    const deleteDatabase = vi.spyOn(indexedDB, 'deleteDatabase')
    try {
      await expect(databaseStores(page, 'leapmux-kv')).resolves.toBeNull()
      expect(deleteDatabase).not.toHaveBeenCalled()
    }
    finally {
      deleteDatabase.mockRestore()
    }
    await expect(readKeyPinState({ accountId })).resolves.toEqual({ pins: {}, removalRevision: 0 })
  })

  it('refuses cleanup of an absent database without blocking the first pin open', async () => {
    await expect(deleteEntries(page, [key])).rejects.toThrow(`the database ${IDB_TRUST_DATABASE_NAME} has no object store entries`)
    await expect(readKeyPinState({ accountId })).resolves.toEqual({ pins: {}, removalRevision: 0 })
  })

  it('seeds an unopened database with a record the strict pin gateway can read', async () => {
    const pins = {
      worker: {
        publicKeyHex: testKeyPinHex('aa'),
        firstSeen: 1,
      },
    }
    await writeEntry(page, key, pins, NEVER)
    await expect(readKeyPinState({ accountId })).resolves.toEqual({ pins, removalRevision: 0 })
  })

  it('rejects and closes the database when the declared store has the wrong key path', async () => {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open(IDB_TRUST_DATABASE_NAME, IDB_NATIVE_SCHEMA_VERSION)
      request.onerror = () => reject(request.error)
      request.onupgradeneeded = () => {
        request.result.createObjectStore('entries', { keyPath: 'wrong' }).createIndex('e', 'e')
      }
      request.onsuccess = () => {
        request.result.close()
        resolve()
      }
    })

    await expect(writeEntry(page, key, {}, NEVER)).rejects.toMatchObject({ name: 'DataError' })
    await expect(new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(IDB_TRUST_DATABASE_NAME)
      request.onerror = () => reject(request.error)
      request.onblocked = () => reject(new Error('the failed write left the database open'))
      request.onsuccess = () => resolve()
    })).resolves.toBeUndefined()
  })
})

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

describe('deleteEntries', () => {
  const draft = 'leapmux:u:user-1:editor-draft:agent-a'
  const heights = 'leapmux:u:user-1:chat-row-heights:agent-a'
  const theme = 'leapmux:device:theme'

  /** Store one row at each of the three keys above. */
  async function storeRows(): Promise<void> {
    for (const key of [draft, heights, theme])
      await writeEntry(page, key, 'value', NEVER)
  }

  it('deletes the listed keys, keeps the others, and resolves the number of deleted rows', async () => {
    await storeRows()
    await expect(deleteEntries(page, [heights, theme])).resolves.toBe(2)
    expect(await storageKeys(page)).toEqual([draft])
  })

  it('deletes preference and trust rows from their owning databases', async () => {
    const pin = accountStorageKey('user-1', KEY_KEY_PINS)
    await writeEntry(page, draft, 'draft', NEVER)
    await writeEntry(page, pin, {}, NEVER)

    await expect(deleteEntries(page, [draft, pin])).resolves.toBe(2)
    await expect(readEntry(page, draft)).resolves.toBeNull()
    await expect(readEntry(page, pin)).resolves.toBeNull()
  })

  it('keeps a variable preference suffix that resembles a pin in preferences', async () => {
    const lookalike = accountStorageKey('user-1', 'files-show-hidden:w1:/repo/:key-pins')
    await writeEntry(page, lookalike, true, NEVER)

    const preference = await rawEntry('leapmux-kv', lookalike)
    const trust = await rawEntry(IDB_TRUST_DATABASE_NAME, lookalike)
    expect(preference).toMatchObject({ v: true })
    expect(trust).toBeNull()
  })

  it('does not count a listed key that holds no row, nor a key that the list repeats', async () => {
    await storeRows()
    await expect(deleteEntries(page, [heights, 'leapmux:u:user-1:chat-row-heights:absent', heights])).resolves.toBe(1)
    expect(await storageKeys(page)).toEqual([draft, theme].sort())
  })

  it('resolves 0 for an empty list and deletes nothing', async () => {
    await storeRows()
    await expect(deleteEntries(page, [])).resolves.toBe(0)
    expect(await storageKeys(page)).toEqual([draft, heights, theme].sort())
  })

  it('rejects for a database that holds another store, also for an empty list', async () => {
    await createDatabase('leapmux-kv', 'other', 1)
    await expect(deleteEntries(page, ['leapmux:device:theme'])).rejects.toThrow('the database leapmux-kv has no object store entries')
    await expect(deleteEntries(page, [])).rejects.toThrow('the database leapmux-kv has no object store entries')
  })

  it('rejects when the database does not exist', async () => {
    await expect(deleteEntries(page, ['leapmux:device:theme'])).rejects.toThrow('the database leapmux-kv has no object store entries')
  })
})

describe('deleteStorageDatabase', () => {
  it('deletes both preference and trust databases', async () => {
    await writeEntry(page, 'leapmux:device:theme', 'nord', NEVER)
    await writeEntry(page, accountStorageKey('user-1', KEY_KEY_PINS), {}, NEVER)

    await deleteStorageDatabase(page)

    expect(await databaseNames()).toEqual([])
  })

  it('rejects when an open trust connection blocks deletion', async () => {
    await writeEntry(page, accountStorageKey('user-1', KEY_KEY_PINS), {}, NEVER)
    const request = indexedDB.open(IDB_TRUST_DATABASE_NAME)
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      request.onerror = () => reject(request.error)
      request.onsuccess = () => resolve(request.result)
    })
    try {
      await expect(deleteStorageDatabase(page)).rejects.toThrow(`deleting ${IDB_TRUST_DATABASE_NAME} is blocked`)
    }
    finally {
      db.close()
    }
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
    const waiting = waitForStoredEntry(page, prefix, hasHello, 'the late draft is stored')
    await writeEntry(page, `${prefix}agent-late`, { content: 'hello' }, NEVER)
    await expect(waiting).resolves.toBeUndefined()
  })

  it('refuses an empty prefix, which every key matches', async () => {
    await expect(waitForStoredEntry(page, '', hasHello, 'unused')).rejects.toThrow('nonempty key prefix')
  })
})
