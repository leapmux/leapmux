import { expect } from '@playwright/test'
import { reasonixViewImageToolCall } from '../helpers/providerToolCalls'
import { expectImageDataUriInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { toolCallRow } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('recovers the native stored image bytes into the tool result and keeps them after reload', async ({ native, authenticatedReasonixWorkspace }) => {
  const callId = 'reasonix-view-image'
  const { fileName, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedReasonixWorkspace.workingDir,
    marker: 'reasonix',
    toolCall: image => reasonixViewImageToolCall(callId, image.path),
  })
  expectImageDataUriInRequest(resultRequest, 'image/png')
  for (const reload of [false, true]) {
    if (reload)
      await native.page.reload()
    const result = toolCallRow(native.page, callId)
    await expect(result).toContainText(fileName)
    await expectToolRowImage(native.page, fileName)
    await expect(result.locator('button[aria-label="Open image"]')).toHaveCount(1)
  }
})
