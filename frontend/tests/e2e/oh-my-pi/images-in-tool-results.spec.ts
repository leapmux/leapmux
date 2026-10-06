import { readToolCall } from '../helpers/providerToolCalls'
import { expectImageDataUriInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The test calls a real native image tool. The correlated completed result must contain a decoded image.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi tool execution', () => {
  ohMyPiTest('draws a PNG returned by its Read tool', async ({ native, authenticatedOhMyPiWorkspace }) => {
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedOhMyPiWorkspace.workingDir,
      marker: 'omp-read',
      toolCall: image => readToolCall(native.provider, 'read-image', image.path),
    })
    // omp encodes the image again as a WebP before the model reads it. The tool row states only "Read image file
    // [image/webp]", and the user row after it holds the picture as a WebP image part.
    expectImageDataUriInRequest(resultRequest, 'image/webp')
    await expectToolRowImage(native.page, fileName)
  })
})
