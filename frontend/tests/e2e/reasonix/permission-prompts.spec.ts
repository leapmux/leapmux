import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectNoControlBanner, savedControlAnswer, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

function requireWorkingDir(path: string | undefined): string {
  if (!path)
    throw new Error('the provider workspace must have a working directory')
  return path
}

/** Select the Ask approval, under which Reasonix asks before a shell command writes a file. */
async function askBeforeWrites(context: ManagedNativeScenarioContext): Promise<void> {
  await chooseSettingsOption(context.page, 'tool_approval-ask')
  await waitForSettingsIdle(context.page)
}

reasonixTest('asks before a shell command writes a file in ask mode', async ({ native, authenticatedReasonixWorkspace }) => {
  const { page } = native
  await askBeforeWrites(native)
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
    viewProof: async () => {
      await expectNoControlBanner(page)
      // The saved row reads the name of Reasonix's own option.
      await expect(savedControlAnswer(page)).toHaveText('Allow')
    },
  })
})

// The ACP reply selects an option, and an option carries no text. The reason follows as the reader's next message,
// which opens a turn of its own after the refused turn.
reasonixTest('refuses a shell write in ask mode and sends the typed reason as the next message', async ({ native, authenticatedReasonixWorkspace }) => {
  await askBeforeWrites(native)
  const file = join(requireWorkingDir(authenticatedReasonixWorkspace.workingDir), 'reasonix-refused-probe.txt')
  const command = 'printf permission-refused > reasonix-refused-probe.txt'
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'reasonix-refusal', command),
    route: 'next-message',
    beforeDecision: banner => expect(banner).toContainText(command),
    expectNotRun: () => expect(existsSync(file)).toBe(false),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Reject'),
  })
})

// Reasonix's request offers to allow the command for the session, and Reasonix keeps that rule, so the same command
// later runs with no request.
reasonixTest('a session answer covers the same command in the next turn', async ({ native, authenticatedReasonixWorkspace }) => {
  await askBeforeWrites(native)
  const file = join(requireWorkingDir(authenticatedReasonixWorkspace.workingDir), 'reasonix-session.txt')
  // Each run appends the marker, so the file states how many runs happened.
  const command = 'printf reasonix-session >> reasonix-session.txt'
  await exerciseRememberedAllow(native, {
    scope: 'Session',
    firstCall: bashToolCall(native.provider, 'reasonix-session-first', command),
    secondCall: bashToolCall(native.provider, 'reasonix-session-second', command),
    beforeDecision: () => expect(existsSync(file)).toBe(false),
    firstProof: () => expect(readFileSync(file, 'utf8')).toBe('reasonix-session'),
    secondProof: () => expect(readFileSync(file, 'utf8')).toBe('reasonix-sessionreasonix-session'),
  })
})
