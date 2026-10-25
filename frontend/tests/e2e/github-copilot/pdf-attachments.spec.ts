import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { exerciseAttachmentDelivery, nativeUserStrings } from '../helpers/attachmentModelProbe'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { chooseSettingsOption, messageContents, waitForAgentIdle } from '../helpers/ui'

/** The start of the notice that states a PDF which reached the model as its file name alone. */
const NAME_ONLY_NOTICE = 'Copilot sent only the file name of'

// Copilot gives a PDF to a model on the Responses API as an `input_file` part, and to a model on the Messages API as a
// `document` block. The mock serves this model on the Responses API.
copilotTest('gives a PDF to a model on the Responses API as a file', async ({ native }) => {
  await chooseSettingsOption(native.page, `model-${MOCK_MODELS.gooseReasoning}`)
  await exerciseAttachmentDelivery(native, 'pdf', 'copilot-doc.pdf', { protocol: 'openai-responses' })
  await expect(messageContents(native.page).filter({ hasText: NAME_ONLY_NOTICE })).toHaveCount(0)
})

copilotTest('gives a PDF to a model on the Messages API as a document', async ({ native }) => {
  await chooseSettingsOption(native.page, `model-${MOCK_MODELS.anthropic}`)
  await exerciseAttachmentDelivery(native, 'pdf', 'copilot-anthropic-doc.pdf', { protocol: 'anthropic-messages' })
  await expect(messageContents(native.page).filter({ hasText: NAME_ONLY_NOTICE })).toHaveCount(0)
})

// On Chat Completions, Copilot sends the PDF's file name alone. Copilot states no model's API before a send, so LeapMux
// sends the PDF and states the outcome after the message, from the message's echo.
copilotTest('states a PDF that a model on Chat Completions reads as its file name alone', async ({ native }) => {
  const { page } = native
  const fileName = 'copilot-name-only.pdf'
  await exerciseAttachmentDelivery(native, 'pdf', fileName, {
    protocol: 'openai-chat-completions',
    proof: (request, sourcePath) => {
      expect(JSON.stringify(request.body), 'the bytes of the PDF stay out of the request').not.toContain(readFileSync(sourcePath).toString('base64'))
      expect(nativeUserStrings(request.body).join('\n'), 'the model reads the file name').toContain(fileName)
    },
  })
  const notice = messageContents(page).filter({ hasText: `${NAME_ONLY_NOTICE} ${fileName}` })
  await expect(notice).toHaveCount(1)
  await page.reload()
  await waitForAgentIdle(page)
  await expect(notice).toHaveCount(1)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/510.
