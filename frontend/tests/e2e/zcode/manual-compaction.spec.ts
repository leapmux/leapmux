import { expect } from '@playwright/test'
import { expectCompactionNotice } from '../helpers/compaction'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

const OLD_CONTEXT_MARKER = 'LEAPMUXOLDCONTEXTZCODERIVER'

const SUMMARY_MARKER = 'ZCode summary retained the topic.'

zcodeTest('keeps the native summary and the completed notice after reload', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await modelScript.queue(
    { text: 'The first ZCode turn ended.' },
    { text: 'The second ZCode turn ended.' },
    { text: 'The third ZCode turn ended.' },
    { text: SUMMARY_MARKER },
  )
  for (const [index, prompt] of [`Keep ${OLD_CONTEXT_MARKER} in the older context.`, 'Add another ZCode context turn.', 'Finish the earlier ZCode context.'].entries()) {
    await sendMessage(page, modelScript.prompt(prompt))
    const status = await modelScript.waitForSteps(index + 1)
    if (index === 1) {
      expect(JSON.stringify(status.requests.find(request => request.stepIndex === index)?.body)).toContain(OLD_CONTEXT_MARKER)
    }
    await waitForAgentIdle(page)
  }

  await sendMessage(page, '/compact')
  await modelScript.waitForSteps(4)
  await expectCompactionNotice(page)
  await waitForAgentIdle(page)
  await page.reload()
  await expectCompactionNotice(page)

  await modelScript.queue({ text: 'The compacted ZCode session continued.' })
  await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
  const continued = await modelScript.waitForSteps(5)
  const nextRequest = JSON.stringify(continued.requests.find(request => request.stepIndex === 4)?.body)
  expect(nextRequest).toContain(SUMMARY_MARKER)
  expect(nextRequest).not.toContain(OLD_CONTEXT_MARKER)
  await waitForAgentIdle(page)
})
