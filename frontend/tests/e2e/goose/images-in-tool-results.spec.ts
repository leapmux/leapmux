import { join } from 'node:path'
import { expect } from '@playwright/test'
import { gooseTest } from '../goose-fixtures'
import { goosePermissionJudgmentToolCall, gooseReadImageToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

gooseTest('shows the picture returned by read_image', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
  const workingDir = authenticatedGooseWorkspace.workingDir
  if (!workingDir)
    throw new Error('Goose test workspace has no working directory')
  const fileName = writeToolImage(workingDir, 'goose')
  await modelScript.rule({
    name: 'the permission judge clears the image read',
    when: { system: 'permission-safety classifier' },
    respond: { toolCalls: [goosePermissionJudgmentToolCall('judge-goose-image', ['goose-read-image'])] },
  })
  await modelScript.queue(
    { toolCalls: [gooseReadImageToolCall('goose-read-image', join(workingDir, fileName))] },
    { text: `I inspected ${fileName}.` },
  )
  await sendMessage(page, modelScript.prompt(`Read ${fileName} as an image.`))
  await modelScript.waitForSteps(1)
  const permission = page.getByTestId('control-banner').filter({ visible: true })
  await expect(permission).toContainText(fileName)
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
  const status = await modelScript.waitForSteps()
  const toolResultRequest = status.requests.find(request => request.stepIndex === 1)
  expect(JSON.stringify(toolResultRequest?.body)).toContain('data:image/png;base64,iVBORw0KGgo')
  await waitForAgentIdle(page)
  const nativeResult = page.locator('[data-testid="message-bubble"]:visible')
    .filter({ hasText: 'Loaded image from' })
    .filter({ hasText: fileName })
  await expect(nativeResult.first()).toBeVisible()
  await expectDecodedImageInBubble(nativeResult.first())
})
