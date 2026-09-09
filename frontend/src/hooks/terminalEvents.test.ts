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
 * A quake terminal has NO tab, so every field the placed-terminal branch
 * reads is absent for it: no tile, no workspace, and no key any selection
 * carries. That is the whole point of the detached predicate, so the fakes here
 * answer nothing rather than pretending otherwise.
 */
function deps(over: {
  isDetachedOnScreen?: (id: string) => boolean
  detachedOwnerOf?: (id: string) => string | undefined
} = {}) {
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
    detachedOwnerOf: over.detachedOwnerOf,
  }
}

const NOTIFICATION = { title: 'build', body: 'done' } as TerminalNotification

beforeEach(() => {
  notifyOs.mockReset()
})

describe('handleTerminalNotification for a quake terminal', () => {
  // The predicate is asked FIRST for exactly this case. Without it a quake
  // terminal falls to the workspace-key branch, which can never match an id that is not
  // a tab -- so an OSC 9 in a shell the user watches would raise a desktop
  // notification and badge a row nobody can see.
  it('raises no OS notification while its panel is open and its directory is focused', () => {
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

  // The badge goes on the row a surface RENDERS. No surface renders a quake
  // terminal: the tab strip and the sidebar tree both derive from the placed
  // tabs. Worse, nothing could ever clear it -- the one clear site runs from
  // tab selection, and a quake terminal is never selected -- so the flag would
  // sit on an invisible row until the shell exits. A TAB in its working
  // directory is rendered, and it is where the user goes to reach the panel.
  it('badges a tab in the directory, not the quake terminal nobody renders', () => {
    const d = deps({
      isDetachedOnScreen: () => false,
      detachedOwnerOf: id => (id === QUAKE_ID ? 'agent-1' : undefined),
    })

    handleTerminalNotification(QUAKE_ID, NOTIFICATION, d)

    expect(d.metadata.get('agent-1')?.hasNotification).toBe(true)
    expect(d.metadata.get(QUAKE_ID)?.hasNotification).toBeUndefined()
  })

  it('badges a placed terminal on its own row', () => {
    const d = deps({ isDetachedOnScreen: () => false, detachedOwnerOf: () => undefined })

    handleTerminalBell('t-placed', d)

    expect(d.metadata.get('t-placed')?.hasNotification).toBe(true)
  })

  // A caller with no panels at all -- every unit test of the placed path, and
  // the background-workspace branch -- must behave exactly as before.
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
