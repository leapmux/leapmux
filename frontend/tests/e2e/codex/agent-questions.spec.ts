import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { chooseQuestionOption, chooseQuestionOptionWithNote, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'

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
