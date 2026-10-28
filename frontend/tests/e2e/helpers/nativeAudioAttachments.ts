import type { MockModelProtocol, MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { ATTACHMENT_MEDIA_AUDIO_MIME } from '../../../src/generated/contracts/attachment-media'
import { exerciseAttachmentDelivery, expectAttachmentInRequest } from './attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment, writeAudioAttachmentFixture } from './attachments'
import { expectNativeInputRefusal } from './nativeInputQueueRefusal'
import { currentNativeAgent, expectSameNativeSession, nativeTextStep } from './nativeScenario'
import { chooseNativeModel } from './nativeSettings'
import { assistantBubbles, inputQueue, userBubbles, waitForAgentIdle } from './ui'

/** Prove original WAV/FLAC bytes for both prompt forms, then reload every recording's history row. */
export async function proveNativeAudioAttachments(context: NativeScenarioContext, protocol: MockModelProtocol): Promise<readonly MockModelRequestRecord[]> {
  const names: string[] = []
  const requests: MockModelRequestRecord[] = []
  for (const [format, mediaType] of [['wav', ATTACHMENT_MEDIA_AUDIO_MIME.Wav], ['flac', ATTACHMENT_MEDIA_AUDIO_MIME.Flac]] as const) {
    for (const attachmentOnly of [false, true]) {
      const name = `${format}-${attachmentOnly ? 'only' : 'prompt'}.${format}`
      names.push(name)
      requests.push(await exerciseAttachmentDelivery(context, 'audio', name, { fixturePath: writeAudioAttachmentFixture(name, format), protocol, mediaType, attachmentOnly }))
    }
  }
  await context.page.reload()
  for (const name of names)
    await expect(userBubbles(context.page).filter({ hasText: name })).toHaveCount(1)
  return requests
}

/** Retain a refused recording across reload and retry it once after switching the same native session's model. */
export async function proveNativeAudioModelRetry(context: ManagedNativeScenarioContext, modelID: string, requestModel?: string): Promise<void> {
  const { page, modelScript } = context
  const before = await currentNativeAgent(context)
  const name = 'retained-recording.wav'
  const path = writeAudioAttachmentFixture(name)
  await expectAttachmentOutcome(page, 'audio', { supported: true, fileName: name, fixturePath: path })
  const answer = 'The retried recording reached the audio model.'
  const step = await modelScript.queue(nativeTextStep(context, answer))
  await sendWithAttachment(page, modelScript.prompt('Listen after choosing a model that accepts audio.'))
  const reason = 'does not accept audio attachments'
  const failed = await expectNativeInputRefusal(context, reason)
  await page.reload()
  const restored = await expectNativeInputRefusal(context, reason)
  expect(restored.id).toBe(failed.id)
  const status = await modelScript.status()
  expect(status.requests).toHaveLength(0)
  expect(status.unexpectedRequests).toHaveLength(0)
  await chooseNativeModel(context, modelID)
  await inputQueue(page).getByTestId(`queued-input-${failed.id}`).getByRole('button', { name: 'Retry', exact: true }).click()
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(step)
  if (requestModel !== undefined)
    expect(request.body).toMatchObject({ model: requestModel })
  await expectAttachmentInRequest(page, request, 'audio', path, { mediaType: ATTACHMENT_MEDIA_AUDIO_MIME.Wav })
  await expect(inputQueue(page)).toHaveCount(0)
  await expect(userBubbles(page).filter({ hasText: name })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  expectSameNativeSession(before, await currentNativeAgent(context), 'Retrying the recording')
}
