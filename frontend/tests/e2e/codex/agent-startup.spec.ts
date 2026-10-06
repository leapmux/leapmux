import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { agentTabs, expectAgentTabCount } from '../helpers/ui'
import { nativeLaunch } from './scenarios'

codexTest.describe('codex agent lifecycle', () => {
  codexTest('codex agent tab is visible after creation', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await expect(agentTabs(page).first()).toBeVisible()
  })

  codexTest('can create multiple Codex agents', async ({ native, page }) => {
    const tabsBefore = await agentTabs(page).count()

    // Require the Codex button before the click. An absent button or ineffective click must fail this case.
    const newAgentBtn = page.getByTestId(`new-agent-button-${AgentProvider.CODEX}`).filter({ visible: true }).first()
    await expect(newAgentBtn).toBeVisible()
    await newAgentBtn.click()

    // A new tab must appear.
    await expectAgentTabCount(page, tabsBefore + 1)
    expect((await currentNativeAgent(native)).agentProvider).toBe(AgentProvider.CODEX)
  })
})

for (const failed of [false, true]) {
  codexTest(`delivers queued input through controlled native startup with failure ${failed}`, async ({ native }) => {
    await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed })
  })
}
