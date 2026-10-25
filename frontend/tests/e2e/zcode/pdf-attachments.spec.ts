import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseAttachmentDelivery, expectAttachmentInRequest } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { expectNativeInputRefusal } from '../helpers/nativeInputQueueRefusal'
import { currentNativeAgent, expectSameNativeSession, nativeTextStep } from '../helpers/nativeScenario'
import { assistantBubbles, chooseSettingsOption, inputQueue, userBubbles, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

/** The default model, whose entry in ZCode's own model registry declares PDF input. */
const PDF_MODEL = `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.zai}`

/** The alternate model, which declares text input only. */
const TEXT_MODEL = `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`

// ZCode gives a PDF to a model that declares PDF input as a Chat Completions `file` part.
zcodeTest('gives a PDF to a model that declares PDF input as a file', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'pdf', 'zcode-doc.pdf', { protocol: 'openai-chat-completions' })
})

// ZCode accepts a PDF for a model without PDF input and then omits it from the model request, so the Worker refuses
// the send and states the model. The same queued item reaches the model after a switch to a model with PDF input.
zcodeTest('refuses a PDF on a model without PDF input and delivers the same queued item after a model switch', async ({ native }) => {
  const { page, modelScript } = native
  await chooseSettingsOption(page, `model-${TEXT_MODEL}`)
  await waitForSettingsIdle(page)
  const before = await currentNativeAgent(native)
  const answer = 'The retried PDF arrived.'
  const step = await modelScript.queue(nativeTextStep(native, answer))
  const file = await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'zcode-text-only.pdf' })
  await sendWithAttachment(page, modelScript.prompt('Inspect this PDF after choosing a model that reads PDFs.'))
  const failed = await expectNativeInputRefusal(native, `the ZCode model ${TEXT_MODEL} does not accept PDF attachments (${basename(file)})`)
  const status = await modelScript.status()
  expect(status.nextStep, 'the refused PDF reached no model').toBe(step)
  expect(status.unexpectedRequests).toEqual([])

  await chooseSettingsOption(page, `model-${PDF_MODEL}`)
  await waitForSettingsIdle(page)
  await inputQueue(page).getByTestId(`queued-input-${failed.id}`).getByRole('button', { name: 'Retry', exact: true }).click()
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(step)
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
  await expectAttachmentInRequest(page, request, 'pdf', file)
  await expect(inputQueue(page)).toHaveCount(0)
  await expect(userBubbles(page).filter({ hasText: basename(file) })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toBeVisible()
  expectSameNativeSession(before, await currentNativeAgent(native), 'Retrying the PDF')
})
