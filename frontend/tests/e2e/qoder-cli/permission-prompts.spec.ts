import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow, expectSavedRefusalFeedback } from '../helpers/nativePermission'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { answerControl, expectNoControlBanner, savedControlAnswer, sendMessage, toolRows, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'
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
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect(savedControlAnswer(page)).toHaveText('Allow')
      },
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
    await expect(savedControlAnswer(page)).toHaveText('Deny')
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
  // The reason rides in the `message` and `reason` of Qoder's own decision, and Qoder hands it to the model.
  qoderTest('a denied write does not run, and the typed reason reaches the model', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId })
    const marker = join(askingQoderWorkspace.workingDir, 'qoder-denied-marker')
    await exerciseNativePermissionReason(context, {
      toolCall: bashToolCall(context.provider, 'deny-call', 'printf x > qoder-denied-marker'),
      route: 'native-reply',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('qoder-denied-marker')
        expect(existsSync(marker)).toBe(false)
      },
      expectNotRun: () => expect(existsSync(marker), 'the denied command never ran').toBe(false),
      viewProof: async (reason) => {
        await expectNoControlBanner(page)
        await expectSavedRefusalFeedback(page, reason)
      },
    })
  })

  // The Allow scope group offers the session tier the runtime itself keeps: the
  // answer carries Qoder's `permissionScope: "session"`, the runtime derives the
  // session rule, and the same command in the next turn raises no banner at any
  // time. A session rule lives in the session alone, so no rule file needs
  // restoring.
  qoderTest('a session allow covers the same command in the next turn', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId })
    const proof = join(askingQoderWorkspace.workingDir, 'qoder-session-proof.txt')
    // Each run appends, so the file states how many runs happened.
    const command = 'printf qoder-session >> qoder-session-proof.txt'
    await exerciseRememberedAllow(context, {
      scope: 'Session',
      firstCall: bashToolCall(context.provider, 'qoder-session-first', command),
      secondCall: bashToolCall(context.provider, 'qoder-session-second', command),
      beforeDecision: () => expect(existsSync(proof)).toBe(false),
      firstProof: () => expect(readFileSync(proof, 'utf8')).toBe('qoder-session'),
      secondProof: () => expect(readFileSync(proof, 'utf8')).toBe('qoder-sessionqoder-session'),
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Allow'),
    })
  })
})
