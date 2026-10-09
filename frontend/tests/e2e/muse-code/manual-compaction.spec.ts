/**
 * A user /compact command cannot start a native Muse compaction: the installed host
 * answers `compaction_unavailable`, so LeapMux sends the command to the model as an
 * ordinary message instead of a summarizer turn.
 */
import { expect } from '@playwright/test'
import { nativeModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('manual-compaction: the refused native compact reaches the model as text', async ({ native }) => {
  const { page, modelScript } = native
  const context = 'MANUALCOMPACT cedar detail for the manual compaction probe.'
  const start = await modelScript.queue(nativeTextStep(native, `Noted: ${context}`))
  await sendMessage(page, modelScript.prompt('Record the manual compaction context.'))
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)

  const after = await modelScript.queue(nativeTextStep(native, 'The manual compact command reached the model as text.'))
  await sendMessage(page, '/compact')
  await modelScript.waitForSteps(after + 1)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(after)
  expect(nativeModelContextText(request)).toContain('/compact')
  expect(nativeModelContextText(request)).toContain(context)
})
