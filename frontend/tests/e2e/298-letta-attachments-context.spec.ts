import { expectAttachmentOutcome } from './helpers/attachments'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

/**
 * 298 — Letta Code attachments and context usage.
 *
 * Letta takes a text and an image attachment. The worker and the plugin both
 * reject a PDF (`RejectPDFAndBinaryAttachment`, `pdf: false`), so the matrix
 * cell that marks PDF ✅ is not reproducible here; only text and image are
 * asserted. Context usage reads the usage block the mock reports.
 */
lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('accepts a text and an image attachment', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'lnote.txt' })
    await page.reload()
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'lshot.png' })
  })

  // The usage block the mock reports is the only source of these counts. A
  // default of 1/1 would make every number equal; 12000/40 is the marker.
  lettaTest('the agent info grid follows the usage the model reports', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({
      text: ARITHMETIC_ANSWER_TEXT,
      usage: { inputTokens: 12000, outputTokens: 40 },
    })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

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
