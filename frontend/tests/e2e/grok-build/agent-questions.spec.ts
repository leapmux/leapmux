import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from '../grok-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build control requests', () => {
  // Grok uses each question's text as its answer key.
  // The reply carries the reader's words beside the selected option. A note does not replace that option.
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
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText('Which database?')
    await page.locator('[data-testid="question-option-Postgres"]:visible').click()
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Use version 16')
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const body = JSON.stringify((await modelScript.status()).requests.at(-1)?.body)
    expect(body).toContain('\\"Which database?\\"=\\"Postgres\\"')
    expect(body).toContain('user notes: Use version 16')
    await expect(messageBubbles(page).filter({ hasText: 'Which database?: Postgres, Use version 16' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'Postgres it is.' })).toBeVisible()
  })
})
