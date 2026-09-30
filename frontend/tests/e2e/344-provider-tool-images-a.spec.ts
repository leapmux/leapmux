import { join } from 'node:path'
import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { gooseReadImageToolCall, reasonixViewImageToolCall } from './helpers/providerToolCalls'
import { expectDecodedImageInBubble, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

gooseTest.describe('Goose images in tool results', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')
  gooseTest('shows the picture returned by read_image', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    const workingDir = authenticatedGooseWorkspace.workingDir
    if (!workingDir)
      throw new Error('Goose test workspace has no working directory')
    const fileName = writeToolImage(workingDir, 'goose')
    await modelScript.rule({
      name: 'the permission judge clears the image read',
      when: { system: 'permission-safety classifier' },
      respond: { toolCalls: [{
        id: 'judge-goose-image',
        name: 'platform__tool_by_tool_permission',
        arguments: { read_only_request_ids: ['goose-read-image'] },
      }] },
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
})

reasonixTest.describe('Reasonix images in tool results', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
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
})
