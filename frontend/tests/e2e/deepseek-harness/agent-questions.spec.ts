import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { chooseQuestionOption, chooseQuestionOptionWithNote, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { savedControlAnswer } from '../helpers/ui'
import { deepseekHarnessToolResultText } from './nativeToolResultText'

for (const note of [undefined, 'Keep the second route.']) {
  deepseekHarnessTest(note === undefined ? 'returns the exact chosen native answer and keeps the saved answer after reload' : 'returns the selected native answer with its restored note', async ({ native }) => {
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [{ question: 'Choose a route', header: 'Route', options: [{ label: 'First', description: 'Use the first route.' }, { label: 'Second', description: 'Use the second route.' }] }],
      callId: 'native-route-question',
      prompt: 'Ask the scripted native route question.',
      answer: 'The native question completed.',
      reply: note === undefined ? chooseQuestionOption('Second', native) : chooseQuestionOptionWithNote(native, 'Second', note),
      // DeepSeek Harness returns the answer as one native text block.
      readResult: deepseekHarnessToolResultText,
    })
    expect(JSON.parse(result)).toEqual({ answers: [{ id: 'question-1', selected: ['Second'], ...(note === undefined ? {} : { custom: note }) }] })
    await expect(savedControlAnswer(native.page)).toHaveText(`Choose a route: Second${note === undefined ? '' : `, ${note}`}`)
    await native.page.reload()
    await expect(savedControlAnswer(native.page)).toHaveText(`Choose a route: Second${note === undefined ? '' : `, ${note}`}`)
  })
}

deepseekHarnessTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native, { readResult: deepseekHarnessToolResultText })
})

deepseekHarnessTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native, { readResult: deepseekHarnessToolResultText })
})

deepseekHarnessTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
