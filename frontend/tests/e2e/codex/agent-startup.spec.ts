import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'

codexTest.describe('codex agent lifecycle', () => {
  codexTest('codex agent tab is visible after creation', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const tabs = page.locator('[data-testid="tab"]')
    await expect(tabs.first()).toBeVisible()
  })

  codexTest('can create multiple Codex agents', async ({ authenticatedCodexWorkspace, page, leapmuxServer }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const tabs = page.locator('[data-testid="tab"]')
    const tabsBefore = await tabs.count()

    // Require the Codex button before the click. An absent button or ineffective click must fail this case.
    const newAgentBtn = page.getByTestId(`new-agent-button-${AgentProvider.CODEX}`).filter({ visible: true }).first()
    await expect(newAgentBtn).toBeVisible()
    await newAgentBtn.click()

    // A new tab must appear.
    await expect(tabs).toHaveCount(tabsBefore + 1)
    expect((await currentNativeAgent({ page, leapmuxServer })).agentProvider).toBe(AgentProvider.CODEX)
  })
})

for (const failed of [false, true]) {
  codexTest(`delivers queued input through controlled native startup with failure ${failed}`, async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
    await exerciseAgentStartup({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }, {
      launch: resolveNativeStartupLaunch(leapmuxServer.agentEnv, { binaryName: 'codex', holdWhen: ['app-server'] }),
      failed,
    })
  })
}
