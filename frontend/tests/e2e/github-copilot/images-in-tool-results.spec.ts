import { decompressContentToString } from '../../../src/lib/decompress'
import { copilotTest } from '../copilot-fixtures'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'

copilotTest('shows the picture returned by View', async ({ native, authenticatedCopilotWorkspace }, testInfo) => {
  try {
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedCopilotWorkspace.workingDir,
      marker: 'copilot',
      approve: true,
      toolCall: image => readToolCall(native.provider, 'read-image-probe', image.path),
    })
    // Copilot returns the file of a tool as a user row after the tool row, so the next model request holds the PNG.
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, fileName)
  }
  catch (error) {
    try {
      const messages = await readAllAgentMessages(native, await selectedAgentTabId(native.page))
      const rows = messages.map(message => ({
        seq: String(message.seq),
        spanType: message.spanType,
        content: decompressContentToString(message.content, message.contentCompression),
        supplemental: decompressContentToString(message.supplementalContent, message.supplementalContentCompression),
      }))
      await testInfo.attach('copilot-native-worker-rows', { body: JSON.stringify(rows, null, 2), contentType: 'application/json' })
    }
    catch (captureError) {
      await testInfo.attach('copilot-native-worker-capture-error', { body: String(captureError), contentType: 'text/plain' })
    }
    throw error
  }
})
