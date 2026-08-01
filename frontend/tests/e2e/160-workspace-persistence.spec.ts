import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, openWorkspace, reopenWorkspace, waitForWorkspaceReady, workspaceRow } from './helpers/ui'

/**
 * Which workspace the app opens on used to be carried by the URL
 * (`/workspace/{id}`), so a reload was self-describing. It is now a per-user
 * localStorage entry read by `resolveActiveWorkspace`, which makes these the
 * only end-to-end checks that the selection survives a reload at all — and that
 * a selection pointing at a workspace the user no longer has degrades to a
 * sibling rather than to an empty shell.
 */
test.describe('Workspace persistence across reloads', () => {
  test('reload reopens the workspace the user switched to, not the default one', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist Alpha')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist Beta')
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)

      // Find out which workspace a cold start picks, then switch to the OTHER
      // one. Naming a workspace up front would make this vacuous: sidebar order
      // is section position, not creation order, so hard-coding the target can
      // silently pick the very workspace the no-saved-id fallback lands on --
      // and then a build that ignored the saved id entirely would still pass.
      await page.goto('/')
      // Wait for THIS test's two rows, not for a global count. `leapmuxServer`
      // is worker-scoped: one dev instance serves every spec file the Playwright
      // worker runs, and a best-effort teardown elsewhere can leave workspaces
      // behind, so `toHaveCount(2)` was really asserting "no other spec file ran
      // on this worker first" -- which is true or false depending on the shard
      // and the worker count.
      await expect(workspaceRow(page, ws1)).toBeVisible()
      await expect(workspaceRow(page, ws2)).toBeVisible()
      const activeRow = page.locator('[data-testid^="workspace-item-"][data-active="true"]').first()
      await expect(activeRow).toBeVisible()
      const activeTestId = await activeRow.getAttribute('data-testid')
      // Whichever workspace a cold start lands on -- one of ours or a leftover
      // from an earlier file -- the target is a DIFFERENT one, so the reload
      // below still has to honour the saved id rather than the default.
      const fallback = activeTestId!.replace('workspace-item-', '')
      const target = fallback === ws1 ? ws2 : ws1

      await openWorkspace(page, target)
      await reopenWorkspace(page, target)
      await expect(workspaceRow(page, fallback))
        .toHaveAttribute('data-active', 'false')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('a persisted workspace that was deleted elsewhere falls back to a sibling', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const survivor = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist Survivor')
    const doomed = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist Doomed')
    await openAgentViaAPI(hubUrl, adminToken, workerId, survivor)
    await openAgentViaAPI(hubUrl, adminToken, workerId, doomed)

    try {
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
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, survivor).catch(() => {})
    }
  })

  test('switching workspaces leaves the URL at the app home', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist URL Alpha')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist URL Beta')
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, ws1)
      await expect(page).toHaveURL(/\/$/)

      await workspaceRow(page, ws2).click()
      await expect(workspaceRow(page, ws2))
        .toHaveAttribute('data-active', 'true')
      // The point of the change: a switch is not a navigation.
      await expect(page).toHaveURL(/\/$/)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('a retired /workspace/{id} URL is a 404, not the app', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/workspace/any-id-at-all')

    // No sidebar, no shell: the path fell through to the catch-all route, which
    // sits outside the `(app)` group and so outside AppShell entirely.
    await expect(page.locator('[data-testid^="workspace-item-"]')).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Go to Dashboard' })).toBeVisible()
  })
})
