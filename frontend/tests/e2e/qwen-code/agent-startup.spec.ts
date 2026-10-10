import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { agentTabs, expectAgentTabCount } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'
import { nativeLaunch } from './scenarios'

qwenTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
})

qwenTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})

qwenTest('the new-agent button opens another agent of this provider', async ({ native, page }) => {
  // The bar's own buttons hold only the two most recently used providers, and a use
  // counts only an open that the UI itself drove. Open one agent of this provider
  // through the tab bar's More options menu first, which records the provider and
  // puts its own button in the bar.
  const tabsBefore = await agentTabs(page).count()
  await page.getByTestId('tab-more-menu').click()
  await page.locator(`menu[popover]:visible [data-testid="menu-new-agent-${AgentProvider.QWEN_CODE}"]`).click()
  await expectAgentTabCount(page, tabsBefore + 1)
  // The menu agent is selected, so this also proves it reaches ACTIVE before its
  // sibling starts: two startups of one provider must not contend.
  await currentNativeAgent(native)

  // The provider's own button is in the bar now. Require it before the click, then
  // require the agent the button opens: one more tab, of this provider, and ACTIVE.
  const newAgentBtn = page.getByTestId(`new-agent-button-${AgentProvider.QWEN_CODE}`).filter({ visible: true }).first()
  await expect(newAgentBtn).toBeVisible()
  await newAgentBtn.click()
  await expectAgentTabCount(page, tabsBefore + 2)
  expect((await currentNativeAgent(native)).agentProvider).toBe(AgentProvider.QWEN_CODE)
})
