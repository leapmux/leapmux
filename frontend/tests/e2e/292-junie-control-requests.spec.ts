import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, junieAnswerToolCall } from './helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.JUNIE

/** Housekeeping turns every Junie task answers before the main agent runs. */
function junieHousekeeping() {
  return [
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Command task' } },
  ]
}

junieTest.describe('Junie control requests', () => {
  // `brave_mode: off` makes Junie ask before every shell command instead of
  // auto-approving the safe ones. The banner is a permission request; Allow runs
  // the command and its output reaches the chat.
  junieTest('an allowed command runs, and its output reaches the chat', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'off' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'junie-allow', 'echo "junie-$((40 + 2))"')] },
      { toolCalls: [junieAnswerToolCall('junie-allow-answer', 'The command ran.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the echo command.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toBeVisible()
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(messageBubbles(page).filter({ hasText: 'junie-42' }).first()).toBeVisible()
  })

  // A denied command never runs, so its output is nowhere on the page.
  junieTest('a denied command does not run', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'off' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.queue(
      // `tee` writes a file so the call is not a read-only echo Junie might
      // auto-approve even in `off`.
      { toolCalls: [bashToolCall(PROVIDER, 'junie-deny', 'echo "junie-should-not-run" | tee junie-deny-out.txt')] },
      { toolCalls: [junieAnswerToolCall('junie-deny-answer', 'I did not run it.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the command.'))
    await modelScript.waitForSteps(1)

    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(messageBubbles(page).filter({ hasText: 'junie-should-not-run' })).toHaveCount(0)
  })
})
