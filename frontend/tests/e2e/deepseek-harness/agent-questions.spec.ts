import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { savedControlAnswer } from '../helpers/ui'
import { deepseekHarnessToolResultText } from './nativeToolResultText'

deepseekHarnessTest('returns the exact chosen native answer and keeps the saved answer after reload', async ({ native }) => {
  const { result } = await exerciseQuestionAnswer(native, {
    questions: [{ question: 'Choose a route', header: 'Route', options: [{ label: 'First', description: 'Use the first route.' }, { label: 'Second', description: 'Use the second route.' }] }],
    callId: 'native-route-question',
    prompt: 'Ask the scripted native route question.',
    answer: 'The native question completed.',
    reply: chooseQuestionOption('Second'),
    // DeepSeek Harness returns the answer as one native text block.
    readResult: deepseekHarnessToolResultText,
  })
  expect(JSON.parse(result)).toEqual({ answers: [{ id: 'question-1', selected: ['Second'] }] })
  await expect(savedControlAnswer(native.page)).toHaveText('Choose a route: Second')
  await native.page.reload()
  await expect(savedControlAnswer(native.page)).toHaveText('Choose a route: Second')
})
