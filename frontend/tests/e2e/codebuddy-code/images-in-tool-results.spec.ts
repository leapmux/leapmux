import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowWithoutImage, runToolImageTurn } from '../helpers/toolImages'

codebuddyTest.describe('CodeBuddy Code file tool execution', () => {
  codebuddyTest('shows the native Read placeholder without an inline image', async ({ native, authenticatedCodebuddyWorkspace }) => {
    const { fileName, path, resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedCodebuddyWorkspace.workingDir,
      marker: 'codebuddy-read',
      toolCall: image => readToolCall(native.provider, 'read-image', image.path),
    })
    expect(resultRequest.protocol).toBe('openai-chat-completions')
    expect(nativeToolResult(resultRequest, 'read-image'))
      .toBe(`This is an image file. Use the ViewImage tool with the absolute path "${path}" to view its content.`)
    await expectToolRowWithoutImage(native.page, fileName)
  })
})
