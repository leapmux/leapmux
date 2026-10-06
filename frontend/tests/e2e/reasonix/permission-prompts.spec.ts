import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectNoControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixPlanAndNormalWrites } from './planWriteScenario'

function requireWorkingDir(path: string | undefined): string {
  if (!path)
    throw new Error('the provider workspace must have a working directory')
  return path
}

reasonixTest('permission-prompts: refuses a native write in Plan mode and asks in Normal mode', async ({ native }) => {
  await exerciseReasonixPlanAndNormalWrites(native)
})

reasonixTest('asks before a shell command writes a file in ask mode', async ({ native, authenticatedReasonixWorkspace }) => {
  const { page } = native
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  const file = join(requireWorkingDir(authenticatedReasonixWorkspace.workingDir), 'reasonix-permission-probe.txt')
  const command = 'printf permission-approved > reasonix-permission-probe.txt'
  await exerciseNativePermissionDecision(native, {
    toolCall: bashToolCall(native.provider, 'reasonix-permission', command),
    decision: 'allow',
    beforeDecision: async (banner) => {
      await expect(banner).toContainText(command)
      expect(existsSync(file)).toBe(false)
    },
    nativeProof: () => {
      expect(readFileSync(file, 'utf8')).toBe('permission-approved')
    },
    viewProof: () => expectNoControlBanner(page),
  })
})
