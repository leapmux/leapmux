import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { kiloTest } from '../kilo-fixtures'

kiloTest('shows the picture returned by Read', async ({ native, authenticatedKiloWorkspace }) => {
  const { fileName, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedKiloWorkspace.workingDir,
    marker: 'kilo',
    toolCall: image => readToolCall(native.provider, 'read-image-probe', image.path),
  })
  // The mock model declares the image input modality, so the read tool gives the PNG to the next model request.
  expectPngInRequest(resultRequest)
  await expectToolRowImage(native.page, fileName)
})
