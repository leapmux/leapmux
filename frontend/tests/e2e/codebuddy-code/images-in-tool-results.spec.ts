import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowWithoutImage, runToolImageTurn } from '../helpers/toolImages'

codebuddyTest.describe('CodeBuddy Code file tool execution', () => {
  codebuddyTest('shows the native Read placeholder without an inline image', async ({ native, authenticatedCodebuddyWorkspace }) => {
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedCodebuddyWorkspace.workingDir,
      marker: 'codebuddy-read',
      toolCall: image => readToolCall(native.provider, 'read-image', image.path),
    })
    expect(resultRequest.protocol).toBe('openai-chat-completions')
    expectPngInRequest(resultRequest, 'data-uri')
    await expectToolRowWithoutImage(native.page, fileName)
  })
})
