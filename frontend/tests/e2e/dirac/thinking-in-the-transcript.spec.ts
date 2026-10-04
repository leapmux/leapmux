import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from '../dirac-fixtures'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { ARITHMETIC_PROMPT, bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'

diracTest.describe('Dirac thinking and context usage', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  const REASONING = 'I add the two numbers column by column.'

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
})
