import { expect, test } from './fixtures'
import { deleteWorkspaceViaAPI } from './helpers/api'
import { activeWorkspaceId, loginViaToken, openWorkspace, reopenWorkspace, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'

/**
 * Which workspace the app opens on used to be carried by the URL
 * (`/workspace/{id}`), so a reload was self-describing. It is now a per-user
 * browser-storage entry read by `resolveActiveWorkspace`, which makes these the
 * only end-to-end checks that the selection survives a reload at all — and that
 * a selection pointing at a workspace the user no longer has degrades to a
 * sibling rather than to an empty shell.
 */
test.describe('Workspace persistence across reloads', () => {
  test('reload reopens the workspace the user switched to, not the default one', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist Alpha')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist Beta')

    await loginViaToken(page, leapmuxServer.adminToken)

    // Find out which workspace a cold start picks, then switch to the OTHER
    // one. Naming a workspace up front would make this vacuous: sidebar order
    // is section position, not creation order, so hard-coding the target can
    // silently pick the very workspace the no-saved-id fallback lands on --
    // and then a build that ignored the saved id entirely would still pass.
    await page.goto('/')
    // Wait for THIS test's two rows, not for a global count. `leapmuxServer`
    // is worker-scoped: one dev instance serves every spec file the Playwright
    // worker runs, so a global `toHaveCount(2)` would also assert that no other
    // workspace exists, which is the job of the per-test reset, not this test.
    await expect(workspaceRow(page, ws1)).toBeVisible()
    await expect(workspaceRow(page, ws2)).toBeVisible()
    // Whichever workspace a cold start lands on, the target is a DIFFERENT
    // one, so the reload below still has to honour the saved id rather than
    // the default.
    const fallback = await activeWorkspaceId(page)
    const target = fallback === ws1 ? ws2 : ws1

    await openWorkspace(page, target)
    await reopenWorkspace(page, target)
    await expect(workspaceRow(page, fallback))
      .toHaveAttribute('data-active', 'false')
  })

  test('a persisted workspace that was deleted elsewhere falls back to a sibling', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const { workspaceId: survivor } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist Survivor')
    const { workspaceId: doomed } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist Doomed')

    await loginViaToken(page, adminToken)
    await openWorkspace(page, doomed)

    // Delete out-of-band, the way another device or the CLI would. The tab
    // holding this page never sees the click, so all it has on the next load
    // is a persisted id for a workspace the hub no longer lists.
    await deleteWorkspaceViaAPI(hubUrl, adminToken, doomed)

    await page.goto('/')
    await expect(workspaceRow(page, survivor))
      .toHaveAttribute('data-active', 'true')
    await expect(workspaceRow(page, doomed)).toHaveCount(0)
    // The fall-back has to be a real activation, not just a highlighted row.
    await waitForWorkspaceReady(page)
  })

  test('switching workspaces leaves the URL at the app home', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist URL Alpha')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist URL Beta')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await expect(page).toHaveURL(/\/$/)

    await workspaceRowTitle(page, ws2).click()
    await expect(workspaceRow(page, ws2))
      .toHaveAttribute('data-active', 'true')
    // The point of the change: a switch is not a navigation.
    await expect(page).toHaveURL(/\/$/)
  })

  test('a retired /workspace/{id} URL is a 404, not the app', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/workspace/any-id-at-all')

    // No sidebar, no shell: the path fell through to the catch-all route, which
    // sits outside the `(app)` group and so outside AppShell entirely. The count
    // covers every mounted copy, visible or not.
    await expect(page.locator('[data-testid^="workspace-item-"]')).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Go to Dashboard' })).toBeVisible()
  })
})
