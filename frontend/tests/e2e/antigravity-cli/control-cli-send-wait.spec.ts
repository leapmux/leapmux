import type { Page } from '@playwright/test'
import type { CLIConfigDir } from '../helpers/cli'
import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { userBubbles, waitForControlBanner } from '../helpers/ui'
import { finishAntigravityChildWithReload, openAntigravityRunningChild } from './childScenarios'

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

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    ...keyedSend(agentId, prompt, requestId),
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
}

async function steerKeyedSend(page: Page, cli: CLIConfigDir, args: string[], preview: string): Promise<void> {
  await runCLI(cli, args)
  const row = queuedInputRow(page, preview)
  await steerButton(row).click()
  await expectSteerAccepted(page, row)
}

antigravityTest('proves both steers in one native execution and excludes earlier text', async ({ native, page, modelScript, leapmuxServer }) => {
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Include the revised checklist.'
  const text = modelScript.prompt(prompt)
  const before = 'The original checklist was ready.'
  const after = 'The revised checklist was verified.'
  const gate = 'antigravity-cli-steer-progress'
  const initial = sendAndWait(id, modelScript.prompt('Check the original checklist.'), 'antigravity-steer-original')
  const keys = ['antigravity-steer-first', 'antigravity-steer-second']
  await modelScript.queue({
    text: before,
    stream: {
      chunkChars: 12,
      delayMs: 0,
      gates: [
        { afterChunk: 1, name: gate },
      ],
    },
  }, {
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'antigravity-after-steer-question', [NATIVE_CONTROL_QUESTION]),
    ],
  }, { text: after })

  const [waiting] = await withCleanup(async () => await Promise.all([
    runCLI(cli, initial),
    (async () => {
      await modelScript.waitForGate(gate)
      for (const key of keys)
        await steerKeyedSend(page, cli, keyedSend(id, text, key), prompt)
      await modelScript.releaseGate(gate)
    })(),
  ]), async () => { await modelScript.releaseGateIfHeld(gate) })
  expect(waiting).toMatchObject({
    state: 'waiting_for_user',
    replies: [before],
  })
  await expect(await waitForControlBanner(page)).toContainText(NATIVE_CONTROL_QUESTION.question)
  const steers = keys.map(key => sendAndWait(id, text, key))
  const pending = await Promise.all(steers.map(args => runCLI(cli, args))) as Array<{ input_id: string }>
  for (const result of pending)
    expect(result).toMatchObject({ state: 'waiting_for_user', replies: [] })
  expect(pending[0]!.input_id).not.toBe(pending[1]!.input_id)

  const { requests } = await runCLI(cli, ['agent', 'requests', id]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  await runCLI(cli, [
    'agent',
    'answer',
    id,
    requests[0]!.id,
    '--option',
    '2',
  ])
  expect(await runCLI(cli, initial)).toMatchObject({
    turn_outcome: 'completed',
    replies: [before, after],
  })
  for (const [index, args] of steers.entries()) {
    expect(await runCLI(cli, args)).toMatchObject({
      input_id: pending[index]!.input_id,
      turn_outcome: 'completed',
      replies: [after],
    })
  }
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(2)
})

antigravityTest('retrieves an unread steer after the next execution consumes it', async ({ native, page, modelScript, leapmuxServer }) => {
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Read the carried checklist revision.'
  const text = modelScript.prompt(prompt)
  const gate = 'antigravity-cli-carried-steer'
  const reply = 'The carried checklist was verified.'
  const key = 'antigravity-carried-steer'
  const start = await modelScript.queue({
    text: 'Partial reply before the interrupt.',
    stream: {
      chunkChars: 12,
      delayMs: 0,
      gates: [
        { afterChunk: 1, name: gate },
      ],
    },
  }, { text: reply })

  await withCleanup(async () => {
    await runCLI(cli, keyedSend(id, modelScript.prompt('Begin the interrupted checklist.'), 'antigravity-interrupted-original'))
    await modelScript.waitForGate(gate)
    await steerKeyedSend(page, cli, keyedSend(id, text, key), prompt)
    await runCLI(cli, ['agent', 'interrupt', id])
    await runCLI(cli, [
      'agent',
      'wait',
      id,
      '--until',
      'idle',
      '--timeout',
      '25s',
    ])
    await runCLI(cli, ['agent', 'queue', 'resume', id])
  }, async () => { await modelScript.releaseGateIfHeld(gate) })

  const next = sendAndWait(id, modelScript.prompt('Resume with the revised checklist.'), 'antigravity-carried-next')
  expect(await runCLI(cli, next)).toMatchObject({
    turn_outcome: 'completed',
    replies: [reply],
  })
  expect(nativeModelContextText(await modelScript.requestAt(start + 1))).toContain(prompt)
  expect(await runCLI(cli, sendAndWait(id, text, key))).toMatchObject({
    turn_outcome: 'completed',
    replies: [reply],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

antigravityTest('proves the reply after a steer reaches a running native child', async ({ native, page, modelScript, leapmuxServer }) => {
  const child = await openAntigravityRunningChild(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Read the revised child checklist.'
  const text = modelScript.prompt(prompt)
  const reply = 'The running child verified the revision.'
  const key = 'antigravity-running-child-steer'
  await modelScript.rule({
    name: 'the running child answers its steer',
    when: { user: prompt },
    respond: { text: reply },
    once: true,
  })
  await withCleanup(async () => {
    await openChildTabFromRow(page, child.row)
    await steerKeyedSend(page, cli, keyedSend(child.childId, text, key), prompt)
  }, child.finish)
  await openChildTabFromRow(page, child.row)
  const steer = sendAndWait(child.childId, text, key)
  const result = await runCLI(cli, steer) as { input_id: string }
  const expected = {
    id: child.childId,
    input_id: result.input_id,
    turn_outcome: 'completed',
    replies: [reply],
  }
  expect(result).toMatchObject(expected)
  expect(await runCLI(cli, steer)).toMatchObject(expected)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

antigravityTest('retrieves the exact reply while a later input runs in the same native child', async ({ native, page, modelScript, leapmuxServer }) => {
  const child = await openAntigravityRunningChild(native)
  await finishAntigravityChildWithReload(native, child)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Verify the child checklist.'
  const text = modelScript.prompt(prompt)
  const firstReply = 'The child verified the alpha checklist.'
  const secondReply = 'The child verified the beta checklist.'
  const first = sendAndWait(child.childId, text, 'antigravity-child-alpha')
  const second = sendAndWait(child.childId, text, 'antigravity-child-beta')

  await modelScript.rule({
    name: 'the child returns its first checklist reply',
    when: { user: prompt },
    respond: { text: firstReply },
    once: true,
  })
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    id: child.childId,
    turn_outcome: 'completed',
    replies: [firstReply],
  })

  const gate = 'antigravity-cli-child-second'
  await modelScript.rule({
    name: 'the child holds its second checklist reply',
    when: { user: prompt },
    respond: { text: secondReply, gate },
    once: true,
  })
  const [next] = await withCleanup(async () => await Promise.all([
    runCLI(cli, second) as Promise<{ input_id: string }>,
    (async () => {
      await modelScript.waitForGate(gate)
      await expect(child.row).toHaveAttribute('data-status', 'running')
      expect(await runCLI(cli, first)).toMatchObject({
        input_id: completed.input_id,
        turn_outcome: 'completed',
        replies: [firstReply],
      })
      await modelScript.releaseGate(gate)
    })(),
  ]), async () => { await modelScript.releaseGateIfHeld(gate) })
  expect(next).toMatchObject({
    id: child.childId,
    turn_outcome: 'completed',
    replies: [secondReply],
  })
  expect(next.input_id).not.toBe(completed.input_id)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(2)
})

for (const kind of ['question', 'permission'] as const) {
  antigravityTest(`retrieves each identical input by key across a native ${kind}`, async ({ native, page, modelScript, leapmuxServer }) => {
    const { id } = await currentNativeAgent(native)
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const prompt = 'Check the release checklist.'
    const text = modelScript.prompt(prompt)
    const firstReply = 'The release checklist passed.'
    const secondReply = 'The second checklist choice was recorded.'
    const first = sendAndWait(id, text, 'antigravity-release-first')
    const second = sendAndWait(id, text, 'antigravity-release-second')

    await modelScript.queue({ text: firstReply })
    const completed = await runCLI(cli, first) as { input_id: string }
    expect(completed).toMatchObject({
      turn_outcome: 'completed',
      replies: [firstReply],
    })

    const toolCall = kind === 'question'
      ? askUserQuestionToolCall(native.provider, 'antigravity-cli-question', [NATIVE_CONTROL_QUESTION])
      : bashToolCall(native.provider, 'antigravity-cli-permission', 'printf permission-probe')
    await modelScript.queue({
      toolCalls: [toolCall],
    }, { text: secondReply })
    const waiting = await runCLI(cli, second) as { input_id: string }
    expect(waiting).toMatchObject({ state: 'waiting_for_user', replies: [] })
    expect(waiting.input_id).not.toBe(completed.input_id)
    const banner = await waitForControlBanner(page)
    if (kind === 'question')
      await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
    expect(await runCLI(cli, first)).toMatchObject({
      input_id: completed.input_id,
      turn_outcome: 'completed',
      replies: [firstReply],
    })

    const { requests } = await runCLI(cli, ['agent', 'requests', id]) as {
      requests: Array<{ id: string, kind: string, blocking: boolean }>
    }
    expect(requests).toHaveLength(1)
    const request = requests[0]!
    expect(request).toMatchObject({ kind, blocking: true })
    const answer = kind === 'question'
      ? [
          'agent',
          'answer',
          id,
          request.id,
          '--option',
          '2',
        ]
      : ['agent', 'approve', id, request.id]
    await runCLI(cli, answer)
    expect(await runCLI(cli, second)).toMatchObject({
      input_id: waiting.input_id,
      turn_outcome: 'completed',
      replies: [secondReply],
    })
    await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(2)
  })
}
