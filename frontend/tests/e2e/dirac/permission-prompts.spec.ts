import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatText, controlButton, expectNoControlBanner, savedControlAnswer } from '../helpers/ui'
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
        // The saved row reads the name of Dirac's own option.
        await expect(savedControlAnswer(page)).toHaveText('Approve once')
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
        await expect(savedControlAnswer(page)).toHaveText('Reject once')
      },
    })
  })

  // The ACP reply selects an option, and an option carries no text. The reason follows as the reader's next message.
  diracTest('sends the reader\'s typed refusal reason as the next message', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
    await exerciseNativePermissionReason(context, {
      toolCall: bashToolCall(context.provider, 'dirac-reason', 'echo "dirac-reason-$(printf 42)"'),
      route: 'next-message',
      expectNotRun: async () => expect(await chatText(page)).not.toContain('dirac-reason-42'),
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Reject once'),
    })
  })

  // Dirac's request offers "Always approve", and Dirac writes the command as an allow rule to
  // `<workspace>/.dirac/permissions.json`. In the asking posture of this suite the rule never covers a later call:
  // without a permission decision binding, Dirac 0.5.17 requires an approval unless the rule check AND its own
  // auto-approver both pass, and the auto-approver passes only its fixed list of read-only commands. So the same command
  // asks again in the next turn, and no remembered-allow test exists for Dirac.
})
