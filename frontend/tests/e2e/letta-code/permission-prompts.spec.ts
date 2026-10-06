import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatText, expectNoControlBanner, savedControlAnswer } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code control requests', () => {
  lettaTest('runs a command after the reader allows it', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'allow-call', 'echo "letta-$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: banner => expect(banner).toContainText('Bash'),
      // The command text states no `letta-42`, so only the command's own output can put it into the result.
      nativeProof: request => expect(nativeToolResult(request, 'allow-call')).toContain('letta-42'),
      viewProof: async () => {
        await expectNoControlBanner(page)
        // The saved row reads Letta's own approval_response decision, not a generic answer word.
        await expect(savedControlAnswer(page)).toHaveText('Allow')
        await expect.poll(() => chatText(page)).toContain('letta-42')
      },
    })
  })

  lettaTest('keeps the command from running after the reader denies it', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
    const output = join(askingLettaWorkspace.workingDir, 'letta-deny-out.txt')
    await exerciseNativePermissionDecision(context, {
      // `tee` writes a file, so Letta does not treat the call as a READ-ONLY
      // shell command: those auto-approve in every mode but `strict` and no
      // banner would ever appear. If the call wrongly runs, its stdout is the
      // same marker text, so the assertion below still catches it.
      toolCall: bashToolCall(context.provider, 'deny-call', 'echo "letta-should-not-run" | tee letta-deny-out.txt'),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('Bash')
        expect(existsSync(output)).toBe(false)
      },
      // The model receives exactly one result for the denied call, and the command never wrote its file. The reader
      // fails unless the request holds exactly one result for the call. The proof does not search the result text for
      // the marker, because the command text holds the same marker.
      nativeProof: (request) => {
        nativeToolResult(request, 'deny-call')
        expect(existsSync(output)).toBe(false)
      },
      viewProof: async () => {
        // The command never reached the shell, so its output is nowhere on the page.
        await expect.poll(() => chatText(page)).not.toContain('letta-should-not-run')
        await expectNoControlBanner(page)
        // A denial with no reason keeps the decision word alone.
        await expect(savedControlAnswer(page)).toHaveText('Deny')
      },
    })
  })
})
