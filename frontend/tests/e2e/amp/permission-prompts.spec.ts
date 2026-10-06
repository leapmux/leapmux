import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolOutcome } from '../helpers/nativeScenario'
import { bashToolCall, editToolCall } from '../helpers/providerToolCalls'
import { chatText, controlButton, enterControlFeedback, expectNoControlBanner, sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * The test answers real native permission requests. Allow executes the tool. Deny must reach the next native model request as a refusal.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 *
 * Amp delegates tool permissions to the Worker's helper. Ask raises a banner. Allow All answers at once. Workspace settings must not bypass Ask. Guarded files still follow these rules.
 */
ampTest.describe('Amp permissions', () => {
  ampTest('runs a command after the reader allows it', async ({ askingAmpWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingAmpWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'allow-call', 'echo "amp-$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('echo "amp-$((40 + 2))"')
        await expect(banner).toContainText('shell_command')
      },
      nativeProof: async (request) => {
        expect((await nativeToolOutcome(context, request, 'allow-call')).text).toContain('amp-42')
      },
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect.poll(() => chatText(page)).toContain('amp-42')
      },
    })
  })

  ampTest('refuses a command with the reader\'s reason, which reaches the model', async ({ askingAmpWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingAmpWorkspace.workspaceId })
    const reason = 'Use the clean target instead.'
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'deny-call', 'echo "amp-$((50 + 5))"'),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('echo "amp-$((50 + 5))"')
        // Text in the composer turns Deny into Send feedback, which refuses with it.
        await enterControlFeedback(page, reason)
        await expect(controlButton(page, 'deny')).toHaveText('Send feedback')
      },
      // The Worker's helper refused with the reason, and Amp handed the reason to the model as the result of the
      // call. The command never ran.
      nativeProof: request => expect(JSON.stringify(request.body)).toContain(reason),
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect.poll(() => chatText(page)).toContain(reason)
        expect(await chatText(page)).not.toContain('amp-55')
      },
    })
  })

  ampTest('refuses a turn while the workspace settings allow every call', async ({ askingAmpWorkspace, page, modelScript }) => {
    const settings = join(askingAmpWorkspace.workingDir, '.amp', 'settings.json')
    mkdirSync(join(askingAmpWorkspace.workingDir, '.amp'), { recursive: true })
    writeFileSync(settings, JSON.stringify({ 'amp.dangerouslyAllowAll': true }))

    await sendMessage(page, modelScript.prompt('Run the refused command.'))
    const failed = page.getByTestId(/^queued-input-/).filter({ hasText: 'Failed' })
    await expect(failed).toContainText('amp.dangerouslyAllowAll')
    await expect(failed).toContainText('Allow All')
    // The agent refused before Amp started, so no model call happened.
    expect((await modelScript.status()).requests).toHaveLength(0)
    await expectNoControlBanner(page)
  })

  ampTest('asks before an edit of a file that Amp guards', async ({ askingAmpWorkspace, page, modelScript, leapmuxServer }) => {
    const envFile = join(askingAmpWorkspace.workingDir, '.env')
    writeFileSync(envFile, 'TOKEN=old\n')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingAmpWorkspace.workspaceId })
    // Amp's own guard would ask through a dialog that stream-JSON mode cannot
    // answer, and the session would end. The banner asks instead.
    await exerciseNativePermissionDecision(context, {
      toolCall: editToolCall(context.provider, 'guarded-edit', { path: envFile, before: 'TOKEN=old', after: 'TOKEN=new' }),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('apply_patch')
        expect(readFileSync(envFile, 'utf8')).toBe('TOKEN=old\n')
      },
      nativeProof: async () => {
        await expect.poll(() => readFileSync(envFile, 'utf8')).toBe('TOKEN=new\n')
      },
    })
  })
})
