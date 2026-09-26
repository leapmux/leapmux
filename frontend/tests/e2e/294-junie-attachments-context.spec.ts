import { expectAttachmentOutcome } from './helpers/attachments'
import { junieAnswerToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

junieTest.describe('Junie attachments and context usage', () => {
  // Junie takes a text and an image attachment. The composer draws a pill that
  // names each file; the matrix marks PDF and other binary kinds ❌.
  junieTest('accepts a text and an image attachment', async ({ authenticatedJunieWorkspace, page }) => {
    void authenticatedJunieWorkspace
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'jnote.txt' })
    await page.reload()
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'jshot.png' })
  })

  // The usage block the mock reports is the only source of these counts. A
  // default of 1/1 would make every number equal; 12000/40 is the marker.
  junieTest('the agent info grid follows the usage the model reports', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await modelScript.rule(
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Usage task' } },
    )
    await modelScript.queue({
      toolCalls: [junieAnswerToolCall('junie-usage-answer', 'Usage recorded.')],
      usage: { inputTokens: 12000, outputTokens: 40 },
    })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const infoTrigger = page.locator('[data-testid="agent-info-trigger"]')
    await expect(infoTrigger).toBeVisible()
    await infoTrigger.click()
    const popover = page.locator('[data-testid="agent-info-popover"]')
    await expect(popover).toBeVisible()
    const grid = popover.getByTestId('context-usage-grid')
    await expect(grid).toBeVisible()
    await expect(grid).toContainText('12')
    await expect(grid).toContainText('40')
  })
})
