import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectImageDataUriInRequest, expectToolRowWithoutImage, runToolImageTurn } from '../helpers/toolImages'

codebuddyTest.describe('CodeBuddy Code file tool execution', () => {
  codebuddyTest('shows the native Read placeholder without an inline image', async ({ native, authenticatedCodebuddyWorkspace }) => {
    const { fileName, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedCodebuddyWorkspace.workingDir,
      marker: 'codebuddy-read',
      toolCall: image => readToolCall(native.provider, 'read-image', image.path),
    })
    expect(resultRequest.protocol).toBe('openai-chat-completions')
    expectImageDataUriInRequest(resultRequest, 'image/png')
    await expectToolRowWithoutImage(native.page, fileName)
  })
})
