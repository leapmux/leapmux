import type { TerminalNotification } from '~/generated/proto/leapmux/v1/terminal_pb'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTabMetadataStore } from '~/stores/tabMetadata.store'
import { handleTerminalBell, handleTerminalNotification } from './terminalEvents'

const notifyOs = vi.fn()
vi.mock('~/lib/osNotification', () => ({
  notifyOs: (...args: unknown[]) => notifyOs(...args),
}))

const QUAKE_ID = 'quake-1'

/**
 * A companion terminal has NO tab, so every field the placed-terminal branch
 * reads is absent for it: no tile, no workspace, and no key any selection
 * carries. That is the whole point of the detached predicate, so the fakes here
 * answer nothing rather than pretending otherwise.
 */
function deps(over: { isDetachedOnScreen?: (id: string) => boolean } = {}) {
  const metadata = createTabMetadataStore()
  const selection = {
    activeKeyForTile: () => null,
    activeKeyForWorkspace: () => null,
  } as unknown as TabSelectionStore
  return {
    metadata,
    selection,
    getActiveWorkspaceId: () => 'ws-1',
    view: { getTerminalTab: () => undefined },
    isDetachedOnScreen: over.isDetachedOnScreen,
  }
}

const NOTIFICATION = { title: 'build', body: 'done' } as TerminalNotification

beforeEach(() => {
  notifyOs.mockReset()
})

describe('handleTerminalNotification for a quake terminal', () => {
  // The predicate is asked FIRST for exactly this case. Without it a companion
  // falls to the workspace-key branch, which can never match an id that is not
  // a tab -- so an OSC 9 in a shell the user is watching would raise a desktop
  // notification and badge a row nobody can see.
  it('raises no OS notification while its panel is open and its owner is on screen', () => {
    const d = deps({ isDetachedOnScreen: id => id === QUAKE_ID })

    handleTerminalNotification(QUAKE_ID, NOTIFICATION, d)

    expect(notifyOs).not.toHaveBeenCalled()
    expect(d.metadata.get(QUAKE_ID)?.hasNotification).toBeUndefined()
  })

  it('raises one while its panel is closed', () => {
    const d = deps({ isDetachedOnScreen: () => false })

    handleTerminalNotification(QUAKE_ID, NOTIFICATION, d)

    expect(notifyOs).toHaveBeenCalledWith({ title: 'build', body: 'done', tag: QUAKE_ID })
    expect(d.metadata.get(QUAKE_ID)?.hasNotification).toBe(true)
  })

  // A caller with no panels at all -- every unit test of the placed path, and
  // the background-workspace arm -- must behave exactly as it did before.
  it('raises one when the caller supplies no detached predicate', () => {
    const d = deps()

    handleTerminalNotification(QUAKE_ID, NOTIFICATION, d)

    expect(notifyOs).toHaveBeenCalledOnce()
  })
})

describe('handleTerminalBell for a quake terminal', () => {
  it('badges nothing while its panel is open and its owner is on screen', () => {
    const d = deps({ isDetachedOnScreen: () => true })

    handleTerminalBell(QUAKE_ID, d)

    expect(d.metadata.get(QUAKE_ID)?.hasNotification).toBeUndefined()
  })

  it('badges the row while its panel is closed', () => {
    const d = deps({ isDetachedOnScreen: () => false })

    handleTerminalBell(QUAKE_ID, d)

    expect(d.metadata.get(QUAKE_ID)?.hasNotification).toBe(true)
  })
})
