import { ampTest } from '../amp-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'

/**
 * The test calls a real native image tool. The correlated completed result must contain a decoded image.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws a PNG returned by its Read tool', async ({ native, authenticatedAmpWorkspace }) => {
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedAmpWorkspace.workingDir,
      marker: 'amp-read',
      toolCall: image => readToolCall(native.provider, 'read-image', image.path),
    })
    // Amp's executor returns the Read result, with its image, to the remote actor. The actor gives the thread to the
    // model, so the next model request holds the PNG.
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, fileName)
  })
})
