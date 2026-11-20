import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { IDB_NATIVE_SCHEMA_VERSION } from '../../../src/lib/idb'

/**
 * The browser-storage databases, as an E2E spec reaches them.
 *
 * The `leapmux:` key family lives in IndexedDB (see `~/lib/browserStorage`), so
 * a spec that seeds or asserts one talks to a database rather than to
 * localStorage. The functions here hold the ONLY `page.evaluate` bodies in the
 * suite that open the database of that family, so a change to its layout has
 * one place to land. Another database of the app, the render cache, is not
 * here: `clearStoredRenders` (chatScrollStability.ts) empties it.
 *
 * Key-value reads open the current version and abort creation if the database
 * is absent. A seed can initialize the database at the app's declared native
 * version. `databaseStores` and `countRows` probe other databases as well.
 *
 * The composed key comes from the app's own `accountStorageKey`, never spelled
 * out here: a literal is what went stale the moment the layout changed.
 */

const DB_NAME = 'leapmux-kv'
const TABLE = 'entries'

/** One stored row, as the gateway writes it. */
export interface StoredEntry {
  v: unknown
  /** Expiration, in epoch milliseconds. */
  e: number
}

/** The row at `storedKey`, or null. */
export async function readEntry(page: Page, storedKey: string): Promise<StoredEntry | null> {
  return page.evaluate(([dbName, table, key]) => new Promise<StoredEntry | null>((resolve) => {
    const request = indexedDB.open(dbName)
    request.onerror = () => resolve(null)
    request.onupgradeneeded = () => request.transaction?.abort()
    request.onsuccess = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(table)) {
        db.close()
        resolve(null)
        return
      }
      const get = db.transaction(table, 'readonly').objectStore(table).get(key)
      get.onerror = () => {
        db.close()
        resolve(null)
      }
      get.onsuccess = () => {
        const row = get.result as { v: unknown, e: number } | undefined
        db.close()
        resolve(row === undefined
          ? null
          : { v: row.v, e: row.e })
      }
    }
  }), [DB_NAME, TABLE, storedKey] as const)
}

/**
 * Replace the row at `storedKey`.
 *
 * Create the declared store when seeding an unopened database. The helper
 * refuses a version change to an existing database.
 */
export async function writeEntry(page: Page, storedKey: string, value: unknown, expiresAt: number): Promise<void> {
  await page.evaluate(([dbName, version, table, key, v, e]) => new Promise<void>((resolve, reject) => {
    const request = indexedDB.open(dbName, version)
    request.onerror = () => reject(request.error ?? new Error(`Could not open ${dbName}.`))
    request.onblocked = () => reject(new Error(`The open of ${dbName} is blocked.`))
    request.onupgradeneeded = (event) => {
      if (event.oldVersion !== 0) {
        request.transaction?.abort()
        return
      }
      request.result.createObjectStore(table, { keyPath: 'k' }).createIndex('e', 'e')
    }
    request.onsuccess = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(table)) {
        db.close()
        reject(new Error(`The database ${dbName} has no object store ${table}.`))
        return
      }
      const tx = db.transaction(table, 'readwrite')
      tx.objectStore(table).put({ k: key, v, e })
      tx.oncomplete = () => {
        db.close()
        resolve()
      }
      tx.onabort = () => {
        db.close()
        reject(tx.error ?? new Error(`Could not write ${key}.`))
      }
    }
  }), [
    DB_NAME,
    IDB_NATIVE_SCHEMA_VERSION,
    TABLE,
    storedKey,
    value,
    expiresAt,
  ] as const)
}

/** Every stored key, sorted. For a spec asserting which keys exist at all. */
export async function storageKeys(page: Page): Promise<string[]> {
  return page.evaluate(([dbName, table]) => new Promise<string[]>((resolve) => {
    const request = indexedDB.open(dbName)
    request.onerror = () => resolve([])
    request.onupgradeneeded = () => request.transaction?.abort()
    request.onsuccess = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(table)) {
        db.close()
        resolve([])
        return
      }
      const keys = db.transaction(table, 'readonly').objectStore(table).getAllKeys()
      keys.onerror = () => {
        db.close()
        resolve([])
      }
      keys.onsuccess = () => {
        db.close()
        resolve((keys.result as string[]).slice().sort())
      }
    }
  }), [DB_NAME, TABLE] as const)
}

/**
 * Delete the row at each key of `storedKeys` in one transaction, and resolve the number of rows that it deleted. A
 * key that holds no row deletes nothing and does not count.
 *
 * Rejects when the database or its `entries` store is absent. In that case a
 * zero count would hide that the caller never reached the store it meant to clean.
 */
export async function deleteEntries(page: Page, storedKeys: readonly string[]): Promise<number> {
  return page.evaluate(([dbName, table, keys]) => new Promise<number>((resolve, reject) => {
    const request = indexedDB.open(dbName)
    let missingDatabase = false
    const missingStore = () => new Error(`the database ${dbName} has no object store ${table}`)
    request.onerror = () => reject(missingDatabase
      ? missingStore()
      : new Error(`could not open ${dbName}`))
    request.onupgradeneeded = () => {
      missingDatabase = true
      request.transaction?.abort()
    }
    request.onsuccess = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(table)) {
        db.close()
        reject(missingStore())
        return
      }
      let deleted = 0
      const tx = db.transaction(table, 'readwrite')
      const entries = tx.objectStore(table)
      for (const key of keys) {
        // The requests of one transaction run in order, so the count reads the row before the delete removes it.
        const count = entries.count(key)
        count.onsuccess = () => {
          deleted += count.result
        }
        entries.delete(key)
      }
      tx.oncomplete = () => {
        db.close()
        resolve(deleted)
      }
      // A failed request aborts the transaction. So this handler covers a failed request, and also an abort that no
      // request caused.
      tx.onabort = () => {
        db.close()
        reject(new Error(`could not delete from ${table}`))
      }
    }
  }), [DB_NAME, TABLE, storedKeys] as const)
}

/** Delete the whole database, so a spec can prove nothing in it holds a session. */
export async function deleteStorageDatabase(page: Page): Promise<void> {
  await page.evaluate(dbName => new Promise<void>((resolve) => {
    const request = indexedDB.deleteDatabase(dbName)
    request.onsuccess = () => resolve()
    request.onerror = () => resolve()
    request.onblocked = () => resolve()
  }), DB_NAME)
}

/**
 * The object stores `dbName` holds, sorted, or null when the database does not
 * exist.
 *
 * Open without a version and abort creation when the database is absent.
 */
export async function databaseStores(page: Page, dbName: string): Promise<string[] | null> {
  return page.evaluate(name => new Promise<string[] | null>((resolve) => {
    const request = indexedDB.open(name)
    request.onerror = () => resolve(null)
    request.onupgradeneeded = () => request.transaction?.abort()
    request.onsuccess = () => {
      const db = request.result
      const stores = Array.from(db.objectStoreNames).sort()
      db.close()
      resolve(stores)
    }
  }), dbName)
}

/**
 * The number of rows in the object store `store` of `dbName`, or null when the database or the store does not exist,
 * or when the read fails.
 *
 * Versionless like `databaseStores`, and it also undoes its own creation of an absent database. A spec polls it,
 * because the app writes these rows off the interaction path.
 */
export async function countRows(page: Page, dbName: string, store: string): Promise<number | null> {
  return page.evaluate(([name, storeName]) => new Promise<number | null>((resolve) => {
    const request = indexedDB.open(name)
    let created = false
    request.onerror = () => resolve(null)
    request.onupgradeneeded = () => {
      created = true
    }
    request.onsuccess = () => {
      const db = request.result
      if (created) {
        db.close()
        // Undo the probe's own creation, so the app still finds no database.
        indexedDB.deleteDatabase(name)
        resolve(null)
        return
      }
      if (!db.objectStoreNames.contains(storeName)) {
        db.close()
        resolve(null)
        return
      }
      const count = db.transaction(storeName, 'readonly').objectStore(storeName).count()
      count.onerror = () => {
        db.close()
        resolve(null)
      }
      count.onsuccess = () => {
        db.close()
        resolve(count.result)
      }
    }
  }), [dbName, store] as const)
}

/**
 * Wait until a stored row whose key starts with `prefix` holds a value that `accept` takes.
 *
 * The app writes a row through a debounce and a write queue, so the row can land after the interaction returns. A
 * fixed sleep cannot account for a busy host, so poll the rows. Build `prefix` with `accountStorageKey` and match
 * with `startsWith`: a regular expression would read the metacharacters of a key.
 */
export async function waitForStoredEntry(
  page: Page,
  prefix: string,
  accept: (value: unknown) => boolean,
  message: string,
): Promise<void> {
  if (prefix === '')
    throw new Error('waitForStoredEntry requires a nonempty key prefix, because every stored key matches an empty one.')
  await expect.poll(async () => {
    for (const key of await storageKeys(page)) {
      if (!key.startsWith(prefix))
        continue
      const row = await readEntry(page, key)
      if (row !== null && accept(row.v))
        return true
    }
    return false
  }, message).toBe(true)
}

/** Every key in one Web Storage area, sorted. */
export async function webStorageKeys(page: Page, area: 'local' | 'session'): Promise<string[]> {
  return page.evaluate((which) => {
    const store = which === 'local'
      ? localStorage
      : sessionStorage
    const keys: string[] = []
    for (let i = 0; i < store.length; i++) {
      const key = store.key(i)
      if (key !== null)
        keys.push(key)
    }
    return keys.sort()
  }, area)
}
