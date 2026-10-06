import { droidTest, expect } from '../droid-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, messageContents, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

droidTest.describe('Factory Droid basic chat', () => {
  droidTest('draws the answer and ends the turn', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectAssistantAnswer(page)
    await expect(userBubbles(page).filter({ hasText: '1234 + 5678' }).first()).toBeVisible()

    // The native model request contains the user prompt.
    // The title rule handles housekeeping without consuming a queued step.
    const request = status.requests.find(record => record.stepIndex === 0)
    expect(JSON.stringify(request?.body)).toContain('1234 + 5678')

    // The turn end closes the turn.
    await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended/)

    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)

    // The Worker stores each streamed row. Reload restores the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
  })
})

droidTest('ends the actual native turn and keeps its answer after reload', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
