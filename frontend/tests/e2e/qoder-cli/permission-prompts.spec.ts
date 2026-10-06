import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { answerControl, controlButton, enterControlFeedback, expectNoControlBanner, sendMessage, toolRows, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'
import { expectQoderModeChip, nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI control requests', () => {
  qoderTest('raises a banner for a tool call and runs it once allowed', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId })
    const output = join(askingQoderWorkspace.workingDir, 'qoder-control-probe.txt')
    const command = 'printf hi > ./qoder-control-probe.txt'
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'call-1', command),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('qoder-control-probe.txt')
        expect(existsSync(output)).toBe(false)
      },
      nativeProof: () => {
        expect(readFileSync(output, 'utf8')).toBe('hi')
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })

  qoderTest('keeps a denied write out of the workspace', async ({ askingQoderWorkspace, page, modelScript }) => {
    const output = join(askingQoderWorkspace.workingDir, 'qoder-control-denied.txt')
    const command = 'printf denied > ./qoder-control-denied.txt'
    const start = await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.QODER, 'denied-call', command)] })
    // The fallback answers any request after the denial. This test proves only that the denied command never runs,
    // so it does not state whether the runtime asks the model again.
    await modelScript.fallback({ text: 'The denied call ended.' })
    await sendMessage(page, modelScript.prompt(`Try ${command}.`))
    // Wait for the native model request before you check the banner.
    // Agent startup can exceed the banner assertion deadline.
    await modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('qoder-control-denied.txt')
    expect(existsSync(output)).toBe(false)
    await answerControl(page, 'deny')
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(output)).toBe(false)
  })
})

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest('runs a read-only shell command with no banner in Default mode', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    const { workingDir } = askingQoderWorkspace
    await waitForSettingsHydrated(page)
    await expectQoderModeChip(page, 'Default')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId })

    // The observation fails if a banner shows at any time in the turn.
    let followUp: MockModelRequestRecord | undefined
    await expectNoNativeControl(context, {
      testId: 'control-banner',
      relatedProof: async () => {
        ({ resultRequest: followUp } = await runNativeToolTurn(context, {
          toolCalls: [bashToolCall(context.provider, 'read-only', 'pwd')],
          prompt: 'Run pwd.',
          answer: 'The command ran.',
        }))
      },
    })

    await expectNoControlBanner(page)
    expect(followUp?.body).toMatchObject({
      messages: expect.arrayContaining([
        expect.objectContaining({ role: 'tool', tool_call_id: 'read-only', content: workingDir }),
      ]),
    })
    await expect(toolRows(page).filter({ hasText: workingDir }).first()).toBeVisible()
  })
})

qoderTest.describe('Qoder CLI control answers', () => {
  qoderTest('a denied write does not run, and the typed reason dismisses the banner', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId })
    const marker = join(askingQoderWorkspace.workingDir, 'qoder-denied-marker')
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'deny-call', 'printf x > qoder-denied-marker'),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('qoder-denied-marker')
        expect(existsSync(marker)).toBe(false)
        // Typing turns the deny button into "Send feedback", which sends the typed
        // text as the denial reason.
        await enterControlFeedback(page, 'the probe is not wanted here')
        await expect(controlButton(page, 'deny')).toHaveText('Send feedback')
      },
      nativeProof: () => {
        expect(existsSync(marker), 'the denied command never ran').toBe(false)
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })
})
