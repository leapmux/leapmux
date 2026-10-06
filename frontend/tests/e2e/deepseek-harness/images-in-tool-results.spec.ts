import { join } from 'node:path'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessReadImageToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, writeToolImage } from '../helpers/toolImages'
import { sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'

deepseekHarnessTest('recovers the real native image bytes and keeps the decoded image after reload', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const name = writeToolImage(agent.workingDir, 'deepseek-native')
  const callId = 'native-image-read'
  await modelScript.queue(
    { toolCalls: [deepseekHarnessReadImageToolCall(callId, join(agent.workingDir, name))] },
    { text: 'The native image read completed.' },
  )
  await sendMessage(page, modelScript.prompt('Read the actual scripted native image.'))
  await waitForNativeToolSteps(context, 2)
  const next = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
  expect(nativeToolResult(next, callId)).toContain('64x64')
  const bubble = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`)
  await expect(bubble).toHaveCount(1)
  await expectDecodedImageInBubble(bubble)
  await page.reload()
  await expectDecodedImageInBubble(bubble)
})
