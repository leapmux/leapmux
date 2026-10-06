import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code basic chat', () => {
  codebuddyTest('draws the answer and ends the turn', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()

    // The Worker stores each streamed row. Reload restores the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
  })
})

codebuddyTest('ends the actual native turn and keeps its answer after reload', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
