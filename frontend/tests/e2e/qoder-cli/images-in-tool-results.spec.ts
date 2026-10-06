import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI images in tool results', () => {
  qoderTest('renders an image returned by the native Read tool', async ({ native, authenticatedQoderWorkspace }) => {
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedQoderWorkspace.workingDir,
      marker: 'qoder-348',
      toolCall: image => readToolCall(native.provider, 'read-qoder-image', image.path),
    })
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, fileName)
  })
})
