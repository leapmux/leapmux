import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace, savedControlAnswer, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { junieTest } from '../junie-fixtures'
import { JUNIE_AGENT, nativeContext } from './scenarios'

// Junie's permission request offers only `Yes` (allow_once) and `No` (reject_once). It offers no remembered scope, so
// the banner draws no scope pills and no allow-always test exists for Junie.
junieTest.describe('Junie control requests', () => {
  junieTest('an allowed command runs, and its output reaches the chat', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'off' } })
    const output = join(workingDir, 'junie-allow-out.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    // The context answers through Junie's own answer tool, as the model of a Junie turn does.
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'junie-allow', `echo "junie-$((40 + 2))" | tee ${output}`),
      decision: 'allow',
      beforeDecision: () => expect(existsSync(output)).toBe(false),
      nativeProof: () => {
        expect(readFileSync(output, 'utf8')).toContain('junie-42')
      },
      viewProof: async () => {
        await expect(messageBubbles(page).filter({ hasText: 'junie-42' }).first()).toBeVisible()
        // Junie names each option after its own ID, so the saved row reads the word of the kind.
        await expect(savedControlAnswer(page)).toHaveText('Allow once')
      },
    })
  })

  junieTest('a denied command does not run', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'off' } })
    const output = join(workingDir, 'junie-deny-out.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'junie-deny', `echo "junie-should-not-run" | tee ${output}`),
      decision: 'deny',
      nativeProof: (request) => {
        expect(existsSync(output)).toBe(false)
        expect(JSON.stringify(request.body)).toContain('Human rejected execution')
      },
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Reject'),
    })
  })

  // The ACP reply selects an option, and an option carries no text. The reason follows as the reader's next message.
  junieTest('sends the reader\'s typed refusal reason as the next message', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'off' } })
    const output = join(workingDir, 'junie-reason-out.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseNativePermissionReason(context, {
      toolCall: bashToolCall(context.provider, 'junie-reason', `echo "junie-should-not-run" | tee ${output}`),
      route: 'next-message',
      expectNotRun: () => expect(existsSync(output)).toBe(false),
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Reject'),
    })
  })
})
