import { expect } from '@playwright/test'
import { compactionIndicator, compactionNoticeRows, expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseFailedNativeCompaction, OLDER_CONTEXT_MARKER } from '../helpers/manualCompaction'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeScenarioModelContextText } from '../helpers/nativeScenario'
import { interruptButton, messageContents, resumePausedQueue, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { exerciseLettaCompaction, LETTA_COMPACTION_OPTIONS } from './compactionScenarios'

lettaTest('stores one completed notice for actual native compaction', async ({ native }) => {
  await exerciseLettaCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})

lettaTest('shows the native summarizer failure and preserves context for the next turn', async ({ native }) => {
  await exerciseFailedNativeCompaction(native, LETTA_COMPACTION_OPTIONS)
  await expect(compactionIndicator(native.page)).toHaveCount(0)
  await expect(compactionNoticeRows(native.page)).toHaveCount(0)
  await expect(messageContents(native.page).filter({ hasText: 'Compaction failed' }).filter({ hasText: 'probe compaction refused' })).toHaveCount(1)
  await expect(messageContents(native.page).filter({ hasText: 'probe compaction refused' })).toHaveCount(1)
  const next = await sendNativeAnswer(native, 'Continue after compaction was refused.', 'The session recovered after compaction failed.')
  expect(nativeScenarioModelContextText(native, next)).toContain(OLDER_CONTEXT_MARKER)
  await native.page.reload()
  await waitForAgentIdle(native.page)
  await expect(messageContents(native.page).filter({ hasText: 'Compaction failed' }).filter({ hasText: 'probe compaction refused' })).toHaveCount(1)
})

lettaTest('reports the native interruption restriction and completes the held compaction', async ({ native }) => {
  const before = await currentNativeAgent(native)
  const gate = 'letta-compaction-interrupt'
  await exerciseLettaCompaction(native, {
    summaryGate: gate,
    onHeld: async () => {
      await expect(compactionIndicator(native.page)).toBeVisible()
      await interruptButton(native.page).click()
      await expect(native.page.getByText('the Letta Code process cannot interrupt compaction', { exact: false })).toBeVisible()
      expect((await native.modelScript.status()).pendingGates).toContain(gate)
      await expect(compactionIndicator(native.page)).toBeVisible()
      await resumePausedQueue(native.page)
    },
  })
  expect((await currentNativeAgent(native)).agentSessionId).toBe(before.agentSessionId)
  await expectCompactionNoticeAfterReload(native.page)
})
