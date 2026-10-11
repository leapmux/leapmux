import { expect } from '@playwright/test'
import { reasonixViewImageToolCall } from '../helpers/providerToolCalls'
import { expectImageDataUriInRequest, runToolImageTurn } from '../helpers/toolImages'
import { chatScrollContainer, messageBubbles } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('keeps view_image text when ACP omits image bytes', async ({ native, authenticatedReasonixWorkspace }) => {
  const { fileName, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedReasonixWorkspace.workingDir,
    marker: 'reasonix',
    toolCall: image => reasonixViewImageToolCall('reasonix-view-image', image.path),
  })
  expectImageDataUriInRequest(resultRequest, 'image/png')
  const result = messageBubbles(native.page).filter({ hasText: '[image: image/png, 64x64]' }).first()
  await expect(result).toContainText(fileName)
  await expect(chatScrollContainer(native.page).locator('button[aria-label="Open image"]')).toHaveCount(0)
})
