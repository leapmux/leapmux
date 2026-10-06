import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessReadImageToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, runToolImageTurn } from '../helpers/toolImages'
import { toolCallRow } from '../helpers/ui'

deepseekHarnessTest('recovers the real native image bytes and keeps the decoded image after reload', async ({ native, authenticatedDeepseekHarnessWorkspace }) => {
  const callId = 'native-image-read'
  const { resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedDeepseekHarnessWorkspace.workingDir,
    marker: 'deepseek-native',
    toolCall: image => deepseekHarnessReadImageToolCall(callId, image.path),
  })
  expect(nativeToolResult(resultRequest, callId)).toContain('64x64')
  const bubble = toolCallRow(native.page, callId)
  await expect(bubble).toHaveCount(1)
  await expectDecodedImageInBubble(bubble)
  await native.page.reload()
  await expectDecodedImageInBubble(bubble)
})
