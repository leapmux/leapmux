import { Buffer } from 'node:buffer'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { CODEX_ASYNC_QUESTION_MODEL } from '../helpers/mockAgentEnvironment'
import { expectTurnEndedAfter } from '../helpers/modelScriptFixture'
import { chooseQuestionOption, chooseQuestionOptionWithNote, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { nativeModelLastUserText } from '../helpers/nativeScenario'
import { codexAsyncQuestionToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, composerEditor, controlButton, expectNoControlBanner, openPendingQuestions, pendingQuestionsBanner, queuePauseButton, resumePausedQueue, sendMessage, userBubbles, visibleOnly, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

codexTest.describe('agent questions for Codex', () => {
  for (const note of [undefined, 'Use a darker red.']) {
    codexTest(note === undefined ? 'returns the selected answer to the native question tool' : 'returns the selected answer and its typed note after reload', async ({ native }) => {
      await chooseSettingsOption(native.page, 'collaboration_mode-plan')
      await waitForSettingsIdle(native.page)
      const { result, request } = await exerciseQuestionAnswer(native, {
        questions: [{
          header: 'Color',
          question: 'Which color should I use?',
          options: [
            { label: 'Blue (Recommended)', description: 'Use blue.' },
            { label: 'Red', description: 'Use red.' },
          ],
        }],
        callId: 'codex-color-question',
        prompt: 'Ask me which color to use.',
        answer: 'The answer was recorded.',
        reply: note === undefined ? chooseQuestionOption('Red', native) : chooseQuestionOptionWithNote(native, 'Red', note),
      })
      expect(request.protocol).toBe('openai-responses')
      expect(result).toContain('Red')
      expect(result).not.toContain('Blue (Recommended)')
      if (note !== undefined)
        expect(result).toContain(note)
    })
  }
})

codexTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await chooseSettingsOption(native.page, 'collaboration_mode-plan')
  await waitForSettingsIdle(native.page)
  await exerciseQuestionPages(native, { multiple: false })
})

codexTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await chooseSettingsOption(native.page, 'collaboration_mode-plan')
  await waitForSettingsIdle(native.page)
  await exerciseTypedQuestionAnswer(native)
})

codexTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await chooseSettingsOption(native.page, 'collaboration_mode-plan')
  await waitForSettingsIdle(native.page)
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})

const ASYNC_QUESTION_TITLE = 'Which color should I use?'
const ASYNC_QUESTION_CALL_ID = 'codex-async-color'
const ASYNC_QUESTION_REPLY_TAG = 'send_user_message_question_reply'

/**
 * Async questions do not block the Codex turn. Each question becomes a control request.
 * The composer stays available. An answer uses the native Codex reply format.
 * Each test selects a model that offers `request_user_input_async`.
 */
codexTest.describe('async agent questions for Codex', () => {
  codexTest('preserves the full question beside its answer after reload', async ({ native }) => {
    const title = `${ASYNC_QUESTION_TITLE}\n${'Detailed color choice. '.repeat(40)}\nWhich shade should I use?`
    await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
    await waitForSettingsIdle(native.page)
    const start = await native.modelScript.queue(
      { toolCalls: [codexAsyncQuestionToolCall(ASYNC_QUESTION_CALL_ID, [{ title, options: ['Blue', 'Red'] }])] },
      { text: 'I asked about the color.' },
      { text: 'Recorded.' },
    )
    await sendMessage(native.page, native.modelScript.prompt('Ask me which color to use.'))
    await native.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(native.page)

    await expectNoControlBanner(native.page)
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')
    const opened = await openPendingQuestions(native.page)
    await expect(opened).toContainText(title)
    await chooseQuestionOption('Red')(opened)
    await expectNoControlBanner(native.page)

    await native.modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(native.page)
    const request = await native.modelScript.requestAt(start + 2)
    const userText = nativeModelLastUserText(request)
    expect(userText).toContain(`<${ASYNC_QUESTION_REPLY_TAG}>`)
    expect(userText).toContain('"answer":"Red"')
    expect(userText).toContain(`"questionItemId":"[\\"request_user_input_async\\",\\"${ASYNC_QUESTION_CALL_ID}\\",0]"`)
    await expect(userBubbles(native.page).filter({ hasText: ASYNC_QUESTION_TITLE }).first()).toBeVisible()
    const answer = () => userBubbles(native.page).filter({ hasText: 'Which shade should I use?' }).first()
    await expect(answer()).toContainText('Detailed color choice. '.repeat(40))
    await expect(answer()).toContainText('Red')
    await native.page.reload()
    await expect(answer()).toContainText('Detailed color choice. '.repeat(40))
    await expect(answer()).toContainText('Red')
  })

  codexTest('steers an answer into the running turn', async ({ native }) => {
    await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
    await waitForSettingsIdle(native.page)
    const start = await native.modelScript.queue(
      { toolCalls: [codexAsyncQuestionToolCall(ASYNC_QUESTION_CALL_ID, [{ title: ASYNC_QUESTION_TITLE, options: ['Blue', 'Red'] }])] },
      { text: 'Still working on it.', gate: 'async-turn-holds' },
      { text: 'Recorded.' },
    )
    await sendMessage(native.page, native.modelScript.prompt('Ask me which color to use.'))
    await native.modelScript.waitForGate('async-turn-holds')

    // The question waits while the turn keeps working.
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')
    await expectNoControlBanner(native.page)
    await expect(native.page.locator('[data-testid="thinking-indicator"]:visible').first()).toBeVisible()

    const opened = await openPendingQuestions(native.page)
    await chooseQuestionOption('Red')(opened)

    // The answer steered into the running turn, so nothing waits in the strip.
    await expect(visibleOnly(native.page.getByTestId(/queued-input-/))).toHaveCount(0)
    await native.modelScript.releaseGate('async-turn-holds')
    await native.modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(native.page)
    const request = await native.modelScript.requestAt(start + 2)
    expect(nativeModelLastUserText(request)).toContain(`<${ASYNC_QUESTION_REPLY_TAG}>`)
  })

  codexTest('skips a question without another model request', async ({ native }) => {
    await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
    await waitForSettingsIdle(native.page)
    const start = await native.modelScript.queue(
      { toolCalls: [codexAsyncQuestionToolCall(ASYNC_QUESTION_CALL_ID, [{ title: ASYNC_QUESTION_TITLE, options: ['Blue', 'Red'] }])] },
      { text: 'I asked about the color.' },
    )
    await sendMessage(native.page, native.modelScript.prompt('Ask me which color to use.'))
    await native.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(native.page)

    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')
    const opened = await openPendingQuestions(native.page)
    await expect(opened).toContainText(ASYNC_QUESTION_TITLE)
    await controlButton(native.page, 'skip').click()
    await expectNoControlBanner(native.page)
    await expect(pendingQuestionsBanner(native.page)).toHaveCount(0)
    await expectTurnEndedAfter(native.modelScript, start + 2)
    await expect(visibleOnly(native.page.getByText(`Skipped: ${ASYNC_QUESTION_TITLE}`)).first()).toBeVisible()
  })

  codexTest('keeps an async question across a reload', async ({ native }) => {
    await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
    await waitForSettingsIdle(native.page)
    const start = await native.modelScript.queue(
      { toolCalls: [codexAsyncQuestionToolCall(ASYNC_QUESTION_CALL_ID, [{ title: ASYNC_QUESTION_TITLE, options: ['Blue', 'Red'] }])] },
      { text: 'I asked about the color.' },
    )
    await sendMessage(native.page, native.modelScript.prompt('Ask me which color to use.'))
    await native.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(native.page)
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')

    await native.page.reload()
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')
    const opened = await openPendingQuestions(native.page)
    await expect(opened).toContainText(ASYNC_QUESTION_TITLE)
    native.modelScript.allowUnconsumed('the question stays unanswered at the end of the test')
  })

  codexTest('answers a question later and keeps the saved draft', async ({ native }) => {
    await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
    await waitForSettingsIdle(native.page)
    const start = await native.modelScript.queue(
      { toolCalls: [codexAsyncQuestionToolCall(ASYNC_QUESTION_CALL_ID, [{ title: ASYNC_QUESTION_TITLE, options: ['Blue', 'Red'] }])] },
      { text: 'I asked about the color.' },
    )
    await sendMessage(native.page, native.modelScript.prompt('Ask me which color to use.'))
    await native.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(native.page)
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')

    await composerEditor(native.page).fill('Answer the color question in a moment.')
    const opened = await openPendingQuestions(native.page)
    await opened.getByTestId('control-answer-later').click()
    await expectNoControlBanner(native.page)
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')
    await expect(composerEditor(native.page)).toHaveText('Answer the color question in a moment.')
    native.modelScript.allowUnconsumed('the question stays unanswered at the end of the test')
  })

  codexTest('keeps the composer free while a question waits', async ({ native }) => {
    await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
    await waitForSettingsIdle(native.page)
    const start = await native.modelScript.queue(
      { toolCalls: [codexAsyncQuestionToolCall(ASYNC_QUESTION_CALL_ID, [{ title: ASYNC_QUESTION_TITLE, options: ['Blue', 'Red'] }])] },
      { text: 'I asked about the color.' },
      { text: 'Reply noted.' },
    )
    await sendMessage(native.page, native.modelScript.prompt('Ask me which color to use.'))
    await native.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(native.page)
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')

    await sendMessage(native.page, native.modelScript.prompt('A plain message beside the question.'))
    await native.modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(native.page)
    await expect(pendingQuestionsBanner(native.page)).toContainText('The agent asked a question.')
    await expectNoControlBanner(native.page)
    native.modelScript.allowUnconsumed('the question stays unanswered at the end of the test')
  })
})

codexTest('keeps two active-turn answers paused until the queue resumes', async ({ native }) => {
  await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
  await waitForSettingsIdle(native.page)
  const start = await native.modelScript.queue(
    { toolCalls: [codexAsyncQuestionToolCall('paused-questions', [
      { title: 'Which color?', options: ['Red', 'Blue'] },
      { title: 'Which shape?', options: ['Round', 'Square'] },
    ])] },
    { text: 'The turn continues.', gate: 'paused-question-turn' },
    { text: 'Both answers were recorded.' },
  )
  await sendMessage(native.page, native.modelScript.prompt('Ask about color and shape.'))
  await native.modelScript.waitForGate('paused-question-turn')
  await queuePauseButton(native.page).click()
  await expect(queuePauseButton(native.page)).toHaveText('Resume Queue')
  await chooseQuestionOption('Red')(await openPendingQuestions(native.page))
  await chooseQuestionOption('Round')(await waitForControlBanner(native.page))
  const queued = visibleOnly(native.page.getByTestId(/^queued-input-/))
  await expect(queued).toHaveCount(2)
  await expect(queued.filter({ hasText: 'Red' })).toBeVisible()
  await expect(queued.filter({ hasText: 'Round' })).toBeVisible()
  await resumePausedQueue(native.page)
  await expect(queued).toHaveCount(0)
  await native.modelScript.releaseGate('paused-question-turn')
  await native.modelScript.waitForSteps(start + 3)
  await waitForAgentIdle(native.page)
  const request = await native.modelScript.requestAt(start + 2)
  expect(JSON.stringify(request.body)).toContain('Red')
  expect(JSON.stringify(request.body)).toContain('Round')
})

codexTest('rejects a file selected after a question opens', async ({ native }) => {
  await chooseSettingsOption(native.page, `model-${CODEX_ASYNC_QUESTION_MODEL}`)
  await waitForSettingsIdle(native.page)
  const start = await native.modelScript.queue(
    { toolCalls: [codexAsyncQuestionToolCall('picker-question', [{ title: 'Which color?', options: ['Red', 'Blue'] }])] },
    { text: 'The question can wait.' },
  )
  await sendMessage(native.page, native.modelScript.prompt('Ask about the color.'))
  await native.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(native.page)
  await expect(pendingQuestionsBanner(native.page)).toBeVisible()
  await native.page.getByTestId('composer-plus-trigger').click()
  const chooserPromise = native.page.waitForEvent('filechooser')
  await native.page.getByTestId('composer-attach-file').click()
  const chooser = await chooserPromise
  const opened = await openPendingQuestions(native.page)
  const fileInput = native.page.getByTestId('file-input')
  await expect(fileInput).toBeDisabled()
  const file = { name: 'late-choice.txt', mimeType: 'text/plain', buffer: Buffer.from('A late selection.') }
  await chooser.setFiles(file)
  await expect(visibleOnly(native.page.getByTestId('attachment-pill'))).toHaveCount(0)
  await opened.getByTestId('control-answer-later').click()
  await expect(fileInput).toBeEnabled()
  await expect(visibleOnly(native.page.getByTestId('attachment-pill'))).toHaveCount(0)
  await fileInput.setInputFiles(file)
  await expect(visibleOnly(native.page.getByTestId('attachment-pill'))).toContainText('late-choice.txt')
  await visibleOnly(native.page.getByTestId('attachment-remove')).click()
  native.modelScript.allowUnconsumed('the question remains unanswered')
})
