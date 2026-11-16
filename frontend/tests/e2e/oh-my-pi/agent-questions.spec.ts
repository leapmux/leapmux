import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseQuestionPreviews, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { restoreQuestionEmptySelection } from '../helpers/nativeQuestionRestoration'
import { nativeTextStep, nativeToolOutcome } from '../helpers/nativeScenario'
import { ohMyPiAskFormToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlButton, expectNoControlBanner, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * A real native question tool opens the shared question controls. The selected answer must reach the native model.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Oh My Pi's ask tool sends a series of native dialogs. The Worker combines them into one question request.
 */
ohMyPiTest.describe('control requests for Oh My Pi', () => {
  ohMyPiTest('delivers the answer to a question', async ({ native }) => {
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [
        {
          question: 'Choose a style',
          header: 'Style',
          options: [
            { label: 'Alpha', description: 'Use the first style.' },
            { label: 'Beta', description: 'Use the second style.' },
          ],
        },
      ],
      callId: 'style-question',
      prompt: 'Ask me which style to use.',
      answer: 'Recorded the style.',
      reply: async (banner) => {
        await expect(banner.getByText('Use the second style.', { exact: true })).toBeVisible()
        await chooseQuestionOption('Beta', native)(banner)
      },
    })
    // The dialog reply reaches Oh My Pi, which sends the selected label to the model as the result of the question
    // call. The request also contains the call arguments, which include every option, so only the result proves the
    // selected answer.
    expect(result).toContain('Beta')
    expect(result).not.toContain('Alpha')
    // The saved answer states the question and the chosen label, and exists only
    // after the answer. The question's own row lists every option before any
    // answer, so it cannot prove which one the reader chose.
    await expect(savedControlAnswer(native.page)).toHaveText('Choose a style: Beta')
  })
})

ohMyPiTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

ohMyPiTest('preserves native option previews in a narrow layout after reload', async ({ native }) => {
  await exerciseQuestionPreviews(native)
})

ohMyPiTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

ohMyPiTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})

ohMyPiTest('clears optional defaults and preserves the empty answer after reload', async ({ native }) => {
  const callId = 'optional-languages'
  const answer = 'The optional languages are empty.'
  const html = '<form><fieldset><legend>Languages</legend><label><input type="checkbox" name="langs" value="go" checked>Go</label><label><input type="checkbox" name="langs" value="rust">Rust</label><label><input type="checkbox" name="langs" value="disabled" checked disabled>Disabled</label></fieldset></form>'
  const start = await native.modelScript.queue(
    {
      toolCalls: [
        ohMyPiAskFormToolCall(callId, html, 'Choose languages'),
      ],
    },
    nativeTextStep(native, answer),
  )
  await sendMessage(native.page, native.modelScript.prompt('Ask the optional languages form and report the answer.'))
  await native.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(native.page)
  const selected = banner.getByTestId('question-option-Go')
  await expect(selected.locator('input')).toBeChecked()
  await expect(banner.getByTestId('question-option-Disabled')).toHaveCount(0)
  await selected.click()
  await restoreQuestionEmptySelection(native, banner, 'Go')
  await expect(controlButton(native.page, 'submit')).toBeEnabled()
  await controlButton(native.page, 'submit').click()
  await native.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(native.page)
  const result = await nativeToolOutcome(native, await native.modelScript.requestAt(start + 1), callId)
  expect(result.text).toMatch(/"langs":\s*\[\]/)
  expect(result.text).not.toContain('go')
  expect(result.text).not.toContain('disabled')
  await expect(assistantBubbles(native.page).filter({ hasText: answer }).first()).toBeVisible()
  await expectNoControlBanner(native.page)
})
