import { clineTest } from '../cline-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowWithoutImage, runToolImageTurn } from '../helpers/toolImages'

clineTest.describe('images in tool results for Cline', () => {
  clineTest('a Read of a PNG draws the name and no picture in the tool row', async ({ native, authenticatedClineWorkspace }) => {
    await runToolImageTurn(native, {
      workingDir: authenticatedClineWorkspace.workingDir,
      marker: 'cline-19',
      toolCall: image => readToolCall(native.provider, 'read-png', image.path),
    })
    // Cline can return native image parts, but LeapMux does not extract them from read results.
    // This profile does not prove the positive image shape. See https://github.com/leapmux/leapmux/issues/497.
    await expectToolRowWithoutImage(native.page, 'tool-image-cline-19')
  })
})
