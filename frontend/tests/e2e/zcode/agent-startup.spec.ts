import { existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { findBinary, requireBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { hubSpawnEnv } from '../helpers/server'
import { agentTabs, expectAgentTabCount } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { zcodeScriptCandidatePaths } from '../zcode-install'

/**
 * The launch that the wrapper holds: the app server.
 *
 * The Worker's ZCode start first runs the same binary as
 * `app-server --stdio --prepare-storage` to locate ZCode's session store
 * (`newZCodeStorageQuery` in providers/zcode/session_store.go), and only then
 * starts the app server. The wrapper accepts one handshake. A held storage query
 * would take it, and the wrapper would then refuse the real app server, which
 * exits with 125. So the storage query runs at once.
 */
const APP_SERVER_LAUNCH = { holdWhen: ['app-server', '--stdio'], passThroughWhen: ['--prepare-storage'] }

zcodeTest.describe('zcode agent startup', () => {
  for (const failed of [false, true]) {
    zcodeTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native, leapmuxServer }) => {
      // Find each executable on the PATH that the Worker receives, because the Worker starts the executable that it finds there.
      const spawnEnvironment = hubSpawnEnv(leapmuxServer.agentEnv)
      const launcher = findBinary('zcode', spawnEnvironment)
      if (launcher && !process.env.LEAPMUX_ZCODE_SCRIPT) {
        await exerciseAgentStartup(native, {
          launch: { binaryName: 'zcode', executable: launcher, ...APP_SERVER_LAUNCH },
          failed,
          workerEnvironment: () => ({ LEAPMUX_ZCODE_SCRIPT: '', LEAPMUX_ZCODE_NODE: '' }),
        })
        return
      }
      const script = process.env.LEAPMUX_ZCODE_SCRIPT
        ?? zcodeScriptCandidatePaths(process.platform, homedir(), process.env).find(existsSync)
      if (!script || !existsSync(script))
        throw new Error('The controlled ZCode startup requires its actual script.')
      const executable = requireBinary('node', 'The controlled ZCode startup requires the Node interpreter', spawnEnvironment)
      await exerciseAgentStartup(native, {
        launch: { binaryName: 'node', executable, ...APP_SERVER_LAUNCH },
        failed,
        workerEnvironment: wrapper => ({ LEAPMUX_ZCODE_SCRIPT: script, LEAPMUX_ZCODE_NODE: join(wrapper.directory, process.platform === 'win32' ? 'node.cmd' : 'node') }),
      })
    })
  }
})

zcodeTest('the new-agent button opens another agent of this provider', async ({ native, page }) => {
  // The bar's own buttons hold only the two most recently used providers, and a use
  // counts only an open that the UI itself drove. Open one agent of this provider
  // through the tab bar's More options menu first, which records the provider and
  // puts its own button in the bar.
  const tabsBefore = await agentTabs(page).count()
  await page.getByTestId('tab-more-menu').click()
  await page.locator(`menu[popover]:visible [data-testid="menu-new-agent-${AgentProvider.ZCODE}"]`).click()
  await expectAgentTabCount(page, tabsBefore + 1)
  // The menu agent is selected, so this also proves it reaches ACTIVE before its
  // sibling starts: two startups of one provider must not contend.
  await currentNativeAgent(native)

  // The provider's own button is in the bar now. Require it before the click, then
  // require the agent the button opens: one more tab, of this provider, and ACTIVE.
  const newAgentBtn = page.getByTestId(`new-agent-button-${AgentProvider.ZCODE}`).filter({ visible: true }).first()
  await expect(newAgentBtn).toBeVisible()
  await newAgentBtn.click()
  await expectAgentTabCount(page, tabsBefore + 2)
  expect((await currentNativeAgent(native)).agentProvider).toBe(AgentProvider.ZCODE)
})
