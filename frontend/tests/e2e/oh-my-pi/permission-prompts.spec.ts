import { expect } from '@playwright/test'

import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatText, expectNoControlBanner } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'
import { nativeContext } from './scenarios'

/**
 * The test answers real native permission requests. Allow executes the tool. Deny must reach the next native model request as a refusal.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Oh My Pi sends an `extension_ui_request` select dialog before execution. Approve and Deny become the shared Allow and Deny controls.
 */
ohMyPiTest.describe('Oh My Pi control requests', () => {
  ohMyPiTest('runs a command after the reader allows it', async ({ askingOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingOhMyPiWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'approve-call', 'echo "omp-$((40 + 2))"'),
      decision: 'allow',
      // omp asks before the call runs. The banner states the command that omp asks about.
      beforeDecision: banner => expect(banner).toContainText('echo "omp-$((40 + 2))"'),
      // The command text states no `omp-42`, so only the command's own output can
      // put it into the result or on the page.
      nativeProof: request => expect(nativeToolResult(request, 'approve-call')).toContain('omp-42'),
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect.poll(() => chatText(page)).toContain('omp-42')
      },
    })
  })

  ohMyPiTest('refuses a command that the reader denies', async ({ askingOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingOhMyPiWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'deny-call', 'echo "omp-$((50 + 5))"'),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('echo "omp-$((50 + 5))"'),
      // omp fails the call with its own words, and the command never runs.
      nativeProof: (request) => {
        const result = nativeToolResult(request, 'deny-call')
        expect(result).toContain('Tool call denied by user')
        expect(result).not.toContain('omp-55')
      },
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect.poll(() => chatText(page)).toContain('Tool call denied by user')
        expect(await chatText(page)).not.toContain('omp-55')
      },
    })
  })
})
