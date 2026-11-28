import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { nativeUserStrings } from '../helpers/attachmentModelProbe'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { bashToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow } from '../helpers/steer'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, userBubbles } from '../helpers/ui'
import { waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { runningChild } from './scenarios'

function childSendAndWait(childId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    childId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--all',
    '--timeout',
    '20s',
  ]
}

deepseekHarnessTest('returns exact replies for continuable child sends and retries during later child work', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const child = await runningChild(native)
  await child.finish()
  expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)

  const prompt = 'DEEPSEEKCHILDCONTINUE keep the earlier child file context.'
  const firstAnswer = 'The same native child accepted its next prompt.'
  const firstRule = 'the continuable child returns its first follow-up reply'
  await modelScript.rule({
    name: firstRule,
    when: { user: prompt },
    respond: { text: firstAnswer },
    once: true,
  })
  const first = childSendAndWait(child.childId, modelScript.prompt(prompt), 'deepseek-child-idle-first')
  const firstResult = await runCLI(cli, first) as { input_id: string }
  expect(firstResult).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [firstAnswer],
  })
  const firstRequest = (await modelScript.status()).requests.find(request => request.rule === firstRule)
  if (!firstRequest)
    throw new Error('The DeepSeek Harness continuable child made no request for its follow-up prompt.')
  const userContent = nativeUserStrings(firstRequest.body).join('\n')
  expect(userContent).toContain(prompt)
  expect(userContent).toContain('NATIVE_CHILD_FILE')
  await expect(userBubbles(page).filter({ hasText: prompt }).first()).toBeVisible()
  await waitForDeepseekHarnessChildReport(native, child.childId, child.parentId, 2)

  // Retry the first keyed request while a later child turn is held open at a tool gate.
  const secondGate = 'deepseek-child-later-gate'
  const secondPrompt = 'DEEPSEEKCHILDLATER run a child command and wait.'
  const secondAnswer = 'The later child turn completed.'
  await modelScript.rule({
    name: 'the later child turn runs a shell command',
    when: { user: secondPrompt },
    respond: {
      toolCalls: [
        bashToolCall(native.provider, 'deepseek-child-later-shell', 'printf child-later-ok'),
      ],
    },
    once: true,
  }, {
    name: 'the later child turn holds before finishing',
    when: {
      body: [secondPrompt, 'child-later-ok'],
    },
    respond: { text: secondAnswer, gate: secondGate },
    once: true,
  })
  const second = childSendAndWait(child.childId, modelScript.prompt(secondPrompt), 'deepseek-child-idle-second')
  const retryFirstWhileSecondRuns = async () => {
    try {
      await modelScript.waitForGate(secondGate)
      expect(await runCLI(cli, first)).toMatchObject({
        input_id: firstResult.input_id,
        turn_outcome: 'completed',
        replies: [firstAnswer],
      })
    }
    finally {
      await modelScript.releaseGateIfHeld(secondGate)
    }
  }
  const [secondResult] = await Promise.all([
    runCLI(cli, second),
    retryFirstWhileSecondRuns(),
  ])
  expect(secondResult).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [secondAnswer],
  })
  await expect(assistantBubbles(page).filter({ hasText: secondAnswer })).toHaveCount(1)
})

deepseekHarnessTest('returns only the post-steer reply for a running continuable child', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const child = await runningChild(native)
  await child.finish()
  expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)

  const gate = 'deepseek-child-steer-gate'
  const leadPrompt = 'DEEPSEEKCHILDLEAD run the command before reading a steer.'
  const steerPrompt = 'DEEPSEEKCHILDSTEER include the steered note.'
  const before = 'The child command is about to run.'
  const after = 'The steered child update completed.'
  await modelScript.rule({
    name: 'the child lead starts a tool turn and holds for steering',
    when: { user: leadPrompt },
    respond: {
      text: before,
      gate,
      toolCalls: [
        bashToolCall(native.provider, 'deepseek-child-steer-shell', 'printf child-steer-ok'),
      ],
    },
    once: true,
  }, {
    name: 'the child answers after reading the steer',
    when: { user: steerPrompt },
    respond: { text: after },
    once: true,
  })

  const lead = childSendAndWait(child.childId, modelScript.prompt(leadPrompt), 'deepseek-child-steer-lead')
  const steer = childSendAndWait(child.childId, modelScript.prompt(steerPrompt), 'deepseek-child-steer-follow')
  const deliverSteer = async () => {
    await modelScript.waitForGate(gate)
    return await Promise.all([
      runCLI(cli, steer),
      withCleanup(async () => {
        const row = queuedInputRow(page, steerPrompt)
        await row.getByRole('button', { name: 'Steer', exact: true }).click()
        await expect(row.and(page.locator(`:not([data-input-state="${AgentInputState.AWAITING_ECHO}"])`))).toHaveCount(0)
        expect(await row.count()).toBe(1)
      }, async () => {
        await modelScript.releaseGateIfHeld(gate)
      }),
    ])
  }
  const [leadResult, [steerResult]] = await Promise.all([
    runCLI(cli, lead),
    deliverSteer(),
  ])
  expect(leadResult).toMatchObject({
    turn_outcome: 'completed',
    replies: [before, after],
  })
  expect(steerResult).toMatchObject({
    turn_outcome: 'completed',
    replies: [after],
  })
  expect(await runCLI(cli, steer)).toMatchObject({
    input_id: (steerResult as { input_id: string }).input_id,
    turn_outcome: 'completed',
    replies: [after],
  })
})

deepseekHarnessTest('reports an interrupted continuable child send and keeps the child continuable', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const child = await runningChild(native)
  await child.finish()
  expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)

  const gate = 'deepseek-child-interrupt-gate'
  const interruptedPrompt = 'DEEPSEEKCHILDINTERRUPT hold the child turn until interrupted.'
  await modelScript.rule({
    name: 'the continuable child holds until interrupted',
    when: { user: interruptedPrompt },
    respond: { text: 'This partial child reply will be interrupted.', gate },
    once: true,
  })

  const interrupted = childSendAndWait(child.childId, modelScript.prompt(interruptedPrompt), 'deepseek-child-interrupt')
  const interruptWhenHeld = async () => {
    try {
      await modelScript.waitForGate(gate)
      await runCLI(cli, ['agent', 'interrupt', child.childId])
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }
  }
  const [interruptedResult] = await Promise.all([
    runCLI(cli, interrupted),
    interruptWhenHeld(),
  ])
  expect(interruptedResult).toMatchObject({
    message: 'delivered',
    turn_outcome: 'interrupted',
  })
  expect(await runCLI(cli, interrupted)).toMatchObject({
    input_id: (interruptedResult as { input_id: string }).input_id,
    turn_outcome: 'interrupted',
  })

  await runCLI(cli, [
    'agent',
    'queue',
    'resume',
    child.childId,
  ])
  const afterPrompt = 'DEEPSEEKCHILDAFTERINT confirm the child remains continuable.'
  const afterAnswer = 'The continuable child recovered after interrupt.'
  await modelScript.rule({
    name: 'the continuable child completes after an earlier interrupt',
    when: { user: afterPrompt },
    respond: { text: afterAnswer },
    once: true,
  })
  const afterSend = childSendAndWait(child.childId, modelScript.prompt(afterPrompt), 'deepseek-child-after-interrupt')
  expect(await runCLI(cli, afterSend)).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [afterAnswer],
  })
  await expect(assistantBubbles(page).filter({ hasText: afterAnswer })).toHaveCount(1)
})
