import { expect } from '@playwright/test'
import { claudeProcessTest as test } from '../claude-fixtures'
import { countOriginalAnswerRows, expectNativeResumeContext, expectResumedConversation, nativeResumeTexts } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, composerEditor, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { restartWorker, stopWorker } from '../process-control-fixtures'
import { expectAnswerAndTurnEnd, waitForWorkerConnection, withRestartWorkspace } from './workerRestart'

test.describe('Agent Session Resume', () => {
  test('should resume agent session after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    const texts = nativeResumeTexts()
    const priorAnswerMarker = texts.originalAnswer
    const firstPrompt = `${ARITHMETIC_PROMPT} ${texts.originalPrompt}`
    const secondPrompt = `${SECOND_ARITHMETIC_PROMPT} ${texts.resumedPrompt}`
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Resume Test' }, async ({ agentId }) => {
      const server = { leapmuxServer: separateHubWorker }

      // Send a message and wait for response
      const firstStep = await modelScript.queue({ text: `${ARITHMETIC_ANSWER_TEXT} ${priorAnswerMarker}` })
      await sendMessage(page, modelScript.prompt(firstPrompt))
      await modelScript.waitForSteps(firstStep + 1)
      expect(JSON.stringify((await modelScript.requestAt(firstStep)).body)).not.toContain(priorAnswerMarker)

      // Wait for the assistant's response
      await expectAnswerAndTurnEnd(page)
      const originalAnswerRows = await countOriginalAnswerRows(server, agentId, texts)

      // Stop the worker
      await stopWorker(separateHubWorker)

      // Wait until the browser observes the closed worker channel. The editor
      // stays visible while offline, so it cannot prove this state change.
      await waitForWorkerConnection(page, false)

      // The editor should still be enabled (agent has session ID so it's resumable)
      await expect(composerEditor(page)).toBeVisible()

      // Restart the worker
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Send a new message to the closed (but resumable) agent
      const resumedStep = await modelScript.queue({ text: `${SECOND_ARITHMETIC_ANSWER_TEXT} ${texts.resumedAnswer}` })
      await sendMessage(page, modelScript.prompt(secondPrompt))
      await modelScript.waitForSteps(resumedStep + 1)
      const nextRequest = await modelScript.requestAt(resumedStep)
      const nextBody = JSON.stringify(nextRequest.body)
      expect(nextBody).toContain(priorAnswerMarker)
      expect(nextBody).toContain(SECOND_ARITHMETIC_PROMPT)
      expectNativeResumeContext(nativeModelConversationTurns(nextRequest), texts)

      // Wait for the answer of the resumed turn. The answer "3333" does not
      // occur in the first answer "6912", so this check cannot match the
      // earlier bubble.
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
      await waitForAgentIdle(page)
      await expectResumedConversation({ page, ...server }, agentId, texts, originalAnswerRows)
    })
  })
})
