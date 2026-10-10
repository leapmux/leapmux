import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow, expectSavedRefusalFeedback } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, savedControlAnswer, waitForSettingsIdle } from '../helpers/ui'

for (const decision of ['allow', 'deny'] as const) {
  claudeTest(`returns the actual native ${decision} decision before a protected file changes`, async ({ native }) => {
    await chooseSettingsOption(native.page, 'permissionMode-default')
    await waitForSettingsIdle(native.page)
    const file = join(createTestDirectory('claude-native-permission-'), 'protected.txt')
    const callId = `native-permission-${decision}`
    const command = `printf 'PERMISSION%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, callId, command),
      decision,
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      nativeProof: (request) => {
        const result = nativeToolResult(request, callId)
        if (decision === 'allow') {
          expect(result).toContain('PERMISSION42')
          expect(readFileSync(file, 'utf8')).toBe('PERMISSION42\n')
        }
        else {
          expect(result).toMatch(/denied|permission|rejected/i)
          expect(existsSync(file)).toBe(false)
        }
      },
      // Claude's answer is the neutral envelope, so the shared reader states the word of the button.
      viewProof: () => expect(savedControlAnswer(native.page)).toHaveText(decision === 'allow' ? 'Allow' : 'Deny'),
    })
  })
}

// The reason rides in the `message` of Claude's own permission answer, and Claude hands it to the model as the result
// of the refused call.
claudeTest('hands the reader\'s typed refusal reason to the model as the result of the call', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const file = join(createTestDirectory('claude-native-permission-'), 'refused.txt')
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'native-permission-reason', `printf refused > ${quotePosixShellArgument(file)}`),
    route: 'native-reply',
    expectNotRun: () => expect(existsSync(file)).toBe(false),
    viewProof: reason => expectSavedRefusalFeedback(native.page, reason),
  })
})

// The Allow scope group offers the session tier: the answer carries the
// `updatedPermissions` grant Claude Code keeps in memory for the session -- one
// allow rule for the whole Bash tool. A LATER command of the same tool must
// raise no banner at any time, which also proves the rule is tool-wide rather
// than command-typed. A session destination writes no rule file.
claudeTest('a session allow covers the tool for the rest of the session', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const dir = createTestDirectory('claude-native-session-')
  const firstFile = join(dir, 'first.txt')
  const secondFile = join(dir, 'second.txt')
  await exerciseRememberedAllow(native, {
    scope: 'Session',
    firstCall: bashToolCall(native.provider, 'native-session-first', `printf one > ${quotePosixShellArgument(firstFile)}`),
    secondCall: bashToolCall(native.provider, 'native-session-second', `printf two > ${quotePosixShellArgument(secondFile)}`),
    beforeDecision: () => expect(existsSync(firstFile)).toBe(false),
    firstProof: () => expect(readFileSync(firstFile, 'utf8')).toBe('one'),
    secondProof: () => expect(readFileSync(secondFile, 'utf8')).toBe('two'),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow'),
  })
})
