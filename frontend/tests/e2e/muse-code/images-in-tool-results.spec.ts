/**
 * Muse's native read of an image file hands its bytes to the NEXT model request as
 * an image part, and its tool result states the file and its media type as text
 * alone. The wire's tool result carries no image content, so the row draws no
 * picture and no renderable bytes reach the transcript.
 */
import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowWithoutImage, runToolImageTurn } from '../helpers/toolImages'
import { museTest } from '../muse-fixtures'

museTest.describe('Muse Code images in tool results', () => {
  museTest('a native read of a PNG reaches the model and draws no picture in the tool row', async ({ native, authenticatedMuseWorkspace }) => {
    const workingDir = authenticatedMuseWorkspace.workingDir
    if (!workingDir)
      throw new Error('The image proof requires the working directory of the native agent.')
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir,
      marker: 'muse-read',
      toolCall: image => readToolCall(native.provider, 'read-png', image.path),
    })
    expectPngInRequest(resultRequest)
    await expectToolRowWithoutImage(native.page, fileName)
  })
})
