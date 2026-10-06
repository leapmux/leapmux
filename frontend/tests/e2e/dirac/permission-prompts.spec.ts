import { diracTest, expect } from '../dirac-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatText, controlButton, expectNoControlBanner } from '../helpers/ui'
import { nativeContext } from './scenarios'

diracTest.describe('Dirac control requests', () => {
  diracTest('runs a command after the reader approves it', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
    // The context answers through Dirac's own respond tool, as the model of a Dirac turn does.
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'dirac-allow', 'echo "dirac-allow-$(printf 42)"'),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('dirac-allow')
        await expect(controlButton(page, 'allow')).toHaveText('Allow')
      },
      // The command computes the marker, so only a run prints it.
      nativeProof: request => expect(JSON.stringify(request.body)).toContain('dirac-allow-42'),
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect.poll(() => chatText(page)).toContain('dirac-allow-42')
      },
    })
  })

  diracTest('keeps the command from running after the reader rejects it', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'dirac-deny', 'echo "dirac-deny-$(printf 42)"'),
      decision: 'deny',
      beforeDecision: () => expect(controlButton(page, 'deny')).toHaveText('Deny'),
      // A denied call reaches no shell, so its output marker is nowhere in the model request
      // or on the page. The command text itself never states the number below.
      nativeProof: request => expect(JSON.stringify(request.body)).not.toContain('dirac-deny-42'),
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect.poll(() => chatText(page)).not.toContain('dirac-deny-42')
      },
    })
  })
})
