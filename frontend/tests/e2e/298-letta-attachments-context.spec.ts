import { expectAttachmentOutcome } from './helpers/attachments'
import { expectContextUsage } from './helpers/contextUsage'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'
import { LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

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
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({
      text: ARITHMETIC_ANSWER_TEXT,
      usage,
    })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectContextUsage(page, usage)
  })
})
