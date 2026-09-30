import { compactionNoticeRow } from './helpers/compaction'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

lettaTest('passes compact text to the model on its App Server path', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
  void authenticatedLettaWorkspace
  await modelScript.rule(LETTA_TITLE_RULE)
  const priorAnswer = 'LETTA_PRIOR_CONTEXT_MARKER stays in the conversation.'
  await modelScript.queue(
    { text: priorAnswer },
    { text: 'The slash text reached the model.' },
    { text: 'The later task ended.' },
  )
  await sendMessage(page, modelScript.prompt('Record an earlier answer.'))
  await modelScript.waitForSteps(1)
  await waitForAgentIdle(page, 180_000)

  await sendMessage(page, modelScript.prompt('/compact'))
  const compactStatus = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page, 180_000)
  expect(JSON.stringify(compactStatus.requests.find(record => record.stepIndex === 1)?.body)).toContain('/compact')
  await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'The slash text reached the model.' }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)

  await sendMessage(page, modelScript.prompt('Continue the prior conversation.'))
  const continued = await modelScript.waitForSteps(3)
  await waitForAgentIdle(page, 180_000)
  const nextBody = JSON.stringify(continued.requests.find(record => record.stepIndex === 2)?.body)
  expect(nextBody).toContain(priorAnswer)
  expect(nextBody).toContain('Continue the prior conversation.')
  expect(continued.requests.filter(record => record.stepIndex !== undefined)).toHaveLength(3)
})
