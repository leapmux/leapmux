import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { answerControl, controlButton, enterControlFeedback, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code control requests', () => {
  codebuddyTest('raises a banner for a tool call and runs it once allowed', async ({ askingCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingCodebuddyWorkspace.workspaceId })
    const output = join(askingCodebuddyWorkspace.workingDir, 'codebuddy-permission-allowed.txt')
    const command = 'printf CODEBUDDY_ALLOWED > ./codebuddy-permission-allowed.txt'
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'call-1', command),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText(command)
        expect(existsSync(output)).toBe(false)
      },
      nativeProof: () => {
        expect(readFileSync(output, 'utf8')).toBe('CODEBUDDY_ALLOWED')
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })

  codebuddyTest('does not run a denied write command', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    const output = join(askingCodebuddyWorkspace.workingDir, 'codebuddy-permission-denied.txt')
    const command = 'printf CODEBUDDY_DENIED > ./codebuddy-permission-denied.txt'
    const start = await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'denied-call', command)] })
    // The fallback answers any request after the denial. This test proves only that the denied command never runs,
    // so it does not state whether the runtime asks the model again.
    await modelScript.fallback({ text: 'The denied call ended.' })
    await sendMessage(page, modelScript.prompt('Try the scripted write command.'))
    // Wait for the native model request before you check the banner.
    // Agent startup can exceed the banner assertion deadline.
    await modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText(command)
    expect(existsSync(output)).toBe(false)
    await answerControl(page, 'deny')
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(output)).toBe(false)
  })
})

codebuddyTest.describe('CodeBuddy Code control answers', () => {
  codebuddyTest('a denied command does not run, and the reason reaches the model', async ({ askingCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingCodebuddyWorkspace.workspaceId })
    const marker = join(askingCodebuddyWorkspace.workingDir, 'codebuddy-denied-marker')
    const reason = 'the probe is not wanted here'
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'deny-call', 'touch codebuddy-denied-marker'),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('touch codebuddy-denied-marker')
        // Typing turns the deny button into "Send feedback", which sends the typed
        // text as the denial reason.
        await enterControlFeedback(page, reason)
        await expect(controlButton(page, 'deny')).toHaveText('Send feedback')
      },
      nativeProof: (request) => {
        expect(existsSync(marker), 'the denied command never ran').toBe(false)
        // The model reads the reason in the request that follows the denial.
        expect(JSON.stringify(request.body)).toContain(reason)
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })
})
