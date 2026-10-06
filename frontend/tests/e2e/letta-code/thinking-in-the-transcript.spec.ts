import { bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code basic chat', () => {
  lettaTest('draws model reasoning in a thought band', async ({ authenticatedReasoningLettaWorkspace, page, modelScript }) => {
    void authenticatedReasoningLettaWorkspace
    const reasoning = 'LETTA_THOUGHT_MARKER I compare the two values.'
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({ reasoning, text: 'The answer is 6912.' })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
