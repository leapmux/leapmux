import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, junieAnswerToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, JUNIE_AGENT, junieTest } from '../junie-fixtures'

junieTest.describe('Junie control requests', () => {
  const PROVIDER = AgentProvider.JUNIE

  junieTest('an allowed command runs, and its output reaches the chat', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'off' } })
    const output = join(workingDir, 'junie-allow-out.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'junie-allow', `echo "junie-$((40 + 2))" | tee ${output}`)] },
      { toolCalls: [junieAnswerToolCall('junie-allow-answer', 'The command ran.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the echo command.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toBeVisible()
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(messageBubbles(page).filter({ hasText: 'junie-42' }).first()).toBeVisible()
    expect(readFileSync(output, 'utf8')).toContain('junie-42')
  })

  junieTest('a denied command does not run', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'off' } })
    const output = join(workingDir, 'junie-deny-out.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'junie-deny', `echo "junie-should-not-run" | tee ${output}`)] },
      { toolCalls: [junieAnswerToolCall('junie-deny-answer', 'I did not run it.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the command.'))
    await modelScript.waitForSteps(1)

    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(existsSync(output)).toBe(false)
    const followup = status.requests.find(record => record.stepIndex === 1)
    expect(JSON.stringify(followup?.body)).toContain('Human rejected execution')
  })
})
