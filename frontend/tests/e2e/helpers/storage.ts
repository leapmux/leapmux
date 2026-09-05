import type { Page } from '@playwright/test'

/**
 * The browser-storage database, as an E2E spec reaches it.
 *
 * The `leapmux:` key family lives in IndexedDB (see `~/lib/browserStorage`), so
 * a spec that seeds or asserts one talks to a database rather than to
 * localStorage. These three functions are the ONLY `page.evaluate` bodies in
 * the suite that name that layout, so a change to it has one place to land.
 *
 * Every helper opens with NO VERSION. A versionless open attaches to whatever
 * version exists, so the harness can never trigger the app's own schema repair
 * -- and it never has to be kept in step with what Dexie stores by hand. On a
 * database the app has not created yet it creates an empty one, which reads as
 * "nothing stored" and is the right answer for a spec that has not signed in.
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
        resolve(row === undefined ? null : { v: row.v, e: row.e })
      }
    }
  }), [DB_NAME, TABLE, storedKey] as const)
}

/**
 * Replace the row at `storedKey`.
 *
 * The store must already exist, which it does once the app has opened the
 * database. A spec that seeds before the first load navigates, seeds, and
 * reloads -- IndexedDB is durable across a navigation, so no init script is
 * needed to make the value survive one.
 */
export async function writeEntry(page: Page, storedKey: string, value: unknown, expiresAt: number): Promise<void> {
  await page.evaluate(([dbName, table, key, v, e]) => new Promise<void>((resolve, reject) => {
    const request = indexedDB.open(dbName)
    request.onerror = () => reject(new Error(`could not open ${dbName}`))
    request.onupgradeneeded = () => {
      // Only reached when the app has never opened the database. Build the
      // minimum the app's own declaration builds, so its schema check finds a
      // matching shape rather than deleting the seed.
      request.result.createObjectStore(table, { keyPath: 'k' }).createIndex('e', 'e')
    }
    request.onsuccess = () => {
      const db = request.result
      const tx = db.transaction(table, 'readwrite')
      tx.objectStore(table).put({ k: key, v, e })
      tx.oncomplete = () => {
        db.close()
        resolve()
      }
      tx.onerror = () => {
        db.close()
        reject(new Error(`could not write ${key}`))
      }
    }
  }), [DB_NAME, TABLE, storedKey, value, expiresAt] as const)
}

/** Every stored key, sorted. For a spec asserting which keys exist at all. */
export async function storageKeys(page: Page): Promise<string[]> {
  return page.evaluate(([dbName, table]) => new Promise<string[]>((resolve) => {
    const request = indexedDB.open(dbName)
    request.onerror = () => resolve([])
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

/** Delete the whole database, so a spec can prove nothing in it holds a session. */
export async function deleteStorageDatabase(page: Page): Promise<void> {
  await page.evaluate(dbName => new Promise<void>((resolve) => {
    const request = indexedDB.deleteDatabase(dbName)
    request.onsuccess = () => resolve()
    request.onerror = () => resolve()
    request.onblocked = () => resolve()
  }), DB_NAME)
}
