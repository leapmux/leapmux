import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result.
  mimoTest('a Read of a PNG draws the picture in the tool row', async ({ native, authenticatedMiMoWorkspace }) => {
    const { resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedMiMoWorkspace.workingDir,
      marker: 'mimo-64',
      toolCall: image => readToolCall(native.provider, 'read-png', image.fileName),
    })
    // The mock model declares the image input modality, so the read tool gives the PNG to the next model request.
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, 'tool-image-mimo-64')
  })
})
