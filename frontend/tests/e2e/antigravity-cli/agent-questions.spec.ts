import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { savedControlAnswer, toolCallRow } from '../helpers/ui'

antigravityTest('answers a native question and resumes the turn', async ({ native }) => {
  const callId = 'color-question'
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [
      {
        question: 'Pick a color.',
        header: 'Color',
        options: [
          { label: 'Blue', description: 'Use blue.' },
          { label: 'Green', description: 'Use green.' },
        ],
      },
    ],
    callId,
    prompt: 'Ask me to pick a color.',
    answer: 'The answer was recorded.',
    reply: chooseQuestionOption('Green', native),
  })
  expect(request.protocol).toBe('google-generative-language')
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
  const requestRow = toolCallRow(native.page, callId, 'request')
  const resultRow = toolCallRow(native.page, callId)
  const expectTranscript = async () => {
    await expect(requestRow.locator('xpath=ancestor::*[@data-span-columns][1]')).toHaveAttribute('data-span-columns', '0')
    await expect(resultRow.locator('xpath=ancestor::*[@data-span-columns][1]')).toHaveAttribute('data-span-columns', '1')
    await expect(resultRow).toHaveAttribute('data-tool-status', 'completed')
    await expect(resultRow).toContainText('Green')
    await expect(resultRow).not.toContainText('Blue')
    await expect(savedControlAnswer(native.page).filter({ hasText: 'Green' })).toBeVisible()
  }
  await expectTranscript()
  await native.page.reload()
  await expectTranscript()
})

antigravityTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

antigravityTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

antigravityTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
