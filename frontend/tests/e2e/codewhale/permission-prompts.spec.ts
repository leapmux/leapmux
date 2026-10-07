import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'

import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectNoControlBanner, savedControlAnswer, toolRows } from '../helpers/ui'

codewhaleTest.describe('Codewhale approvals', () => {
  // The Ask posture asks before a command that writes. A read-only command runs
  // without a banner, which is why each command below creates a file.
  //
  // Each command that must RUN prints a number that its own text does not state,
  // such as `approved-42` from `approved-$((40 + 2))`. The row's header shows the
  // command, so a marker that the command text holds matches the row whether or
  // not the command ran.
  codewhaleTest('runs a command that the reader allows', async ({ native }) => {
    const approved = join((await currentNativeAgent(native)).workingDir, 'approved.txt')
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'allow-call', 'touch approved.txt && echo "approved-$((40 + 2))"'),
      decision: 'allow',
      // The approval states no arguments of its own. The banner draws the command
      // from the call that the runtime reported before it asked.
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('touch approved.txt')
        expect(existsSync(approved)).toBe(false)
      },
      nativeProof: (request) => {
        expect(existsSync(approved)).toBe(true)
        expect(nativeToolResult(request, 'allow-call')).toContain('approved-42')
      },
      viewProof: async () => {
        await expectNoControlBanner(native.page)
        await expect(toolRows(native.page).filter({ hasText: 'approved-42' }).first()).toBeVisible()
        await expect(savedControlAnswer(native.page)).toHaveText('Allow')
      },
    })
  })

  codewhaleTest('refuses a command that the reader denies', async ({ native }) => {
    const denied = join((await currentNativeAgent(native)).workingDir, 'denied.txt')
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'deny-call', 'touch denied.txt'),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('touch denied.txt'),
      // The runtime fails the call, and the model reads why.
      nativeProof: (request) => {
        expect(nativeToolResult(request, 'deny-call')).toContain('denied by user')
        expect(existsSync(denied)).toBe(false)
      },
      viewProof: async () => {
        await expectNoControlBanner(native.page)
        await expect(savedControlAnswer(native.page)).toHaveText('Deny')
      },
    })
  })

  // The approval route of the runtime carries no reason. The Worker queues the reason as the reader's next message,
  // which opens a turn of its own after the refused turn.
  codewhaleTest('sends the reader\'s typed refusal reason as the next message', async ({ native }) => {
    const denied = join((await currentNativeAgent(native)).workingDir, 'reason-denied.txt')
    await exerciseNativePermissionReason(native, {
      toolCall: bashToolCall(native.provider, 'reason-call', 'touch reason-denied.txt'),
      route: 'next-message',
      beforeDecision: banner => expect(banner).toContainText('touch reason-denied.txt'),
      expectNotRun: () => expect(existsSync(denied)).toBe(false),
      // The saved row states the decision alone, because the reason is the row of the next message.
      viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Deny'),
    })
  })
})
