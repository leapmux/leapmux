import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatText, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The test answers real native permission requests. Allow executes the tool. Deny must reach the next native model request as a refusal.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Oh My Pi sends an `extension_ui_request` select dialog before execution. Approve and Deny become the shared Allow and Deny controls.
 */
ohMyPiTest.describe('Oh My Pi control requests', () => {
  ohMyPiTest('runs a command after the reader allows it', async ({ approvingOhMyPiWorkspace, page, modelScript }) => {
    void approvingOhMyPiWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'approve-call', 'echo "omp-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    // omp asks before the call runs, so the second step waits on the banner.
    await modelScript.waitForSteps(1)

    // The banner states the command that omp asks about.
    await expect(visibleControlBanner(page)).toContainText('echo "omp-$((40 + 2))"')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(visibleControlBanner(page)).toHaveCount(0)
    // The command text states no `omp-42`, so only the command's own output can
    // put it on the page.
    await expect.poll(() => chatText(page)).toContain('omp-42')
  })

  ohMyPiTest('refuses a command that the reader denies', async ({ approvingOhMyPiWorkspace, page, modelScript }) => {
    void approvingOhMyPiWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'deny-call', 'echo "omp-$((50 + 5))"')] },
      { text: 'The command was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Run the other arithmetic command.'))
    await modelScript.waitForSteps(1)

    await expect(visibleControlBanner(page)).toContainText('echo "omp-$((50 + 5))"')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(visibleControlBanner(page)).toHaveCount(0)
    // omp fails the call with its own words, and the command never runs.
    await expect.poll(() => chatText(page)).toContain('Tool call denied by user')
    expect(await chatText(page)).not.toContain('omp-55')
  })
})
