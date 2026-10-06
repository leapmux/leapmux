import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code basic chat', () => {
  codebuddyTest('draws model reasoning in a thought row', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    await modelScript.queue({ reasoning: 'I inspect the numbers first.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the numbers first.' }).first()).toBeVisible()
    await expectAssistantAnswer(page)

    await page.reload()
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the numbers first.' }).first()).toBeVisible()
    await expectAssistantAnswer(page)
  })
})
