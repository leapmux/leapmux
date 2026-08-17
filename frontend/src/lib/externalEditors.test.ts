/// <reference types="vitest/globals" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  _resetEditorCacheForTests,
  loadDetectedEditors,
  resolvePreferredEditor,
} from './externalEditors'

const listEditorsMock = vi.fn()

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    platformBridge: {
      ...actual.platformBridge,
      listEditors: (refresh?: boolean) => listEditorsMock(refresh ?? false),
    },
  }
})

describe('loadDetectedEditors', () => {
  beforeEach(() => {
    _resetEditorCacheForTests()
    listEditorsMock.mockReset()
  })
  afterEach(() => {
    _resetEditorCacheForTests()
  })

  it('returns the bridge result', async () => {
    listEditorsMock.mockResolvedValueOnce([
      { id: 'vscode', displayName: 'Visual Studio Code' },
      { id: 'zed', displayName: 'Zed' },
    ])
    const got = await loadDetectedEditors()
    expect(got).toEqual([
      { id: 'vscode', displayName: 'Visual Studio Code' },
      { id: 'zed', displayName: 'Zed' },
    ])
  })

  it('caches across calls (single IPC round-trip)', async () => {
    listEditorsMock.mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
    await loadDetectedEditors()
    await loadDetectedEditors()
    await loadDetectedEditors()
    expect(listEditorsMock).toHaveBeenCalledTimes(1)
  })

  it('coalesces concurrent callers into one in-flight promise', async () => {
    let resolveList: (v: unknown) => void = () => {}
    listEditorsMock.mockImplementationOnce(() => new Promise((r) => {
      resolveList = r
    }))
    const a = loadDetectedEditors()
    const b = loadDetectedEditors()
    resolveList([{ id: 'zed', displayName: 'Zed' }])
    const [ra, rb] = await Promise.all([a, b])
    expect(ra).toEqual(rb)
    expect(listEditorsMock).toHaveBeenCalledTimes(1)
  })

  it('lets a later caller retry after a failure', async () => {
    listEditorsMock.mockRejectedValueOnce(new Error('boom'))
    await expect(loadDetectedEditors()).rejects.toThrow('boom')
    listEditorsMock.mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
    const got = await loadDetectedEditors()
    expect(got).toEqual([{ id: 'vscode', displayName: 'VS Code' }])
  })

  it('bypasses cache and re-asks the bridge when refresh=true', async () => {
    listEditorsMock
      .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
      .mockResolvedValueOnce([
        { id: 'vscode', displayName: 'VS Code' },
        { id: 'zed', displayName: 'Zed' },
      ])
    const first = await loadDetectedEditors()
    const second = await loadDetectedEditors(true)
    expect(first).toHaveLength(1)
    expect(second).toHaveLength(2)
    expect(listEditorsMock).toHaveBeenCalledTimes(2)
    expect(listEditorsMock).toHaveBeenLastCalledWith(true)
  })

  it('preserves object identity for unchanged editors across refreshes', async () => {
    // The Tauri bridge always hands us freshly-deserialized objects, so
    // even an unchanged editor arrives as a different object reference on
    // each call. We must stabilize those references so Solid's <For>
    // doesn't tear down and re-create unchanged menu items (which causes
    // a downstream chat-scroll bug from the resulting layout thrash).
    listEditorsMock
      .mockResolvedValueOnce([
        { id: 'vscode', displayName: 'VS Code' },
        { id: 'zed', displayName: 'Zed' },
      ])
      .mockResolvedValueOnce([
        // Same id+name as the first call, but a brand-new object.
        { id: 'vscode', displayName: 'VS Code' },
        // `zed` removed; brand-new object for vscode again.
      ])
    const first = await loadDetectedEditors()
    const firstVscode = first.find(e => e.id === 'vscode')
    const second = await loadDetectedEditors(true)
    const secondVscode = second.find(e => e.id === 'vscode')
    expect(secondVscode).toBe(firstVscode) // same reference, not just equal
    expect(second).toHaveLength(1)
  })

  it('refreshes object identity when displayName changes', async () => {
    listEditorsMock
      .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
      .mockResolvedValueOnce([{ id: 'vscode', displayName: 'Visual Studio Code' }])
    const first = await loadDetectedEditors()
    const second = await loadDetectedEditors(true)
    // Different displayName → must be a new reference so Solid re-renders.
    expect(second[0]).not.toBe(first[0])
    expect(second[0].displayName).toBe('Visual Studio Code')
  })
})

describe('resolvePreferredEditor', () => {
  const list = [
    { id: 'vscode', displayName: 'VS Code' },
    { id: 'zed', displayName: 'Zed' },
  ]

  it('returns undefined for an empty list, and persists nothing', () => {
    const persist = vi.fn()
    expect(resolvePreferredEditor([], 'zed', persist)).toBeUndefined()
    expect(persist).not.toHaveBeenCalled()
  })

  it('returns the pinned editor when it is still detected, and persists nothing', () => {
    const persist = vi.fn()
    expect(resolvePreferredEditor(list, 'zed', persist)?.id).toBe('zed')
    expect(persist).not.toHaveBeenCalled()
  })

  it('falls back to the first entry and persists when the pin is gone', () => {
    const persist = vi.fn()
    expect(resolvePreferredEditor(list, 'idea', persist)?.id).toBe('vscode')
    expect(persist).toHaveBeenCalledWith('vscode')
  })

  it('falls back to the first entry when nothing is pinned, and persists it', () => {
    const persist = vi.fn()
    expect(resolvePreferredEditor(list, undefined, persist)?.id).toBe('vscode')
    expect(persist).toHaveBeenCalledWith('vscode')
  })

  // Both directions are the CALLER's. This module holds no storage
  // accessor for the pin at all now: a second, non-reactive reader beside
  // the preference signal is what put the menu label and the editor a
  // launch opened out of step for the life of a page.
  it('persists only through the injected writer', () => {
    const persist = vi.fn()
    expect(resolvePreferredEditor(list, 'idea', persist)?.id).toBe('vscode')
    expect(persist).toHaveBeenCalledTimes(1)
  })
})
