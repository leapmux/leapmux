import { IDBFactory } from 'fake-indexeddb'
import { afterEach, beforeEach, vi } from 'vitest'
import { resetBrowserStorageForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { TEST_USER_ID } from './crdtBridge'

/**
 * Give this test file a working IndexedDB, so the ASYNCHRONOUS storage tier can
 * round-trip.
 *
 * `vitest.setup.ts` deliberately installs no `indexedDB` global. The
 * SYNCHRONOUS tier needs none -- it answers from an in-memory mirror, so a
 * write and a read back in the same test never touch a database -- and a global
 * factory would hang any file that installs fake timers, because
 * fake-indexeddb's requests never complete when their timer source is frozen.
 *
 * The asynchronous tier has no mirror, so a value that has been flushed lives
 * only on disk. A test that stores one and reads it back is testing persistence
 * and therefore needs a place to persist to. Call this once at the top level of
 * such a file.
 *
 * DO NOT combine it with fake timers in the same file.
 */
export function useTestStorage(): void {
  beforeEach(() => {
    // Runs AFTER the global setup hook, so it is this factory the gateway
    // opens. `resetBrowserStorageForTests` drops the connection the previous
    // test cached, which points into the previous universe.
    vi.stubGlobal('indexedDB', new IDBFactory())
    resetBrowserStorageForTests()
    setStorageAccountForTests(TEST_USER_ID)
  })

  afterEach(() => {
    resetBrowserStorageForTests()
    vi.unstubAllGlobals()
  })
}
