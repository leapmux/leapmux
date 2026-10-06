import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
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
    // The mock model takes image input, so the read tool gives the PNG to the next model request.
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, fileName)
  })
})
