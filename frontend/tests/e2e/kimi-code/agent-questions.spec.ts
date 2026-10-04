import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeModelBodiesAfter } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('answers Kimi Code questions', () => {
  kimiTest('a selected answer reaches the model as the tool result', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(KIMI, 'color-question', [{
          question: 'Which color do you prefer?',
          header: 'Color',
          options: [
            { label: 'Red', description: 'A warm color.' },
            { label: 'Blue', description: 'A cool color.' },
          ],
        }])],
      },
      { text: 'Recorded the color.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me which color I prefer.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Which color do you prefer?')
    await expect(banner.getByText('A cool color.', { exact: true })).toBeVisible()
    await banner.getByTestId('question-option-Blue').click()
    await page.getByTestId('control-submit-btn').click()
    await expect(banner).toHaveCount(0)

    // The model's own tool call repeats every option label in each later request,
    // so a bare label is no proof. Kimi Code answers with
    // `{"answers":{"<question>":"<label>"}}` as the tool result TEXT, and the
    // encoded request body escapes each quote of that text.
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const later = nativeModelBodiesAfter(status, 1)
    expect(later).toContain('\\"Which color do you prefer?\\":\\"Blue\\"')
    expect(later).not.toContain('\\"Which color do you prefer?\\":\\"Red\\"')
  })
})
