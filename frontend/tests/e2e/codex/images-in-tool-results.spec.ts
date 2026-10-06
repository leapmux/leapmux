import { codexTest } from '../codex-fixtures'
import { codexViewImageToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'

codexTest.describe('Codex images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result.
  codexTest('a Read of a PNG draws the picture in the tool row', async ({ native, authenticatedCodexWorkspace }) => {
    const { resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedCodexWorkspace.workingDir,
      marker: 'codex-77',
      toolCall: image => codexViewImageToolCall('read-png', image.fileName),
    })
    // `view_image` exists to put the image into the model input, so the next model request holds the PNG.
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, 'tool-image-codex-77')
  })
})
