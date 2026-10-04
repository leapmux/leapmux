import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'

droidTest.describe('Factory Droid basic chat', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('draws model reasoning in a thought band', async ({ authenticatedReasoningDroidWorkspace, page, modelScript }) => {
    void authenticatedReasoningDroidWorkspace
    const reasoning = 'DROID_THOUGHT_MARKER I compare the two values.'
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ reasoning, text: 'The answer is 6912.' })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
