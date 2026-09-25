import type { Page } from '@playwright/test'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { askUserQuestionToolCall, bashToolCall, exitPlanModeToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, messageBubbles, openWorkspace, sendMessage, userBubbles, visibleOnly, waitForAgentIdle } from './helpers/ui'
import { expect, openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QWEN_CODE

function controlBanner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

qwenTest.describe('Qwen Code control requests', () => {
  // Qwen's own default mode, `default`, asks before a shell command. Its answer is
  // one of the options it sent, and a reason the reader types has no field in
  // that answer, so the reason follows as a message of its own.
  qwenTest('approves one command and rejects the next with a reason', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Default')
    const approved = join(workingDir, 'approved.txt')
    const rejected = join(workingDir, 'rejected.txt')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'qwen-approved', `touch ${approved}`)] },
      { toolCalls: [bashToolCall(PROVIDER, 'qwen-rejected', `touch ${rejected}`)] },
    )
    await sendMessage(page, modelScript.prompt('Create the two scripted files.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText(`touch ${approved}`)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps(2)
    await expect(banner).toContainText(`touch ${rejected}`)
    expect(existsSync(approved)).toBe(true)
    // The reason ends the rejected turn and opens the next one, which answers it.
    await modelScript.queue({ text: 'I will not create the second file.' })
    const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
    await editor.fill('Do not create the second file.')
    await page.keyboard.press('Meta+Enter')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(userBubbles(page).filter({ hasText: 'Do not create the second file.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I will not create the second file.' })).toBeVisible()
    expect(existsSync(rejected)).toBe(false)
  })

  qwenTest('answers a question through its own reply field', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'qwen-question', [{
          question: 'Which color do you want?',
          header: 'Color',
          options: [{ label: 'Red', description: 'The red one' }, { label: 'Blue', description: 'The blue one' }],
        }])],
      },
      { text: 'You chose Blue.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me for a color.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Which color do you want?')
    await page.locator('[data-testid="question-option-Blue"]:visible').click()
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The answer reached Qwen through its own reply field -- its tool result
    // states it -- and the saved answer reads back under the question's header.
    const status = await modelScript.status()
    expect(JSON.stringify(status.requests.at(-1)?.body)).toContain('**Color**: Blue')
    await expect(messageBubbles(page).filter({ hasText: 'Color: Blue' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'You chose Blue.' })).toBeVisible()
  })

  qwenTest('approves a plan and leaves plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'qwen-plan', '# Qwen probe plan\n\n1. Change no files.')] },
      { text: 'Plan approved; starting.' },
    )
    await sendMessage(page, modelScript.prompt('Plan the probe, then ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    // The request carries the plan itself, so the banner draws it.
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Change no files.')
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Plan approved; starting.' })).toBeVisible()
    // Qwen reports the mode it left plan mode for, and the chip follows it.
    await expectSettingsChip(page, 'Default')
  })

  // An approval with a fresh context replaces the session in place. The old
  // turn waits on its plan approval, so the clear answers that approval and
  // cancels the old turn before the new session opens, and the plan that the
  // approval carried runs in the new session. Nothing of the old session
  // reaches the reader after that.
  qwenTest('approves a plan with a fresh context and runs it in the new session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    // The clear answers the old approval `cancelled`, which Qwen reads as "keep
    // planning", and the cancel of the old turn follows it. The old session can
    // ask the model once in between, so its turn count is not this test's to fix.
    await modelScript.fallback({ text: 'Still planning in the old session.' })
    // The new session receives the stored plan, with the marker that the plan
    // carries, so its request reaches this script.
    await modelScript.rule({
      name: 'the approved plan runs in the new session',
      when: { body: 'Execute the following plan' },
      respond: { text: 'Running the approved plan in a fresh context.' },
    })
    await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(PROVIDER, 'qwen-fresh-plan', modelScript.prompt('# Qwen fresh plan\n\n1. Change no files.'))],
    })
    await sendMessage(page, modelScript.prompt('Plan the probe, then ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    const clearContext = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await clearContext.check()
    await expect(clearContext).toBeChecked()
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)

    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'Running the approved plan in a fresh context.' })).toBeVisible()
    await waitForAgentIdle(page, 120_000)
    await expect(banner).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'Still planning in the old session.' })).toHaveCount(0)
  })
})
