import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { boxCenter, dragSidebarLeafTo } from './helpers/drag'
import { agentTabs, expectAgentTabCount, loginViaToken, openWorkspace, sidebarLeafIds, sidebarLeafLabels, sidebarLeaves, tabbarAgentLabels, waitForLayoutSave, waitForWorkspaceReady, workspaceChevron, workspaceRow } from './helpers/ui'

/**
 * Regression: dragging a tab from a non-active workspace's expanded
 * sidebar section to the active workspace (either onto the active
 * workspace's sidebar item or onto the active tabbar zone) used to
 * strip the tab's `title` and `agentProvider` from the destination,
 * so the sidebar row rendered the agent's nanoid and the tabbar
 * rendered "Agent" with the generic icon. Refreshing the page
 * re-fetched the agent record and the title returned — confirming
 * the data loss was purely client-side.
 *
 * Root cause: the CRDT-projection reconciler effect in `AppShell.tsx`
 * read `tabStore.state.tabs` inside its body without `untrack`. The
 * optimistic `tabStore.addTab` in the cross-workspace move handler
 * re-ran the effect against a CRDT projection that hadn't yet absorbed
 * the move op (the op only ships after the worker RPC resolves). Step
 * 1 silently removed the just-added tab as "gone from this workspace",
 * and step 2 re-added it as a bare record (no title / agentProvider /
 * git fields) after the move op finally landed.
 *
 * Defense layered with `tabStore.addTab` dedupe by `(type, id)` — an
 * HMR / concurrent-restore race could land both a bare reconciler
 * insert and a full worker-restore insert for the same id, producing
 * two sidebar rows that "closing one removes both" because removeTab
 * filters by key. The dedupe makes the first insert (typically the
 * one with metadata) win.
 *
 * This spec exercises the real drag-and-drop gesture through the
 * sidebar — the unit tests pin the reconciler / addTab contract; this
 * one pins the full UI flow end-to-end and adds a `page.reload()`
 * checkpoint so a future regression that survived in-session would
 * still fail after rehydration from the worker.
 */

test.describe('Cross-workspace sidebar drag preserves title and icon', () => {
  test('drag from non-active sidebar section to active workspace keeps title; survives reload', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // API-seed both workspaces with an agent. We pass a known title
    // (the bug strips exactly this field; the API path defaults
    // `title=""` which would render the empty-fallback both before
    // AND after the move, masking the regression).
    const wsA = await createWorkspaceViaAPI(hubUrl, adminToken, 'Drag Source')
    const wsB = await createWorkspaceViaAPI(hubUrl, adminToken, 'Drag Target')
    const wsATitle = 'Source Agent'
    const wsBTitle = 'Target Agent'
    const wsAAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, wsA, undefined, { title: wsATitle })
    await openAgentViaAPI(hubUrl, adminToken, workerId, wsB, undefined, { title: wsBTitle })

    await loginViaToken(page, adminToken)

    // Land on wsB (the destination — the user's repro had the
    // target workspace active at the moment of the drag).
    await openWorkspace(page, wsB)
    await agentTabs(page).first().waitFor()
    // Poll: the title is worker-side metadata, fetched asynchronously after
    // the tab itself renders from the CRDT projection. A one-shot read races
    // that fetch. This still fails if the title never arrives.
    await expect.poll(() => tabbarAgentLabels(page)).toEqual([wsBTitle])

    // Expand wsA in the sidebar so its tab-tree-leaf mounts and is
    // draggable. Clicking the chevron fires `onExpandWorkspace`, which
    // lazy-loads wsA's tabs, which the projection already carries.
    await workspaceChevron(page, wsA).click()
    // One leaf each under wsA (the source) and wsB (the destination --
    // already visible because wsB is active). Counted PER WORKSPACE, not
    // across the whole sidebar: a global `toHaveCount(2)` would also assert
    // that no other workspace is expanded, which this test is not about.
    await expect.poll(() => sidebarLeafLabels(page, wsA)).toHaveLength(1)
    await expect.poll(() => sidebarLeafLabels(page, wsB)).toHaveLength(1)

    // Verify wsA's leaf renders with its seeded title before the
    // drag — confirms the projection + hydrators delivered the metadata
    // we'll be asserting survives the move.
    await expect.poll(() => sidebarLeafLabels(page, wsA)).toEqual([wsATitle])

    // Target: drop on wsB's workspace item in the sidebar (it's the
    // active workspace, so this is a non-active → active move).
    const saved = waitForLayoutSave(page)
    await dragSidebarLeafTo(sidebarLeaves(page, wsA).first(), await boxCenter(workspaceRow(page, wsB)))
    await saved

    // wsB's tabbar now has two agent tabs — the original wsBTitle
    // and the moved wsATitle. Both must keep their titles; the
    // pre-fix bug would have collapsed the moved one to a bare
    // "Agent" (tabbar fallback) and its sidebar row to the nanoid.
    await expectAgentTabCount(page, 2)
    const tabbarTitles = await tabbarAgentLabels(page)
    expect(new Set(tabbarTitles)).toEqual(new Set([wsATitle, wsBTitle]))
    expect(tabbarTitles).not.toContain(wsAAgentId)
    expect(tabbarTitles).not.toContain('Agent')

    // Mirror assertion in the sidebar — wsB's section now lists
    // both tabs and neither row shows the nanoid fallback.
    const sidebarLabels = await sidebarLeafLabels(page, wsB)
    expect(new Set(sidebarLabels)).toEqual(new Set([wsATitle, wsBTitle]))

    // --- Reload checkpoint (#3) ---
    //
    // The cross-workspace move emits a CRDT `SetTabRegister(tile_id)`
    // batch and nothing else — the worker stores no workspace id, so
    // there is no worker-side RPC to pair it with. After reload, the
    // batch should be durably committed — the destination workspace
    // must still show two tabs (the original + the moved), and the
    // moved tab id must NOT have leaked back to the source.
    //
    // We intentionally only assert COUNT and id-NOT-leaked-to-source
    // here, not titles. Title round-tripping through `listAgents`
    // after a refresh interacts with several worker-side concerns
    // (openAgent title persistence, listAgents ordering vs the
    // CRDT-projection reconciler) that are independent of the bug
    // under test. A non-deterministic title result post-reload
    // would mask the actual move regression we DO cover (the
    // pre-reload assertion above).
    await page.reload()
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()

    await expectAgentTabCount(page, 2)
    // The moved tab is under wsB in the reloaded sidebar. This positive read
    // comes first: it proves that the sidebar projected the reloaded layout,
    // so the empty read of wsA below cannot pass on a tree that never rendered.
    await expect.poll(() => sidebarLeafIds(page, wsB)).toContain(wsAAgentId)
    // wsAAgentId must not appear back under wsA's sidebar section
    // after refresh — the move op committed to the hub and the
    // post-reload `listTabs(wsA)` should no longer return it.
    await workspaceChevron(page, wsA).click()
    await expect(workspaceRow(page, wsA)).toHaveAttribute('data-expanded', 'true')
    expect(await sidebarLeafIds(page, wsA)).toEqual([])
  })
})
