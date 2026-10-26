import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { userBubbles } from '../helpers/ui'

codebuddyTest('delivers native WAV bytes with and without a prompt and retains both attachments after reload', async ({ native }) => {
  const names = ['voice-with-prompt.wav', 'voice-only.wav']
  for (const [index, name] of names.entries()) {
    await exerciseAttachmentDelivery(native, 'audio', name, {
      protocol: 'openai-chat-completions',
      mediaType: 'audio/wav',
      attachmentOnly: index === 1,
    })
  }
  await native.page.reload()
  for (const name of names)
    await expect(userBubbles(native.page).filter({ hasText: name })).toHaveCount(1)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
