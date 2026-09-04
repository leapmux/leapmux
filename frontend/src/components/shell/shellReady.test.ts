import { describe, expect, it } from 'vitest'
import { isShellReady } from './shellReady'

const base = {
  workspacesLoaded: true,
  sectionsLoaded: true,
  workspaceCount: 1,
  workspaceError: null as string | null,
  activeWorkspaceId: 'ws-1' as string | null,
  centerReady: false,
  bootstrapTimedOut: false,
}

describe('isShellReady', () => {
  it('stays false until both lists have completed an attempt', () => {
    expect(isShellReady({ ...base, workspacesLoaded: false, centerReady: true })).toBe(false)
    expect(isShellReady({ ...base, sectionsLoaded: false, centerReady: true })).toBe(false)
  })

  it('becomes true when the active workspace centre is ready', () => {
    expect(isShellReady({ ...base, centerReady: true })).toBe(true)
  })

  it('becomes true when the CRDT bootstrap watchdog fires', () => {
    expect(isShellReady({ ...base, bootstrapTimedOut: true })).toBe(true)
  })

  it('becomes true for a genuine empty workspace list', () => {
    expect(isShellReady({
      ...base,
      workspaceCount: 0,
      activeWorkspaceId: null,
      centerReady: false,
    })).toBe(true)
  })

  it('becomes true when the list failed and nothing is selected', () => {
    expect(isShellReady({
      ...base,
      workspaceCount: 0,
      workspaceError: 'Failed to load workspaces',
      activeWorkspaceId: null,
      centerReady: false,
    })).toBe(true)
  })

  it('stays false in the pre-adopt gap: workspaces exist but no id is selected yet', () => {
    expect(isShellReady({
      ...base,
      workspaceCount: 2,
      activeWorkspaceId: null,
      centerReady: false,
    })).toBe(false)
  })

  it('stays false while an active workspace waits on its CRDT tree', () => {
    expect(isShellReady({
      ...base,
      activeWorkspaceId: 'ws-1',
      centerReady: false,
      bootstrapTimedOut: false,
    })).toBe(false)
  })

  it('keeps the overlay when a list error leaves an active id without a ready centre', () => {
    // A hub blip must not yank the user off a workspace they were on; the
    // shell stays under the overlay until centreReady or the watchdog.
    expect(isShellReady({
      ...base,
      workspaceError: 'Failed to load workspaces',
      activeWorkspaceId: 'ws-1',
      centerReady: false,
    })).toBe(false)
  })
})
