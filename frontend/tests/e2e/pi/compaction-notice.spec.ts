import { expect } from '@playwright/test'
import { expectCompactionNotice } from '../helpers/compaction'
import { chatScrollContainer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

const OLD_CONTEXT_MARKER = 'LEAPMUXOLDCONTEXTPIRIVER'

const SUMMARY_MARKER = 'Summary: the earlier Pi turns established the topic.'

piTest('replaces a failed native compaction start with its error', async ({ authenticatedPiWorkspace, page }) => {
  void authenticatedPiWorkspace
  await sendMessage(page, '/compact')
  const chat = chatScrollContainer(page)
  await expect(chat).toContainText('Nothing to compact (session too small)')
  await waitForAgentIdle(page)

  await page.reload()
  await expect(chat).toContainText('Nothing to compact (session too small)')
  await expect(chat).not.toContainText('Compacting context...')
})

piTest('draws and keeps the native notice after a manual compaction', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await modelScript.rule({
    name: 'pi-native-compaction',
    when: { system: '^You are a context summarization assistant\\.' },
    respond: { text: SUMMARY_MARKER },
  })
  await modelScript.queue(
    { text: 'The first Pi turn ended.' },
    { text: 'The second Pi turn ended.' },
    { text: 'The third Pi turn ended.' },
  )
  await sendMessage(page, modelScript.prompt(`Keep ${OLD_CONTEXT_MARKER} in the older context.`))
  await modelScript.waitForSteps(1)
  await waitForAgentIdle(page)
  await sendMessage(page, modelScript.prompt('Add another turn before compaction.'))
  const before = await modelScript.waitForSteps(2)
  expect(JSON.stringify(before.requests.find(request => request.stepIndex === 1)?.body)).toContain(OLD_CONTEXT_MARKER)
  await waitForAgentIdle(page)
  await sendMessage(page, modelScript.prompt('Add the current turn before compaction.'))
  await modelScript.waitForSteps(3)
  await waitForAgentIdle(page)

  await sendMessage(page, '/compact')
  await expectCompactionNotice(page)
  const compacted = await modelScript.status()
  expect(compacted.ruleMatches['pi-native-compaction']).toBeGreaterThan(0)
  await waitForAgentIdle(page)

  await page.reload()
  await expectCompactionNotice(page)
  await modelScript.queue({ text: 'The compacted Pi session continued.' })
  await sendMessage(page, modelScript.prompt('Continue after compaction.'))
  const status = await modelScript.waitForSteps(4)
  const nextRequest = JSON.stringify(status.requests.find(request => request.stepIndex === 3)?.body)
  expect(nextRequest).toContain(SUMMARY_MARKER)
  expect(nextRequest).not.toContain(OLD_CONTEXT_MARKER)
  await waitForAgentIdle(page)
})
