/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { KEY_ACTIVE_WORKSPACE, localStorageGet, resetStorageAccountForTests, setStorageAccount } from '~/lib/browserStorage'
import { emitAddTab } from '~/stores/tabOps'
import { TEST_USER_ID, withTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
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

function setup() {
  const stores = createTestTabStores('ws-test')
  const active: Array<string | null> = []
  const switchWorkspace = createWorkspaceSwitcher({
    setActiveWorkspaceId: next => active.push(next),
  })
  return { ...stores, switchWorkspace, active }
}

describe('createWorkspaceSwitcher', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('persists the new workspace', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        const { switchWorkspace } = setup()
        switchWorkspace('w1')
        expect(localStorageGet<string>(KEY_ACTIVE_WORKSPACE)).toBe('w1')
        dispose()
      })
    })
  })

  // The switcher no longer takes a user id: browserStorage scopes every key to
  // the signed-in account, so two accounts keep their own last workspace
  // without any caller threading an id through. This is the same guarantee the
  // old per-user key template gave, moved under the whole namespace.
  it('keeps each account on its own key', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        const { switchWorkspace } = setup()
        switchWorkspace('w1')

        setStorageAccount('bob')
        switchWorkspace('w2')
        expect(localStorageGet<string>(KEY_ACTIVE_WORKSPACE)).toBe('w2')

        setStorageAccount(TEST_USER_ID)
        expect(localStorageGet<string>(KEY_ACTIVE_WORKSPACE)).toBe('w1')
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
        expect(localStorageGet<string>(KEY_ACTIVE_WORKSPACE)).toBeUndefined()
        dispose()
      })
    })
  })

  // Replaces 'writes nothing when the user is not restored yet'. That case
  // guarded a blank user id keying the write to a name no later read could
  // reconstruct; the id is no longer part of the name, so the guard is gone and
  // the namespace enforces the same thing one level down -- loudly, because
  // AppShell only ever builds this switcher inside AuthGuard.
  it('throws rather than writing when no account is set', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        const { switchWorkspace, active } = setup()
        resetStorageAccountForTests()
        expect(() => switchWorkspace('w1')).toThrow(/No storage account is set/)
        // The signal still flipped: the write is the last statement.
        expect(active).toEqual(['w1'])
        setStorageAccount(TEST_USER_ID)
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
