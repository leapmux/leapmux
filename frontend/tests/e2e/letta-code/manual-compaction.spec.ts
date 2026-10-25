import { expect } from '@playwright/test'
import { compactionIndicator, compactionNoticeRows, expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeScenarioModelContextText } from '../helpers/nativeScenario'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { exerciseLettaCompaction } from './compactionScenarios'

lettaTest.describe('native manual compaction', () => {
  lettaTest('runs the native summarizer and retains its completion after reload', async ({ native }) => {
    await exerciseLettaCompaction(native, {
      summaryGate: 'letta-manual-summary',
      onHeld: async () => {
        await expect(compactionIndicator(native.page)).toBeVisible()
      },
    })
    await expectCompactionNoticeAfterReload(native.page)
  })

  lettaTest('handles native help and invalid arguments without changing model context', async ({ native }) => {
    const answer = 'Preserve LETTA_COMMAND_CONTEXT while testing command help.'
    await sendNativeAnswer(native, 'Remember the command test context.', answer)
    for (const command of [
      { input: '/compact help', output: 'Summarize conversation history' },
      { input: '/compact invalid-native-mode', output: 'Invalid mode "invalid-native-mode"' },
    ]) {
      const before = (await native.modelScript.status()).requests.filter(request => request.stepIndex !== undefined).length
      await sendMessage(native.page, command.input)
      await waitForAgentIdle(native.page)
      await expect(messageContents(native.page).filter({ hasText: command.output })).toHaveCount(1)
      await expect(compactionIndicator(native.page)).toHaveCount(0)
      await expect(compactionNoticeRows(native.page)).toHaveCount(0)
      expect((await native.modelScript.status()).requests.filter(request => request.stepIndex !== undefined)).toHaveLength(before)
    }
    const next = await sendNativeAnswer(native, 'Continue after the native command checks.', 'The command checks preserved the session.')
    expect(nativeScenarioModelContextText(native, next)).toContain(answer)
  })
})
