import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { exerciseAttachmentDelivery, nativeUserStrings } from '../helpers/attachmentModelProbe'
import { writeVideoAttachmentFixture } from '../helpers/attachments'
import { userBubbles } from '../helpers/ui'

geminiTest('delivers a complete MP4 with its exact native MIME type and bytes and retains it after reload', async ({ native }) => {
  const name = 'native-video.mp4'
  const file = writeVideoAttachmentFixture(name)
  const request = await exerciseAttachmentDelivery(native, 'binary', name, { fixturePath: file, protocol: 'google-generative-language', mediaType: 'video/mp4' })
  expect(nativeUserStrings(request.body).filter(value => value === 'video/mp4')).toHaveLength(1)
  await native.page.reload()
  await expect(userBubbles(native.page).filter({ hasText: name })).toHaveCount(1)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
