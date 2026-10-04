import { join } from 'node:path'
import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { readToolCall } from '../helpers/providerToolCalls'
import { writeToolImage } from '../helpers/toolImages'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

diracTest('receives a real native PNG read without a rendered tool-result image', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const image = writeToolImage(agent.workingDir, 'dirac-native-read')
  const start = (await modelScript.status()).stepCount
  await modelScript.queue(
    { toolCalls: [readToolCall(context.provider, 'dirac-image-read', join(agent.workingDir, image))] },
    nativeTextStep(context, 'The native image read completed.'),
  )
  await sendMessage(page, modelScript.prompt('Read the scripted local PNG and complete the task.'))
  const status = await modelScript.waitForSteps(start + 2)
  const result = nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), 'dirac-image-read')
  expect(result).not.toBe('')
  await waitForAgentIdle(page)
  await expect(messageContents(page).filter({ hasText: 'The native image read completed.' }).first()).toBeVisible()
  await expect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]')).toHaveCount(0)
  await page.reload()
  await expect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]')).toHaveCount(0)
})
