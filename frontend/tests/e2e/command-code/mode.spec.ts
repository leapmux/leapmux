import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { currentNativeAgent, nativeModelContextText, nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption, exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { expectSettingsChip, waitForNativeSettingsHydrated } from '../helpers/ui'

commandCodeTest('applies the native planning mode and preserves context across its process restart', async ({ native: context }) => {
  await sendNativeAnswer(context, 'Keep COMMANDCODE_MODE_CONTEXT before the native process restarts.', 'COMMANDCODE_MODE_ANSWER')
  await exerciseNativeOption(context, { groupId: 'permissionMode', value: 'plan', nativeProof: (request) => {
    expect(nativeModelInstructionText(request)).toMatch(/plan[\s\S]*(?:read-only|read only)/i)
    expect(nativeModelContextText(request)).toContain('COMMANDCODE_MODE_ANSWER')
  } })
})

commandCodeTest('returns from Plan to Default and proves both native modes after reload', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  await expectSettingsOptionsOffered(native.page, 'permissionMode', ['default', 'plan', 'bypass'])
  await expectSettingsChip(native.page, 'Bypass')
  await sendNativeAnswer(native, 'Keep the mode sequence context.', 'COMMANDCODE_MODE_SEQUENCE_CONTEXT')
  const agent = await currentNativeAgent(native)
  let defaultProbe = 0
  for (const mode of [
    { value: 'plan', label: 'Plan', previousLabel: 'Bypass' },
    { value: 'default', label: 'Default', previousLabel: 'Plan' },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, { groupId: 'permissionMode', ...mode, noticeLabel: 'Permissions' })
    const nativeProof = async (request: MockModelRequestRecord) => {
      const instruction = nativeModelInstructionText(request)
      if (mode.value === 'plan')
        expect(instruction).toMatch(/plan[\s\S]*(?:read-only|read only)/i)
      else
        expect(instruction).not.toMatch(/plan[\s\S]*(?:read-only|read only)/i)
      expect(nativeModelContextText(request)).toContain('COMMANDCODE_MODE_SEQUENCE_CONTEXT')
      if (mode.value === 'default') {
        const callId = `native-default-mode-${defaultProbe++}`
        const path = join(agent.workingDir, `${callId}.txt`)
        expect(existsSync(path)).toBe(false)
        await expectNoNativeControl(native, {
          testId: 'control-banner',
          relatedProof: async () => {
            const { resultRequest } = await runNativeToolTurn(native, {
              toolCalls: [writeToolCall(native.provider, callId, { path, content: 'The Default write must not run.\n' })],
              prompt: 'Run the supplied file write in the selected setting.',
              answer: `The native Default write ${defaultProbe} was refused.`,
              permissions: 'none',
            })
            const refusal = nativeToolResult(resultRequest, callId)
            expect(refusal).toContain('requires permissions')
            expect(refusal).toContain('--yolo')
            expect(existsSync(path)).toBe(false)
          },
        })
      }
    }
    await nativeProof(await sendNativeAnswer(native, 'Reply once with the current native setting.', `Command Code used ${mode.label} mode.`))
    await exerciseRestoredNativeOption(native, { groupId: 'permissionMode', value: mode.value, nativeProof })
    await expectSettingsChip(native.page, mode.label)
    await notice.assertStored()
  }
})
