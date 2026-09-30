import { exerciseAttachmentDelivery } from './helpers/attachmentModelProbe'
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
 * Letta takes text and image attachments. The worker and browser reject PDF
 * and other binary files. The tests check the model request for file bytes.
 */
lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('delivers text attachment bytes to the model', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'letta-notes.txt')
  })

  lettaTest('delivers image attachment bytes to the model', async ({ authenticatedVisionLettaWorkspace, page, modelScript }) => {
    void authenticatedVisionLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'letta-shot.png')
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
