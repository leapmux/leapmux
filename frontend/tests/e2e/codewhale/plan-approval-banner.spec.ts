import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, messageContents, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'

codewhaleTest('enforces the actual Plan policy and changes to Agent without a plan review request', async ({ native }) => {
  const { page } = native
  await chooseSettingsOption(page, 'codewhale_mode-plan')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
  await expectNoPlanReview(native, {
    relatedProof: async () => {
      const planned = await runNativeToolTurn(native, {
        toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'no-review-plan', 'printf "PLANREFUSAL%s\\n" "$((40 + 2))"')],
        prompt: 'Check the native Plan policy before execution.',
        answer: 'The native Plan policy refused shell execution.',
      })
      expect(nativeToolResult(planned.resultRequest, 'no-review-plan')).toContain('not available in Plan mode')
      await chooseSettingsOption(page, 'codewhale_mode-agent')
      await chooseSettingsOption(page, 'permissionMode-full_access')
      await waitForSettingsIdle(page)
      await expectSettingsChip(page, 'Agent')
      const executed = await runNativeToolTurn(native, {
        toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'no-review-agent', 'printf "PLANEXECUTION%s\\n" "$((40 + 2))"')],
        prompt: 'Execute the same operation after the native mode change.',
        answer: 'The actual Agent mode executed the shell command.',
      })
      expect(nativeToolResult(executed.resultRequest, 'no-review-agent')).toContain('PLANEXECUTION42')
      await expect(messageContents(page).filter({ hasText: 'PLANEXECUTION42' }).first()).toBeVisible()
    },
    afterReload: async () => {
      await waitForSettingsHydrated(page)
      await expectSettingsChip(page, 'Agent')
    },
  })
})
