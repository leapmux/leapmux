import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { submitGoal } from '../helpers/goalsAndTodos'
import { ruleRequest } from '../helpers/mockModelScript'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall, completeGoalToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { interruptButton, openWorkspace, resumePausedQueue, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { kiroGoalSessionId } from './goalReceipt'
import { KIRO_AGENT, nativeContext } from './scenarios'

function keyedSend(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
  ]
}

const wait = ['--wait', '--all', '--timeout', '20s']

kiroTest('returns the exact reply to a steer injected into the parent’s own turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const workspaceId = authenticatedEmptyWorkspace.workspaceId
  const { workingDir } = await openProviderAgent(leapmuxServer, workspaceId, KIRO_AGENT, {
    optionValues: { policyPreset: 'ask' },
  })
  await openWorkspace(page, workspaceId)
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId,
  })
  const { id: agentId, agentSessionId: sessionId } = await currentNativeAgent(context)
  expect(sessionId).toMatch(/^sess_[\da-f-]+$/)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const priorReply = 'The parent is ready for a goal.'
  const prior = keyedSend(agentId, modelScript.prompt('Prepare the goal workspace.'), 'kiro-parent-primer')
  await modelScript.queue(nativeTextStep(context, priorReply))
  expect(await runCLI(cli, prior.concat(wait))).toMatchObject({
    turn_outcome: 'completed',
    replies: [priorReply],
  })

  const initial = 'The native workflow notification arrived.'
  const guidance = 'Include the release status in the notification.'
  const reply = 'The release is ready for verification.'
  const proof = 'Parent permission allowed.'
  const output = join(workingDir, 'kiro-parent-approval.txt')
  const quotedOutput = quotePosixShellArgument(output)
  const gate = 'hold-kiro-parent-notification'
  const steer = keyedSend(agentId, modelScript.prompt(guidance), 'kiro-parent-steer')
  const parentMatcher = { protocol: 'aws-event-stream', body: `"conversationId"\\s*:\\s*"${sessionId}"` } as const
  await modelScript.rule({
    name: 'complete the goal step',
    when: { protocol: 'aws-event-stream', user: 'original_user_request' },
    respond: {
      toolCalls: [
        completeGoalToolCall(context.provider, 'kiro-parent-goal-done'),
      ],
    },
    once: true,
  }, {
    name: 'request permission in the steered parent turn',
    when: { ...parentMatcher, user: guidance },
    respond: {
      toolCalls: [
        bashToolCall(context.provider, 'kiro-parent-steer-permission', `printf '%s' ${quotePosixShellArgument(proof)} > ${quotedOutput} && cat ${quotedOutput}`),
      ],
    },
    once: true,
  }, {
    name: 'answer the approved parent turn',
    when: { ...parentMatcher, user: proof },
    respond: nativeTextStep(context, reply),
    once: true,
  }, {
    name: 'hold the parent notification',
    when: parentMatcher,
    respond: {
      ...nativeTextStep(context, initial),
      text: initial,
      stream: {
        chunkChars: 10,
        delayMs: 0,
        gates: [
          { afterChunk: 1, name: gate },
        ],
      },
    },
    once: true,
  })
  await modelScript.fallback({ text: 'The goal update was recorded.' })

  await withCleanup(async () => {
    await submitGoal(page, modelScript.prompt('Complete the release verification.'))
    const held = await modelScript.waitForGate(gate)
    expect(kiroGoalSessionId(ruleRequest(held, 'hold the parent notification'))).toBe(sessionId)
    await expect(interruptButton(page)).toBeVisible()
    const { input_id: steerId } = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, guidance)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    const snapshot = await readNativeInputQueue(leapmuxServer, agentId)
    expect(snapshot.items.find(item => item.id === steerId)?.state).toBe(AgentInputState.AWAITING_ECHO)
    await modelScript.releaseGate(gate)
    await waitForControlBanner(page)
    expect(existsSync(output)).toBe(false)

    const received = ruleRequest(await modelScript.status(), 'request permission in the steered parent turn')
    expect(kiroGoalSessionId(received)).toBe(sessionId)
    expect(nativeScenarioModelContextText(context, received)).toContain(initial)
    expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
      input_id: steerId,
      state: 'waiting_for_user',
      replies: [],
    })
    expect(await runCLI(cli, prior.concat(wait))).toMatchObject({
      turn_outcome: 'completed',
      replies: [priorReply],
    })
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      requests[0]!.id,
    ])
    await waitForAgentIdle(page)
    expect(readFileSync(output, 'utf8')).toBe(proof)
    expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
      input_id: steerId,
      turn_outcome: 'completed',
      replies: [reply],
    })
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

kiroTest('returns the exact reply to guidance injected after a native tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const workspaceId = authenticatedEmptyWorkspace.workspaceId
  const { agentId, workingDir } = await openProviderAgent(leapmuxServer, workspaceId, KIRO_AGENT, {
    optionValues: { policyPreset: 'allow-all' },
  })
  await openWorkspace(page, workspaceId)
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId,
  })
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const tool = createToolOutputControl(workingDir)
  const guidance = 'Include the checkpoint label in the report.'
  const answer = 'The checkpoint report is ready.'
  const lead = keyedSend(agentId, modelScript.prompt('Run the controlled checkpoint command.'), 'kiro-tool-lead')
  const steer = keyedSend(agentId, modelScript.prompt(guidance), 'kiro-tool-steer')

  await withCleanup(async () => {
    const start = await modelScript.queue({
      toolCalls: [
        bashToolCall(context.provider, 'kiro-cli-steer-tool', tool.command),
      ],
    }, nativeTextStep(context, answer))
    const { input_id: leadID } = await runCLI(cli, lead) as { input_id: string }
    await modelScript.waitForSteps(start + 1)
    await tool.waitForFirstOutput()
    const { input_id: steerID } = await runCLI(cli, steer) as { input_id: string }
    expect(steerID).not.toBe(leadID)
    const row = queuedInputRow(page, guidance)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)

    await tool.releaseFirstOutput()
    await tool.waitForSecondOutput()
    await tool.releaseFinalOutput()
    await modelScript.waitForSteps(start + 2)
    const request = await modelScript.requestAt(start + 1)
    expect(nativeScenarioModelContextText(context, request)).toContain(guidance)
    await waitForAgentIdle(page)
    expect(await runCLI(cli, lead.concat(wait))).toMatchObject({
      input_id: leadID,
      turn_outcome: 'completed',
      replies: [answer],
    })
    expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
      input_id: steerID,
      turn_outcome: 'completed',
      replies: [answer],
    })
  }, async () => {
    await tool.releaseFirstOutput()
    await tool.releaseFinalOutput()
  })
})

const scenarios = [
  { outcome: 'completed', title: 'keeps text before the injection out of the steer reply' },
  { outcome: 'interrupted', title: 'returns a cleared steer’s reply from the later prompt after a stop' },
  { outcome: 'permission', title: 'reports a permission that blocks the turn after the steer' },
] as const

for (const { outcome, title } of scenarios) {
  const interrupted = outcome === 'interrupted'
  const requiresApproval = outcome === 'permission'
  kiroTest(title, async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    const policyPreset = requiresApproval ? 'ask' : 'allow-all'
    const { agentId, workingDir } = await openProviderAgent(leapmuxServer, workspaceId, KIRO_AGENT, {
      optionValues: { policyPreset },
    })
    await openWorkspace(page, workspaceId)
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId,
    })
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const before = 'The initial report is ready.'
    const answer = 'The checkpoint flag is enabled.'
    const guidance = 'Also check the checkpoint flag.'
    const output = join(workingDir, 'kiro-cli-steer-approved.txt')
    const permissionSteps = requiresApproval
      ? [
          {
            toolCalls: [
              bashToolCall(context.provider, 'kiro-cli-steer-permission', `printf approved > ${quotePosixShellArgument(output)}`),
            ],
          },
        ]
      : []
    const gate = 'hold-first-kiro-reply-for-steer'
    const lead = keyedSend(agentId, modelScript.prompt('Prepare the initial report.'), 'kiro-text-lead')
    const steer = keyedSend(agentId, modelScript.prompt(guidance), 'kiro-text-steer')
    await withCleanup(async () => {
      const start = await modelScript.queue({
        ...nativeTextStep(context, before),
        text: before,
        stream: {
          chunkChars: 10,
          delayMs: 0,
          gates: [
            { afterChunk: 1, name: gate },
          ],
        },
      }, ...permissionSteps, nativeTextStep(context, answer))
      if (interrupted)
        modelScript.allowUnconsumed('The native stop ends the first answer after one chunk.')
      const { input_id: leadID } = await runCLI(cli, lead) as { input_id: string }
      await modelScript.waitForGate(gate)
      const { input_id: steerID } = await runCLI(cli, steer) as { input_id: string }
      const row = queuedInputRow(page, guidance)
      await steerButton(row).click()
      await expectSteerAccepted(page, row)
      if (interrupted) {
        await interruptButton(page).click()
        await resumePausedQueue(page)
      }
      await modelScript.releaseGateIfHeld(gate)
      await modelScript.waitForSteps(start + 2)
      const request = await modelScript.requestAt(start + 1)
      expect(nativeScenarioModelContextText(context, request)).toContain(guidance)
      if (requiresApproval) {
        await waitForControlBanner(page)
        expect(existsSync(output)).toBe(false)
        expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
          input_id: steerID,
          state: 'waiting_for_user',
          replies: [],
        })
        expect(await runCLI(cli, lead.concat(wait))).toMatchObject({
          input_id: leadID,
          state: 'waiting_for_user',
        })
        const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
        expect(requests).toHaveLength(1)
        await runCLI(cli, [
          'agent',
          'approve',
          agentId,
          requests[0]!.id,
        ])
        await modelScript.waitForSteps(start + 3)
        expect(readFileSync(output, 'utf8')).toBe('approved')
      }
      await waitForAgentIdle(page)
      const original = await runCLI(cli, lead.concat(wait)) as { replies: string[], turn_outcome: string }
      expect(original).toMatchObject({
        input_id: leadID,
        turn_outcome: interrupted ? 'interrupted' : 'completed',
      })
      if (interrupted) {
        expect(original.replies.join('')).not.toContain(answer)
      }
      else {
        expect(original.replies.join('')).toContain(before)
        expect(original.replies.at(-1)).toBe(answer)
      }
      expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
        input_id: steerID,
        turn_outcome: 'completed',
        replies: [answer],
      })
    }, async () => { await modelScript.releaseGateIfHeld(gate) })
  })
}
