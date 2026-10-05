import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI basic chat', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('draws the answer and ends the turn', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
    await expect(visibleOnly(page.getByText('LeapMux has no display for this row', { exact: true }))).toHaveCount(0)

    // The Worker stores each streamed row. Reload restores the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
    await expect(visibleOnly(page.getByText('LeapMux has no display for this row', { exact: true }))).toHaveCount(0)
  })
})

qoderTest('ends the actual native turn and keeps its answer after reload', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
