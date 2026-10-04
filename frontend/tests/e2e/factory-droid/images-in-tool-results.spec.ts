import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_TITLE_RULE, droidTest } from '../droid-fixtures'
import { expect } from '../fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { writeToolImage } from '../helpers/toolImages'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'

droidTest.describe('Factory Droid images in tool results', () => {
  droidTest('renders an image returned by the native Read tool', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    const fileName = writeToolImage(authenticatedDroidWorkspace.workingDir, 'droid-348')
    const filePath = join(authenticatedDroidWorkspace.workingDir, fileName)
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.DROID, 'read-droid-image', filePath)] },
      { text: 'The image was read.' },
    )
    await sendMessage(page, modelScript.prompt('Read the local image file.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body ?? {}).includes('iVBORw0KGgo')).toBe(true)
    await expect(messageContents(page).filter({ hasText: fileName }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: '"data":"iVBOR' })).toHaveCount(0)
    const image = page.locator('button[aria-label="Open image"] img').first()
    await expect(image).toBeVisible()
    await expect.poll(() => image.evaluate(el => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
  })
})
