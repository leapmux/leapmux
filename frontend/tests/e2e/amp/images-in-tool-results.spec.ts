import { ampTest } from '../amp-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'

/**
 * The test calls a real native image tool. The correlated completed result must contain a decoded image.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws a PNG returned by its Read tool', async ({ native, authenticatedAmpWorkspace }) => {
    const { fileName } = await runToolImageTurn(native, {
      workingDir: authenticatedAmpWorkspace.workingDir,
      marker: 'amp-read',
      toolCall: image => readToolCall(native.provider, 'read-image', image.path),
    })
    await expectToolRowImage(native.page, fileName)
  })
})
