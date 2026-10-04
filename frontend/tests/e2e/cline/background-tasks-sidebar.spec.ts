import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { backgroundTasksSection, expectRowBecomesFinal, expectSectionPersists, HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset, tabById } from '../helpers/ui'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest('keeps the actual native background task row through completion and reload', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await applyPermissionPreset(page, 'bypass')
  const child = await openHeldChildTab(page, modelScript, { provider: context.provider, childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] })
  try {
    await tabById(page, child.rootTabId).click()
    await expect(backgroundTasksSection(page)).toBeVisible()
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await expect(child.row).toContainText('Count to one hundred')
  }
  finally {
    await child.finish()
  }
  await expectRowBecomesFinal(page, child.row)
  await expectSectionPersists(page)
})
