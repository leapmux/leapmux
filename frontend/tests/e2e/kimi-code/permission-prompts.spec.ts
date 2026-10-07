import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow, expectSavedRefusalFeedback } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectNoControlBanner, expectSettingsChip, savedControlAnswer, toolRows, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('answers Kimi Code approvals', () => {
  // Always Ask is the default. It asks before each command.
  // A command that must run calculates its output marker. For example, `kimi-allowed-output-$((40 + 2))` produces `kimi-allowed-output-42`.
  // The row header and the model call contain the command text. A marker already in that text cannot prove execution.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')
  })

  kimiTest('an allowed command runs, and its output reaches the model', async ({ native }) => {
    const command = 'echo "kimi-allowed-output-$((40 + 2))"'
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'allow-call', command),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('Bash')
        await expect(banner).toContainText(command)
      },
      // The result of the allowed call carries the calculated marker.
      nativeProof: request => expect(nativeToolResult(request, 'allow-call')).toContain('kimi-allowed-output-42'),
      viewProof: async () => {
        await expectNoControlBanner(native.page)
        await expect(toolRows(native.page).filter({ hasText: 'kimi-allowed-output-42' }).first()).toBeVisible()
        await expect(savedControlAnswer(native.page)).toHaveText('Allow')
      },
    })
  })

  kimiTest('a denied command does not run, and the model learns of the denial', async ({ native, authenticatedKimiWorkspace }) => {
    const marker = join(authenticatedKimiWorkspace.workingDir, 'kimi-denied-marker')
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'deny-call', 'touch kimi-denied-marker'),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('touch kimi-denied-marker')
        expect(existsSync(marker)).toBe(false)
      },
      // Kimi Code returns the denial as the tool result and asks the model again.
      // The result is the text of toolApprovalService.formatApprovalRejectionMessage.
      // The check reads the result of the denied call. The call ID alone is no proof,
      // because the model's own tool call repeats it in every later request.
      nativeProof: (request) => {
        expect(nativeToolResult(request, 'deny-call')).toContain('was not run because the user rejected the approval request')
        expect(existsSync(marker), 'the denied command never ran in the working directory').toBe(false)
      },
      viewProof: async () => {
        await expectNoControlBanner(native.page)
        await expect(savedControlAnswer(native.page)).toHaveText('Deny')
      },
    })
  })

  // The reason rides in the feedback of Kimi Code's own rejection, and Kimi Code adds it to the result of the call.
  kimiTest('a typed refusal reason reaches the model with the denial', async ({ native, authenticatedKimiWorkspace }) => {
    const marker = join(authenticatedKimiWorkspace.workingDir, 'kimi-reason-marker')
    await exerciseNativePermissionReason(native, {
      toolCall: bashToolCall(native.provider, 'reason-call', 'touch kimi-reason-marker'),
      route: 'native-reply',
      beforeDecision: banner => expect(banner).toContainText('touch kimi-reason-marker'),
      expectNotRun: () => expect(existsSync(marker)).toBe(false),
      viewProof: reason => expectSavedRefusalFeedback(native.page, reason),
    })
  })

  // The session scope adds a rule to the kap-server session, so the same
  // command in a later turn runs with no banner at all.
  //
  // The command has no quotes, unlike the commands above. A quoted command
  // that holds a parenthesis is impossible here, because Kimi Code never
  // matches the rule that it keeps for it. Kimi keeps the rule as
  // `Bash(<command>)`, with a backslash before each parenthesis, and matches
  // it with picomatch. Picomatch reads a double quote in a rule as a quoting
  // mark, and after an escaped parenthesis it drops the closing quote. So the
  // rule of `echo "x-$((40 + 2))"` never matches that command, and Kimi asks
  // again in the next turn, where no reader answers.
  kimiTest('an approval for the session covers the same command in the next turn', async ({ native }) => {
    const command = 'echo kimi-session-scope-$((40 + 2))'
    const marker = 'kimi-session-scope-42'
    await exerciseRememberedAllow(native, {
      scope: 'Session',
      firstCall: bashToolCall(native.provider, 'scope-first', command),
      secondCall: bashToolCall(native.provider, 'scope-second', command),
      // The result of each call carries the marker. A refusal prints no marker.
      firstProof: request => expect(nativeToolResult(request, 'scope-first'), 'the first run printed the marker').toContain(marker),
      secondProof: request => expect(nativeToolResult(request, 'scope-second'), 'the second run printed the marker').toContain(marker),
      viewProof: async () => {
        await expect(toolRows(native.page).filter({ hasText: marker })).not.toHaveCount(0)
        await expect(savedControlAnswer(native.page)).toHaveText('Allow for this session')
      },
    })
  })
})
