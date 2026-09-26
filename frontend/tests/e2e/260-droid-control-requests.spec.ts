import type { Page } from '@playwright/test'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import { askUserQuestionToolCall, editToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 260 — Factory Droid control requests.
 *
 * The worker opens the session in Default autonomy, so Droid raises a
 * `droid.request_permission` banner before a tool that changes something. The
 * reader's Allow answers with `proceed_once` and the call runs. A question is a
 * call of Droid's `AskUser` tool, which reaches the worker as `droid.ask_user`
 * and draws the shared question banner.
 */
droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

/** The control banner on screen. The chat renders each unmeasured row twice. */
function banner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

const PROVIDER = AgentProvider.DROID

droidTest.describe('Factory Droid control requests', () => {
  droidTest('runs a command after the reader allows it', async ({ askingDroidWorkspace, page, modelScript }) => {
    void askingDroidWorkspace
    // Droid's `normal` autonomy asks before an `Edit` (a write), and
    // auto-approves `Execute` (a read-ish shell call). Script the call it
    // really asks for.
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'allow-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
      { text: 'The edit landed.' },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Edit')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
  })

  droidTest('keeps the command from running after the reader denies it', async ({ askingDroidWorkspace, page, modelScript }) => {
    void askingDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    // A cancel ends the turn: Droid runs no follow-up model call, so one step
    // is the whole script.
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'deny-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    await modelScript.waitForSteps(1)

    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page, 180_000)

    await expect(banner(page)).toHaveCount(0)
  })

  droidTest('answers a question through the shared question banner', async ({ askingDroidWorkspace, page, modelScript }) => {
    void askingDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
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
    await page.getByTestId('question-option-Blue').filter({ visible: true }).first().click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
  })
})
