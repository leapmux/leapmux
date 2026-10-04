import type { Page } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code control requests', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.LETTA

  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  async function chatText(page: Page): Promise<string> {
    return (await messageContents(page).allTextContents()).join(' ')
  }

  lettaTest('runs a command after the reader allows it', async ({ askingLettaWorkspace, page, modelScript }) => {
    void askingLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
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
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('letta-42')
  })

  lettaTest('keeps the command from running after the reader denies it', async ({ askingLettaWorkspace, page, modelScript }) => {
    void askingLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
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
    await waitForAgentIdle(page, 180_000)

    // The command never reached the shell, so its output is nowhere on the page.
    await expect.poll(() => chatText(page)).not.toContain('letta-should-not-run')
    await expect(banner(page)).toHaveCount(0)
  })
})
