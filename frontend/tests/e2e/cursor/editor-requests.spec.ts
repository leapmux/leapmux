import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { controlButton, messageBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'

// Cursor's service answers the question itself in the same Run exchange, so the turn is one scripted step and the
// answer is the service's own words, not a scripted answer.
cursorTest('resolves an actual native control without exposing a multiline editor request', async ({ native }) => {
  const { page, modelScript } = native
  await expectNoNativeEditorRequest(native, {
    relatedProof: async () => {
      const start = await modelScript.queue({ toolCalls: [askUserQuestionToolCall(native.provider, 'native-editor-limit-question', [NATIVE_CONTROL_QUESTION])] })
      await sendMessage(page, modelScript.prompt('Ask the scripted native control question.'))
      await modelScript.waitForSteps(start + 1)
      const banner = await waitForControlBanner(page)
      await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
      await banner.getByTestId('question-option-Green').click()
      await controlButton(page, 'submit').click()
      await waitForAgentIdle(page)
      await expect(messageBubbles(page).filter({ hasText: 'Cursor question selected: option-1-2' }).first()).toBeVisible()
    },
  })
})
