import type { Page } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { messageContents, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code control requests', () => {
  const PROVIDER = AgentProvider.LETTA

  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  async function chatText(page: Page): Promise<string> {
    return (await messageContents(page).allTextContents()).join(' ')
  }

  lettaTest('runs a command after the reader allows it', async ({ askingLettaWorkspace, page, modelScript }) => {
    void askingLettaWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'allow-call', 'echo "letta-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Bash')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner(page)).toHaveCount(0)
    // The saved row reads Letta's own approval_response decision, not a generic answer word.
    await expect(savedControlAnswer(page)).toHaveText('Allow')
    await expect.poll(() => chatText(page)).toContain('letta-42')
  })

  lettaTest('keeps the command from running after the reader denies it', async ({ askingLettaWorkspace, page, modelScript }) => {
    void askingLettaWorkspace
    await modelScript.queue(
      // `tee` writes a file, so Letta does not treat the call as a READ-ONLY
      // shell command: those auto-approve in every mode but `strict` and no
      // banner would ever appear. If the call wrongly runs, its stdout is the
      // same marker text, so the assertion below still catches it.
      { toolCalls: [bashToolCall(PROVIDER, 'deny-call', 'echo "letta-should-not-run" | tee letta-deny-out.txt')] },
      { text: 'I did not run it.' },
    )
    await sendMessage(page, modelScript.prompt('Run the command.'))
    await modelScript.waitForSteps(1)

    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The command never reached the shell, so its output is nowhere on the page.
    await expect.poll(() => chatText(page)).not.toContain('letta-should-not-run')
    await expect(banner(page)).toHaveCount(0)
    // A denial with no reason keeps the decision word alone.
    await expect(savedControlAnswer(page)).toHaveText('Deny')
  })
})
