import { expect, test } from './fixtures'
import {
  createWorkspaceViaAPI,
  getUserId,
  inviteToOrgViaAPI,
  openAgentViaAPI,
  shareWorkspaceViaAPI,
} from './helpers/api'
import { expectAnyVisible, loginViaUI } from './helpers/ui'

const ORG_ADMIN_URL_RE = /\/o\/admin/
const WORKSPACE_URL_RE = /\/workspace\//

/**
 * The four "Not Found" route cases (admin-only page as non-admin, nonexistent
 * org slug, nonexistent workspace ID, workspace under wrong org) are unit-
 * tested at the context layer:
 *
 * - tests/unit/components/AuthGuard.test.tsx — requireAdmin → NotFoundPage
 * - tests/unit/context/OrgContext.test.tsx — notFound when slug missing
 *
 * What only a real session-cookie + share-mode integration can verify is that
 * a non-owner who has been granted access via SHARE_MODE_MEMBERS can open the
 * workspace but does NOT see the new-agent button (a permission-gated UI bit
 * that lives across the auth → org → workspace context chain).
 */
test.describe('Permissions', () => {
  test('non-owner of shared workspace cannot create new agents', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, newuserToken } = leapmuxServer

    // Invite newuser to admin's org (idempotent).
    try {
      await inviteToOrgViaAPI(hubUrl, adminToken, adminOrgId, 'newuser')
    }
    catch { /* already invited */ }

    // Admin creates a workspace and shares it with newuser.
    const sharedWorkspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      'Permissions Shared WS',
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, leapmuxServer.workerId, sharedWorkspaceId)
    const newuserUserId = await getUserId(hubUrl, newuserToken)
    await shareWorkspaceViaAPI(
      hubUrl,
      adminToken,
      sharedWorkspaceId,
      'SHARE_MODE_MEMBERS',
      [newuserUserId],
    )

    // Login as newuser and open admin's shared workspace.
    await loginViaUI(page, 'newuser', 'password123')
    await page.goto('/o/admin')
    await expect(page).toHaveURL(ORG_ADMIN_URL_RE)
    await page.getByText('Permissions Shared WS').click()
    await expect(page).toHaveURL(WORKSPACE_URL_RE)

    // Workspace contents render but the new-agent button is suppressed for
    // a non-owner. (`expectAnyVisible` tolerates either tabs already loaded
    // or the empty state — both are valid for an existing shared workspace.)
    await expectAnyVisible(
      page.locator('[data-testid="tab"]'),
      page.getByText('no open tabs'),
    )
    await expect(page.locator('[data-testid^="new-agent-button"]')).not.toBeVisible()
  })
})
