/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { localStorageGet } from '~/lib/browserStorage'
import { emitAddTab } from '~/stores/tabOps'
import { withTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { activeWorkspaceKey } from './tabPersistenceKeys'
import { createWorkspaceSwitcher } from './workspaceSwitcher'

/**
 * What switching workspaces still does.
 *
 * It used to snapshot the outgoing workspace's whole tab + layout state into
 * the registry, in a strict order, because the active store was about to be
 * wiped. Those tests are gone with the behaviour: nothing is wiped, every
 * workspace's tabs stay derived from the projection, and switching only changes
 * which slice the shell renders.
 *
 * One thing survives: persisting which workspace is active. Terminal scrollback
 * used to be flushed here too; it now rides `disposeTerminalInstance`, so its
 * coverage lives in `TerminalView.test.tsx` alongside the other teardown paths.
 */

const USER = 'u1'

function setup(getUserId: () => string = () => USER) {
  const stores = createTestTabStores('ws-test')
  const active: Array<string | null> = []
  const switchWorkspace = createWorkspaceSwitcher({
    getUserId,
    setActiveWorkspaceId: next => active.push(next),
  })
  return { ...stores, switchWorkspace, active }
}

describe('createWorkspaceSwitcher', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('persists the new workspace under this user key', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        const { switchWorkspace } = setup()
        switchWorkspace('w1')
        expect(localStorageGet<string>(activeWorkspaceKey(USER))).toBe('w1')
        dispose()
      })
    })
  })

  it('keeps each user id on its own key', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        let user = 'alice'
        const { switchWorkspace } = setup(() => user)
        switchWorkspace('w1')
        user = 'bob'
        switchWorkspace('w2')
        expect(localStorageGet<string>(activeWorkspaceKey('alice'))).toBe('w1')
        expect(localStorageGet<string>(activeWorkspaceKey('bob'))).toBe('w2')
        dispose()
      })
    })
  })

  it('removes the persisted key when the selection is cleared', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        const { switchWorkspace } = setup()
        switchWorkspace('w1')
        switchWorkspace(null)
        expect(localStorageGet<string>(activeWorkspaceKey(USER))).toBeUndefined()
        dispose()
      })
    })
  })

  // The switcher runs before the session restore resolves on a cold load. A
  // blank user id would key the write to `leapmux:activeWorkspace:`, which no
  // later read reconstructs -- so skip the write and keep the signal flip.
  it('flips the signal but writes nothing when the user is not restored yet', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        const { switchWorkspace, active } = setup(() => '')
        switchWorkspace('w1')
        expect(active).toEqual(['w1'])
        expect(localStorage.getItem(activeWorkspaceKey(''))).toBeNull()
        dispose()
      })
    })
  })

  it('does not touch tabs when switching to the workspace already active', () => {
    withTestBridge((harness) => {
      createRoot((dispose) => {
        const { switchWorkspace, view, active } = setup()
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M' })
        switchWorkspace(harness.workspaceId)
        switchWorkspace(harness.workspaceId)

        expect(active).toEqual([harness.workspaceId, harness.workspaceId])
        // The tab is still there. Under the old design this is where a
        // mis-ordered snapshot could clobber the outgoing workspace's tabs.
        expect(view.forWorkspace(harness.workspaceId).map(t => t.id)).toEqual(['a1'])
        dispose()
      })
    })
  })
})
