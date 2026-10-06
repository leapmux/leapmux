import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('a Read of a PNG draws the picture in the tool row', async ({ native, authenticatedOpencodeWorkspace }) => {
  const { resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedOpencodeWorkspace.workingDir,
    marker: 'opencode-33',
    toolCall: image => readToolCall(native.provider, 'read-png', image.path),
  })
  // The mock model declares the image input modality, so the read tool gives the PNG to the next model request.
  expectPngInRequest(resultRequest)
  await expectToolRowImage(native.page, 'tool-image-opencode-33')
})
