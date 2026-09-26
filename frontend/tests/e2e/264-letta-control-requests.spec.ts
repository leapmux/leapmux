import type { Page } from '@playwright/test'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { askUserQuestionToolCall, bashToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

/**
 * 264 — Letta Code control requests.
 *
 * The worker opens the session in Standard mode, so Letta raises a
 * `can_use_tool` control request before a tool the runtime marks as needing
 * approval. The reader's Allow answers with a flat `approval_response` payload
 * and the call runs. A question is a call of Letta's `AskUserQuestion` tool,
 * which reaches the worker through the same channel and draws the shared
 * question banner.
 */
lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.LETTA

/** The control banner on screen. The chat renders each unmeasured row twice. */
function banner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

/** The visible chat text, joined. */
async function chatText(page: Page): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

lettaTest.describe('Letta Code control requests', () => {
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

  lettaTest('answers a question through the shared question banner', async ({ askingLettaWorkspace, page, modelScript }) => {
    void askingLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'ask-1', [
          { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
        ])],
      },
      { text: 'You chose Blue.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me a question.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Which color do you prefer?')
    // A question option is a radio inside a label, not a button: every other
    // provider's control spec clicks the option by its `question-option-*` id.
    await page.locator('[data-testid="question-option-Blue"]:visible').click()
    // A question is answered by its Submit button. Allow/Deny is the permission
    // pair; the question control offers Submit/Stop.
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
  })
})
