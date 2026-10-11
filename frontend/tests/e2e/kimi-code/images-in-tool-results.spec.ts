import { kimiReadMediaFileToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('Kimi Code images in tool results', () => {
  kimiTest('a ReadMediaFile of a PNG draws the picture in the tool row', async ({ native, authenticatedKimiWorkspace }) => {
    const { resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedKimiWorkspace.workingDir,
      marker: 'kimi-42',
      toolCall: image => kimiReadMediaFileToolCall('read-png', image.fileName),
    })
    // The mock model of Kimi declares the `image_in` capability, so ReadMediaFile gives the PNG to the next model request.
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, 'tool-image-kimi-42')
  })
})
