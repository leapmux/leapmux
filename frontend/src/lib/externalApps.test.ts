/// <reference types="vitest/globals" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ExternalAppKind } from '~/generated/proto/leapmux/desktop/v1/frame_pb'
import {
  _resetExternalAppCacheForTests,
  isFileManager,
  loadExternalApps,
  resolvePreferredExternalApp,
} from './externalApps'

const listAppsMock = vi.fn()

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    platformBridge: {
      ...actual.platformBridge,
      listExternalApps: (refresh?: boolean) => listAppsMock(refresh ?? false),
    },
  }
})

describe('loadExternalApps', () => {
  beforeEach(() => {
    _resetExternalAppCacheForTests()
    listAppsMock.mockReset()
  })
  afterEach(() => {
    _resetExternalAppCacheForTests()
  })

  it('returns the bridge result', async () => {
    listAppsMock.mockResolvedValueOnce([
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
    ])
    const got = await loadExternalApps()
    expect(got).toEqual([
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
    ])
  })

  it('caches across calls (single IPC round-trip)', async () => {
    listAppsMock.mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    await loadExternalApps()
    await loadExternalApps()
    await loadExternalApps()
    expect(listAppsMock).toHaveBeenCalledTimes(1)
  })

  it('coalesces concurrent callers into one in-flight promise', async () => {
    let resolveList: (v: unknown) => void = () => {}
    listAppsMock.mockImplementationOnce(() => new Promise((r) => {
      resolveList = r
    }))
    const a = loadExternalApps()
    const b = loadExternalApps()
    resolveList([{ id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR }])
    const [ra, rb] = await Promise.all([a, b])
    expect(ra).toEqual(rb)
    expect(listAppsMock).toHaveBeenCalledTimes(1)
  })

  it('lets a later caller retry after a failure', async () => {
    listAppsMock.mockRejectedValueOnce(new Error('boom'))
    await expect(loadExternalApps()).rejects.toThrow('boom')
    listAppsMock.mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    const got = await loadExternalApps()
    expect(got).toEqual([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
  })

  it('bypasses cache and re-asks the bridge when refresh=true', async () => {
    listAppsMock
      .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
      .mockResolvedValueOnce([
        { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
        { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
      ])
    const first = await loadExternalApps()
    const second = await loadExternalApps(true)
    expect(first).toHaveLength(1)
    expect(second).toHaveLength(2)
    expect(listAppsMock).toHaveBeenCalledTimes(2)
    expect(listAppsMock).toHaveBeenLastCalledWith(true)
  })

  it('preserves object identity for unchanged applications across refreshes', async () => {
    // The Tauri bridge always hands us freshly-deserialized objects, so
    // even an unchanged application arrives as a different object reference on
    // each call. We must stabilize those references so Solid's <For>
    // doesn't tear down and re-create unchanged menu items (which causes
    // a downstream chat-scroll bug from the resulting layout thrash).
    listAppsMock
      .mockResolvedValueOnce([
        { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
        { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
      ])
      .mockResolvedValueOnce([
        // Same id+name as the first call, but a brand-new object.
        { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
        // `zed` removed; brand-new object for vscode again.
      ])
    const first = await loadExternalApps()
    const firstVscode = first.find(e => e.id === 'vscode')
    const second = await loadExternalApps(true)
    const secondVscode = second.find(e => e.id === 'vscode')
    expect(secondVscode).toBe(firstVscode) // same reference, not just equal
    expect(second).toHaveLength(1)
  })

  it('refreshes object identity when displayName changes', async () => {
    listAppsMock
      .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
      .mockResolvedValueOnce([{ id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR }])
    const first = await loadExternalApps()
    const second = await loadExternalApps(true)
    // Different displayName → must be a new reference so Solid re-renders.
    expect(second[0]).not.toBe(first[0])
    expect(second[0].displayName).toBe('Visual Studio Code')
  })
})

describe('resolvePreferredExternalApp', () => {
  const list = [
    { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
    { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
  ]

  it('returns undefined for an empty list, and persists nothing', () => {
    const persist = vi.fn()
    expect(resolvePreferredExternalApp([], 'zed', persist)).toBeUndefined()
    expect(persist).not.toHaveBeenCalled()
  })

  it('returns the pinned editor when it is still detected, and persists nothing', () => {
    const persist = vi.fn()
    expect(resolvePreferredExternalApp(list, 'zed', persist)?.id).toBe('zed')
    expect(persist).not.toHaveBeenCalled()
  })

  it('falls back to the first entry and persists when the pin is gone', () => {
    const persist = vi.fn()
    expect(resolvePreferredExternalApp(list, 'idea', persist)?.id).toBe('vscode')
    expect(persist).toHaveBeenCalledWith('vscode')
  })

  it('falls back to the first entry when nothing is pinned, and persists it', () => {
    const persist = vi.fn()
    expect(resolvePreferredExternalApp(list, undefined, persist)?.id).toBe('vscode')
    expect(persist).toHaveBeenCalledWith('vscode')
  })

  // Both directions are the CALLER's. This module holds no storage
  // accessor for the pin at all now: a second, non-reactive reader beside
  // the preference signal is what put the menu label and the editor a
  // launch opened out of step for the life of a page.
  it('persists only through the injected writer', () => {
    const persist = vi.fn()
    expect(resolvePreferredExternalApp(list, 'idea', persist)?.id).toBe('vscode')
    expect(persist).toHaveBeenCalledTimes(1)
  })

  // The file manager leads the detected list on every platform and is always
  // present, so a fallback that took the first entry would open Finder for
  // every user who never picked -- on a machine with three editors installed.
  describe('the fallback with the file manager present', () => {
    const withFileManager = [
      { id: 'file-manager', displayName: 'Finder', kind: ExternalAppKind.FILE_MANAGER },
      { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
    ]

    it('skips the file manager and takes the first editor when nothing is pinned', () => {
      const persist = vi.fn()
      expect(resolvePreferredExternalApp(withFileManager, undefined, persist)?.id).toBe('vscode')
      expect(persist).toHaveBeenCalledWith('vscode')
    })

    it('skips it again when the pin names an application that is gone', () => {
      const persist = vi.fn()
      expect(resolvePreferredExternalApp(withFileManager, 'idea', persist)?.id).toBe('vscode')
      expect(persist).toHaveBeenCalledWith('vscode')
    })

    // The pin is tried FIRST, so an explicit choice of the file manager wins.
    it('keeps the file manager when it is the pinned choice', () => {
      const persist = vi.fn()
      expect(resolvePreferredExternalApp(withFileManager, 'file-manager', persist)?.id).toBe('file-manager')
      expect(persist).not.toHaveBeenCalled()
    })

    it('answers the file manager when it is the only application detected', () => {
      const persist = vi.fn()
      const only = [withFileManager[0]!]
      expect(resolvePreferredExternalApp(only, undefined, persist)?.id).toBe('file-manager')
      expect(persist).toHaveBeenCalledWith('file-manager')
    })
  })
})

describe('isFileManager', () => {
  // Asked of the KIND, never of the id: the app menu groups by it and the
  // repository block drops its "Open in ..." row for a file-manager default.
  // An id test would have to be repeated at every one of those sites.
  it('answers true for the file-manager kind', () => {
    expect(isFileManager({ id: 'file-manager', displayName: 'Finder', kind: ExternalAppKind.FILE_MANAGER })).toBe(true)
  })

  it('answers false for an editor', () => {
    expect(isFileManager({ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR })).toBe(false)
  })

  it('answers false for an absent application', () => {
    expect(isFileManager(undefined)).toBe(false)
  })

  // A sidecar that sent no kind at all must not be mistaken for the file
  // manager, which would hide the "Open in ..." row for every editor.
  it('answers false for the unset kind', () => {
    expect(isFileManager({ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.UNSPECIFIED })).toBe(false)
  })
})
