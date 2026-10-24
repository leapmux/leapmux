import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer, NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { nativeModelToolNames } from '../helpers/nativeScenario'

codebuddyTest('returns the selected native answer in Bypass after reload', async ({ native }) => {
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [NATIVE_CONTROL_QUESTION],
    reply: chooseQuestionOption('Green', native),
  })
  expect(nativeModelToolNames(request)).toContain('AskUserQuestion')
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
})

codebuddyTest('preserves selected answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

codebuddyTest('returns a typed native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

codebuddyTest('withdraws a native question and accepts the next turn in the same session', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})

codebuddyTest('keeps the native question and its draft in the owning tab', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
