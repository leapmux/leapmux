import type { Mock } from 'vitest'

/**
 * Spy bundle for tests that exercise the file-save action wiring
 * (`createFileSaveActions` + `FileActionsMenu`). Each test file creates
 * a fresh bundle via `vi.hoisted` so the references survive vitest's
 * vi.mock hoisting:
 *
 *   const spies = vi.hoisted<SaveActionsSpies>(() => ({
 *     downloadImpl: vi.fn(),
 *     saveAsImpl: vi.fn(),
 *     ...
 *   }))
 *
 * The bundle is then passed to vi.mock factories via the helpers
 * exported below, and reset between tests with `resetSaveActionsSpies`.
 */
export interface SaveActionsSpies {
  downloadImpl: Mock
  saveAsImpl: Mock
  saveToDownloadsImpl: Mock
  revealImpl: Mock
  warnToastImpl: Mock
  infoToastImpl: Mock
  revealAfterDownloadImpl: Mock<() => boolean>
  isTauriAppImpl: Mock<() => boolean>
}

// vi.mock factory bodies — each returns an object suitable for the
// matching `vi.mock(path, () => ...)` call. The factory itself runs
// lazily on first import of the mocked module, so referencing the
// `spies` bundle is safe even though `vi.mock` is hoisted.

export function platformBridgeMock(spies: SaveActionsSpies) {
  return {
    isTauriApp: () => spies.isTauriAppImpl(),
    platformBridge: {
      revealInFileManager: (...args: unknown[]) => spies.revealImpl(...args),
      ...platformBridgeFileSaveStubs(),
    },
  }
}

/**
 * The five `fileSave*` methods on `platformBridge`, stubbed to no-op
 * resolved promises. Tests that don't exercise the save-stream flow but
 * mock `~/api/platformBridge` (e.g. components that just need
 * `platformBridge` defined) can spread this into their mock so the
 * stubs stay in sync with the production interface.
 *
 * `fileSaveOpen` resolves a non-empty sentinel path so callers that
 * propagate it into `revealInFileManager` see a truthy value (matching
 * production behavior); tests asserting on the exact path should pin
 * their own mock.
 */
export function platformBridgeFileSaveStubs() {
  return {
    fileSaveOpen: () => Promise.resolve({ id: 1, path: '/tmp/stub' }),
    fileSaveOpenDialog: () => Promise.resolve(null),
    fileSaveWrite: () => Promise.resolve(),
    fileSaveCommit: () => Promise.resolve(),
    fileSaveAbort: () => Promise.resolve(),
  }
}

export function fileDownloadMock(spies: SaveActionsSpies) {
  return {
    downloadFileFromWorker: (...args: unknown[]) => spies.downloadImpl(...args),
    saveFileAs: (...args: unknown[]) => spies.saveAsImpl(...args),
    saveFileToDownloads: (...args: unknown[]) => spies.saveToDownloadsImpl(...args),
  }
}

/**
 * The WHOLE surface of `~/components/common/Toast`, not only the two helpers
 * that the save actions call.
 *
 * A `vi.mock` factory replaces the module for every importer in the file,
 * including a transitive one -- `~/lib/clipboard` announces a failed write
 * through `showWarnToastWithLoggedCause` -- and a named import that the factory
 * omits fails the whole test file at load with "No such export". Every warning
 * helper routes to `warnToastImpl`, so a test can still assert on any of them.
 */
export function toastMock(spies: SaveActionsSpies) {
  return {
    showWarnToast: (...args: unknown[]) => spies.warnToastImpl(...args),
    showInfoToast: (...args: unknown[]) => spies.infoToastImpl(...args),
    showWarnToastWithLoggedCause: (...args: unknown[]) => spies.warnToastImpl(...args),
    showWarnToastUnlessDisconnected: (...args: unknown[]) => spies.warnToastImpl(...args),
    showStickyWarnToast: (...args: unknown[]) => spies.warnToastImpl(...args),
    showErrorToast: (...args: unknown[]) => spies.warnToastImpl(...args),
  }
}

export function preferencesMock(spies: SaveActionsSpies) {
  return {
    usePreferences: () => ({
      revealAfterDownload: () => spies.revealAfterDownloadImpl(),
      setRevealAfterDownload: () => {},
    }),
  }
}

/**
 * Rolls every spy back to its default resolved value. Call from
 * `beforeEach`. Pass per-test overrides afterwards.
 */
export function resetSaveActionsSpies(spies: SaveActionsSpies): void {
  spies.downloadImpl.mockReset()
  spies.downloadImpl.mockResolvedValue(undefined)
  spies.saveAsImpl.mockReset()
  spies.saveAsImpl.mockResolvedValue('/home/alice/Downloads/file.ts')
  spies.saveToDownloadsImpl.mockReset()
  spies.saveToDownloadsImpl.mockResolvedValue('/home/alice/Downloads/file.ts')
  spies.revealImpl.mockReset()
  spies.warnToastImpl.mockReset()
  spies.infoToastImpl.mockReset()
  spies.revealAfterDownloadImpl.mockReset()
  spies.revealAfterDownloadImpl.mockReturnValue(true)
  spies.isTauriAppImpl.mockReset()
  spies.isTauriAppImpl.mockReturnValue(false)
}
