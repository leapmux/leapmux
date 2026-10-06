import { droidTest } from '../droid-fixtures'
import { expect } from '../fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, PNG_BASE64_PREFIX, runToolImageTurn } from '../helpers/toolImages'
import { messageContents } from '../helpers/ui'

droidTest.describe('Factory Droid images in tool results', () => {
  droidTest('renders an image returned by the native Read tool', async ({ native, authenticatedDroidWorkspace }) => {
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedDroidWorkspace.workingDir,
      marker: 'droid-348',
      toolCall: image => readToolCall(native.provider, 'read-droid-image', image.path),
    })
    expectPngInRequest(resultRequest)
    await expect(messageContents(native.page).filter({ hasText: fileName }).first()).toBeVisible()
    // The row draws the decoded picture and never shows the base64 text of the image.
    await expect(messageContents(native.page).filter({ hasText: PNG_BASE64_PREFIX })).toHaveCount(0)
    await expectToolRowImage(native.page, fileName)
  })
})
