import { clineTest } from '../cline-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowWithoutImage, runToolImageTurn } from '../helpers/toolImages'

clineTest.describe('Cline images in tool results', () => {
  clineTest('a Read of a PNG draws the name and no picture in the tool row', async ({ native, authenticatedClineWorkspace }) => {
    await runToolImageTurn(native, {
      workingDir: authenticatedClineWorkspace.workingDir,
      marker: 'cline-19',
      toolCall: image => readToolCall(native.provider, 'read-png', image.path),
    })
    // The name proves the tool ran. Cline builds tool results as text only, so
    // the row draws no picture (matrix note 3).
    await expectToolRowWithoutImage(native.page, 'tool-image-cline-19')
  })
})
