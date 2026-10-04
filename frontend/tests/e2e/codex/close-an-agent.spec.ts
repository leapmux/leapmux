import { expect } from '@playwright/test'
import { AgentActivityState, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { tabById, waitForAgentIdle } from '../helpers/ui'
import { inspectLastTabCloseViaAPI } from '../helpers/worktree'

codexTest.describe('codex agent lifecycle', () => {
  codexTest('can close Codex agent tab', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
    const tabs = page.locator('[data-testid="tab"]')
    const tabsBefore = await tabs.count()
    expect(tabsBefore).toBeGreaterThan(0)

    const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
    await waitForAgentIdle(page)
    const agent = await currentNativeAgent(context)
    expect(agent.activityState).toBe(AgentActivityState.IDLE)
    const inspection = await inspectLastTabCloseViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, TabType.AGENT, agent.id)
    const closeBtn = tabById(page, agent.id).getByTestId('tab-close')
    await expect(closeBtn).toBeVisible()
    await closeBtn.click()
    if (inspection.shouldPrompt) {
      const dialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Close last tab', exact: true }) })
      await expect(dialog).toBeVisible()
      await dialog.getByRole('button', { name: 'Close anyway', exact: true }).click()
      await dialog.getByRole('button', { name: 'Confirm?', exact: true }).click()
    }
    await expect(tabs).toHaveCount(Math.max(tabsBefore - 1, 0))
    await expect.poll(() => nativeAgentById(context, agent.id)).toBeNull()
  })

  codexTest('closes a busy native tool and its provider processes', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
    await exerciseCloseAgent({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId })
  })
})
