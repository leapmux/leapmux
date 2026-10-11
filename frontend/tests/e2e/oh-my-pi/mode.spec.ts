import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeModelContextText, nativeToolOutcome } from '../helpers/nativeScenario'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, waitForNativeSettingsHydrated, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('applies the native approval mode through a restart and a reload', async ({ native, page }) => {
  await chooseSettingsOption(page, 'permissionMode-always-ask')
  await waitForSettingsIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectSettingsOptionChosen(page, 'permissionMode-always-ask')
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(AgentProvider.OH_MY_PI, `native-mode-${reload}`, 'printf "NATIVEAPPROVAL%s\\n" "$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: banner => expect(banner).toContainText('NATIVEAPPROVAL'),
      nativeProof: request => expect(nativeModelContextText(request)).toContain('NATIVEAPPROVAL42'),
    })
  }
})

ohMyPiTest('returns from Always Ask to Write and proves native approval behavior after reload', async ({ native }) => {
  const { page } = native
  await waitForNativeSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', ['always-ask', 'write', 'yolo'])
  await expectSettingsChip(page, 'Yolo')
  await sendNativeAnswer(native, 'Reply before the approval mode sequence.', 'The Oh My Pi mode sequence started.')
  const agent = await currentNativeAgent(native)
  expect(agent.workingDir).not.toBe('')
  for (const mode of [
    { value: 'always-ask', label: 'Always Ask', previousLabel: 'Yolo' },
    { value: 'write', label: 'Write', previousLabel: 'Always Ask' },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, { groupId: 'permissionMode', ...mode, noticeLabel: 'Approval Mode' })
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await waitForNativeSettingsHydrated(page)
      }
      await expectSettingsChip(page, mode.label)
      await expectSettingsOptionChosen(page, `permissionMode-${mode.value}`)
      const callId = `native-mode-${mode.value}-${reload}`
      const filename = `${callId}.txt`
      const path = join(agent.workingDir, filename)
      const content = `${mode.label} after reload ${reload}.\n`
      expect(existsSync(path)).toBe(false)
      const toolCall = writeToolCall(AgentProvider.OH_MY_PI, callId, { path, content })
      const prove = async (request: MockModelRequestRecord) => {
        expect(readFileSync(path, 'utf8')).toBe(content)
        expect((await nativeToolOutcome(native, request, callId)).text).toContain(filename)
      }
      if (mode.value === 'always-ask') {
        await exerciseNativePermissionDecision(native, {
          toolCall,
          decision: 'allow',
          beforeDecision: async (banner) => {
            await expect(banner).toContainText(filename)
            expect(existsSync(path)).toBe(false)
          },
          nativeProof: prove,
        })
      }
      else {
        await expectNoNativeControl(native, {
          testId: 'control-banner',
          relatedProof: async () => {
            const { resultRequest } = await runNativeToolTurn(native, {
              toolCalls: [toolCall],
              prompt: 'Write the file in Write mode.',
              answer: `The Write mode file ended after reload ${reload}.`,
              permissions: 'none',
            })
            await prove(resultRequest)
          },
        })
        const shellId = `${callId}-shell`
        const command = 'printf "OMPMODE%s\\n" "$((40 + 2))"'
        await exerciseNativePermissionDecision(native, {
          toolCall: bashToolCall(AgentProvider.OH_MY_PI, shellId, command),
          decision: 'allow',
          beforeDecision: banner => expect(banner).toContainText(command),
          nativeProof: async (request) => {
            expect((await nativeToolOutcome(native, request, shellId)).text).toContain('OMPMODE42')
          },
        })
      }
      await notice.assertStored()
    }
  }
})
