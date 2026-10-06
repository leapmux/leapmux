import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { compactionNoticeRow } from '../helpers/compaction'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect } from '../letta-fixtures'

/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseOrdinaryCompactText(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  const priorAnswer = 'LETTA_PRIOR_CONTEXT_MARKER stays in the conversation.'
  await modelScript.queue(
    { text: priorAnswer },
    { text: 'The slash text reached the model.' },
    { text: 'The later task ended.' },
  )
  await sendMessage(page, modelScript.prompt('Record an earlier answer.'))
  await modelScript.waitForSteps(1)
  await waitForAgentIdle(page)

  await sendMessage(page, modelScript.prompt('/compact'))
  const compactStatus = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(JSON.stringify(compactStatus.requests.find(record => record.stepIndex === 1)?.body)).toContain('/compact')
  await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'The slash text reached the model.' }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)

  await sendMessage(page, modelScript.prompt('Continue the prior conversation.'))
  const continued = await modelScript.waitForSteps(3)
  await waitForAgentIdle(page)
  const nextBody = JSON.stringify(continued.requests.find(record => record.stepIndex === 2)?.body)
  expect(nextBody).toContain(priorAnswer)
  expect(nextBody).toContain('Continue the prior conversation.')
  expect(continued.requests.filter(record => record.stepIndex !== undefined)).toHaveLength(3)
}
