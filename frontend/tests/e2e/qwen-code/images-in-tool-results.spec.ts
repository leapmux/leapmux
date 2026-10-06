import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowWithoutImage, runToolImageTurn } from '../helpers/toolImages'
import { chatScrollContainer, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { qwenTest } from '../qwen-fixtures'
import { nativeContext, QWEN_AGENT } from './scenarios'

qwenTest.describe('Qwen Code tool execution', () => {
  qwenTest('shows the native image overview without an inline image', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { workingDir } = await openProviderAgent(leapmuxServer, context.workspaceId, QWEN_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    await openWorkspace(page, context.workspaceId)
    const { fileName, resultRequest } = await runToolImageTurn(context, {
      workingDir,
      marker: 'qwen-read',
      toolCall: image => readToolCall(context.provider, 'read-image', image.path),
    })
    expect(resultRequest.protocol).toBe('openai-chat-completions')
    // Qwen re-encodes the image as a JPEG before the model request.
    expect(JSON.stringify(resultRequest.body).includes('data:image/jpeg;base64,/9j/')).toBe(true)
    await expectToolRowWithoutImage(page, fileName)
    await expect(chatScrollContainer(page).getByText(/Image overview: 64x64/).first()).toBeVisible()
  })
})
