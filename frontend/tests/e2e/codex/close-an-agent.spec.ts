import { expect } from '@playwright/test'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { agentTabs, expectAgentTabCount, tabById, waitForAgentIdle } from '../helpers/ui'
import { inspectLastTabCloseViaAPI } from '../helpers/workerTabs'

codexTest.describe('codex agent lifecycle', () => {
  codexTest('can close Codex agent tab', async ({ native, page, leapmuxServer }) => {
    const tabsBefore = await agentTabs(page).count()
    expect(tabsBefore).toBeGreaterThan(0)

    await waitForAgentIdle(page)
    const agent = await currentNativeAgent(native)
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
    await expectAgentTabCount(page, tabsBefore - 1)
    await retryUntilPass(async () => {
      expect(await nativeAgentById(native, agent.id), 'the Worker lists the closed agent no more').toBeNull()
    })
  })

  codexTest('closes a busy native tool and its provider processes', async ({ native }) => {
    await exerciseCloseAgent(native)
  })
})
