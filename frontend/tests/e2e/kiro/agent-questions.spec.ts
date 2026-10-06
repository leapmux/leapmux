import { expect } from '@playwright/test'

import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroUserText } from '../helpers/kiroSurface'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlBanner, expectSettingsChip, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { openProviderAgent } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'

const PROVIDER = AgentProvider.KIRO

kiroTest.describe('Kiro control requests', () => {
  // Kiro offers its question tool in a spec mode alone, and a spec mode first
  // classifies the prompt, which the housekeeping rules answer. The test chooses
  // the SECOND option, so an answer that Kiro never read cannot pass as the first.
  kiroTest('answers a question with a chosen option', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'spec' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Spec')

    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'kiro-question', [{
          question: 'Which database?',
          header: 'Database',
          options: [{ label: 'Postgres', description: 'Relational' }, { label: 'SQLite', description: 'Embedded' }],
        }])],
      },
      { text: 'SQLite it is.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me for a database.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Which database?')
    await page.locator('[data-testid="question-option-SQLite"]:visible').click()
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The call after the question carries the answer as the result of the question
    // tool. The history of the call also lists both options, so the test reads the
    // current message alone.
    const afterAnswer = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    const answered = kiroUserText(afterAnswer?.body)
    expect(answered).toContain('SQLite')
    expect(answered).not.toContain('Postgres')
    await expect(assistantBubbles(page).filter({ hasText: 'SQLite it is.' })).toBeVisible()
  })
})
