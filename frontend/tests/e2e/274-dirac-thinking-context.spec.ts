import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from './dirac-fixtures'
import { diracRespondToolCall } from './helpers/providerToolCalls'
import { ARITHMETIC_PROMPT, bandRows, sendMessage, waitForAgentIdle } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

const REASONING = 'I add the two numbers column by column.'

/**
 * 274 — Dirac thinking and context usage.
 *
 * Dirac runs the deepseek OpenAI-compatible route, which carries the reasoning
 * on `reasoning_content`. The transcript draws it as a thought band, never as
 * answer text. The usage block of a step reaches the agent info as context
 * usage. A turn ends only at `respond complete`.
 */
diracTest.describe('Dirac thinking and context usage', () => {
  diracTest('draws the reasoning in a thought band', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await modelScript.queue({
      reasoning: REASONING,
      toolCalls: [diracRespondToolCall('dirac-think', 'complete', '6912')],
    })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)

    await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
    // The reasoning stays out of the answer text.
    await expect(bandRows(page, 'text').filter({ hasText: REASONING })).toHaveCount(0)
  })

  diracTest('reports the usage block as context usage', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await modelScript.queue({
      usage: { inputTokens: 1200, outputTokens: 80, contextWindow: 8000 },
      toolCalls: [diracRespondToolCall('dirac-usage', 'complete', 'The turn is complete.')],
    })
    await sendMessage(page, modelScript.prompt('Finish the turn.'))
    await waitForAgentIdle(page, 120_000)

    const infoTrigger = page.locator('[data-testid="agent-info-trigger"]')
    await expect(infoTrigger.getByTestId('context-usage-grid')).toBeVisible()
    await infoTrigger.click()
    const popover = page.locator('[data-testid="agent-info-popover"]')
    await expect(popover).toBeVisible()
    await expect(popover.getByText('Context')).toBeVisible()
  })
})
