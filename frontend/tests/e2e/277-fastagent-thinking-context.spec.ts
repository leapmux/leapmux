import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, openFastAgentAgent } from './fastagent-fixtures'
import { expectContextUsage } from './helpers/contextUsage'
import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import { ARITHMETIC_PROMPT, bandRows, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

const REASONING = 'I add the two numbers column by column.'

/**
 * 277 — Fast Agent thinking and context usage.
 *
 * The isolated ZAI model carries `reasoning_content` through Chat Completions.
 * Fast Agent emits that content as ACP thought chunks. Its native metrics
 * status line carries usage to Agent info.
 */
fastAgentTest.describe('Fast Agent thinking and context usage', () => {
  fastAgentTest('draws the reasoning in a thought band', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { model: MOCK_MODELS.zai })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.queue({ reasoning: REASONING, text: '6912' })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ model: MOCK_MODELS.zai })
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

    await expect(page.locator('[data-testid="agent-info-trigger"]').getByTestId('context-usage-grid')).toBeVisible()
    await expectContextUsage(page, { inputTokens: 1200, outputTokens: 80 })
  })
})
