import { join } from 'node:path'
import { expect } from '@playwright/test'
import { GEMINI_MODE } from '../../../src/generated/contracts/gemini-protocol'
import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision, exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { exerciseGeminiAutoEditWrite } from './scenarios'

geminiTest('changes native permission modes and restores the actual file permission behavior', async ({ native, page }) => {
  await waitForSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(GEMINI_MODE))
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  await exerciseNativePermissionWrite(native)
  const automaticNotice = await chooseNativeModeWithNotice(native, {
    groupId: 'permissionMode',
    value: GEMINI_MODE.AutoEdit,
    label: 'Auto Edit',
    previousLabel: 'Default',
    noticeLabel: 'Mode',
  })
  const approveShellWrite = async (restored: boolean) => {
    const agent = await currentNativeAgent(native)
    const callId = `gemini-autoedit-shell-${restored}`
    const filename = `${callId}.txt`
    // Gemini blocks shell arithmetic as command substitution. The operation calculates the output in Node.js.
    const operation = await createNativePermissionFileWrite(native, {
      fileName: filename,
      callId,
      outputPrefix: 'GEMINI_AUTOEDIT_SHELL_',
    })
    const command = operation.toolCall.arguments?.command
    if (typeof command !== 'string')
      throw new Error('The native Gemini shell write requires a command.')
    await operation.beforeDecision()
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, callId, `${command} && cat ${quotePosixShellArgument(join(agent.workingDir, filename))}`),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText(filename)
        await operation.beforeDecision()
      },
      nativeProof: operation.nativeProof,
    })
  }
  await exerciseGeminiAutoEditWrite(native)
  await approveShellWrite(false)
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Auto Edit')
  await expectSettingsOptionChosen(page, 'permissionMode-autoEdit')
  await exerciseGeminiAutoEditWrite(native)
  await approveShellWrite(true)
  await automaticNotice.assertStored()

  const defaultNotice = await chooseNativeModeWithNotice(native, {
    groupId: 'permissionMode',
    value: GEMINI_MODE.Default,
    label: 'Default',
    previousLabel: 'Auto Edit',
    noticeLabel: 'Mode',
  })
  await exerciseNativePermissionWrite(native)
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Default')
  await expectSettingsOptionChosen(page, 'permissionMode-default')
  await exerciseNativePermissionWrite(native)
  await defaultNotice.assertStored()
  await automaticNotice.assertStored()
})

geminiTest('keeps default permissions when model text resembles a native mode update', async ({ native, page }) => {
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  await sendNativeAnswer(native, 'Return the scripted model text.', '[MODE_UPDATE] yolo')
  await expectSettingsOptionChosen(page, 'permissionMode-default')
  await exerciseNativePermissionWrite(native)
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Default')
  await expectSettingsOptionChosen(page, 'permissionMode-default')
})
