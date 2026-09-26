import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from './fastagent-fixtures'
import { ARITHMETIC_PROMPT, bandRows, sendMessage, waitForAgentIdle } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

const REASONING = 'I add the two numbers column by column.'

/**
 * 277 — Fast Agent thinking and context usage.
 *
 * fast-agent serves OpenAI Chat Completions and carries the reasoning on
 * `reasoning_content`, which reaches the transcript as a thought band. The
 * usage block of a step reaches the agent info as context usage.
 */
fastAgentTest.describe('Fast Agent thinking and context usage', () => {
  fastAgentTest('draws the reasoning in a thought band', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue({ reasoning: REASONING, text: '6912' })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)

    await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
    // The reasoning stays out of the answer text.
    await expect(bandRows(page, 'text').filter({ hasText: REASONING })).toHaveCount(0)
  })

  fastAgentTest('reports the usage block as context usage', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue({
      text: 'The turn is complete.',
      usage: { inputTokens: 1200, outputTokens: 80, contextWindow: 8000 },
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
