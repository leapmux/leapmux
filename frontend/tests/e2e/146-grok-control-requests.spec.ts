import type { Page } from '@playwright/test'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { askUserQuestionToolCall, bashToolCall, exitPlanModeToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, expectSettingsOptionChosen, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.GROK_BUILD

function controlBanner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

grokTest.describe('Grok Build control requests', () => {
  // Grok's `ask` approval mode asks before a shell command that writes a file.
  // It lets `touch` and `mkdir` through on its own, so the commands redirect. A
  // plain rejection ends Grok's turn; a rejection that carries a reason puts the
  // reason in Grok's own `followup_message`, and the SAME turn goes on with it.
  grokTest('approves one command and rejects the next with a reason the turn reads', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
    const approved = join(workingDir, 'approved.txt')
    const rejected = join(workingDir, 'rejected.txt')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'grok-approved', `printf approved > ${approved}`)] },
      { toolCalls: [bashToolCall(PROVIDER, 'grok-rejected', `printf rejected > ${rejected}`)] },
      { text: 'I read the reason and stopped.' },
    )
    await sendMessage(page, modelScript.prompt('Create the two scripted files.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText(`printf approved > ${approved}`)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps(2)
    await expect(banner).toContainText(`printf rejected > ${rejected}`)
    expect(existsSync(approved)).toBe(true)
    const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
    await editor.fill('Do not create the second file.')
    await page.keyboard.press('Meta+Enter')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The reason reached the model inside the same turn, and the saved answer
    // shows it rather than the option's words.
    const status = await modelScript.status()
    expect(JSON.stringify(status.requests.at(-1)?.body)).toContain('Do not create the second file.')
    await expect(messageBubbles(page).filter({ hasText: 'Sent feedback:' }).filter({ hasText: 'Do not create the second file.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I read the reason and stopped.' })).toBeVisible()
    expect(existsSync(rejected)).toBe(false)
  })

  // Grok keys each answer by the question's text and carries the reader's own
  // words beside the chosen option, so a note does not replace the choice.
  grokTest('answers a question with a choice and a note', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'grok-question', [{
          question: 'Which database?',
          header: 'Database',
          options: [{ label: 'Postgres', description: 'Relational' }, { label: 'Redis', description: 'In-memory' }],
        }])],
      },
      { text: 'Postgres it is.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me for a database.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Which database?')
    await page.locator('[data-testid="question-option-Postgres"]:visible').click()
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Use version 16')
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const body = JSON.stringify((await modelScript.status()).requests.at(-1)?.body)
    expect(body).toContain('\\"Which database?\\"=\\"Postgres\\"')
    expect(body).toContain('user notes: Use version 16')
    await expect(messageBubbles(page).filter({ hasText: 'Which database?: Postgres, Use version 16' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'Postgres it is.' })).toBeVisible()
  })

  // Grok's plan approval is a request of its own. Approve takes the shared plan
  // surface, and Grok leaves plan mode itself, which the chip then follows.
  grokTest('approves a plan and leaves plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'grok-plan', '')] },
      { text: 'Plan approved; starting.' },
    )
    await sendMessage(page, modelScript.prompt('Finish planning and ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Plan approved; starting.' })).toBeVisible()
    await expectSettingsChip(page, 'Default')
  })
})
