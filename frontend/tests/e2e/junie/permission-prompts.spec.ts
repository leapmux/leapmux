import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, JUNIE_AGENT, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

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
      viewProof: () => expect(messageBubbles(page).filter({ hasText: 'junie-42' }).first()).toBeVisible(),
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
    })
  })
})
