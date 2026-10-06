import { join } from 'node:path'
import { expect } from '@playwright/test'
import { reasonixViewImageToolCall } from '../helpers/providerToolCalls'
import { writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('keeps view_image text when ACP omits image bytes', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  const workingDir = authenticatedReasonixWorkspace.workingDir
  if (!workingDir)
    throw new Error('Reasonix test workspace has no working directory')
  const fileName = writeToolImage(workingDir, 'reasonix')
  await modelScript.queue(
    { toolCalls: [reasonixViewImageToolCall('reasonix-view-image', join(workingDir, fileName))] },
    { text: `I inspected ${fileName}.` },
  )
  await sendMessage(page, modelScript.prompt(`Read ${fileName} as an image.`))
  const status = await modelScript.waitForSteps()
  const toolResultRequest = status.requests.find(request => request.stepIndex === 1)
  expect(JSON.stringify(toolResultRequest?.body)).toContain('data:image/png;base64,iVBORw0KGgo')
  await waitForAgentIdle(page)
  const result = page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: '[image: image/png, 64x64]' }).first()
  await expect(result).toContainText(fileName)
  await expect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]')).toHaveCount(0)
})
