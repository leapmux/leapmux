import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { chooseQuestionOption } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { applyPermissionPreset, assistantBubbles, sendMessage, userBubbles, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'
import { mimoChildTurn } from './childScenario'

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--timeout',
    '20s',
  ]
}

mimoTest('returns the original exact reply while a later native question is open', async ({ native, page, leapmuxServer, modelScript }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the first readiness check.'
  const answer = 'The first readiness check completed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'mimo-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const nextPrompt = 'Ask me which checklist to use.'
  const nextAnswer = 'The second checklist is selected.'
  const step = await modelScript.queue({
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'mimo-cli-checklist', [
        {
          question: 'Which checklist should I use?',
          header: 'Checklist',
          options: [
            { label: 'Alpha', description: 'Use the first checklist.' },
            { label: 'Beta', description: 'Use the second checklist.' },
          ],
        },
      ]),
    ],
  }, nativeTextStep(native, nextAnswer))
  const second = sendAndWait(agentId, modelScript.prompt(nextPrompt), 'mimo-checklist')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  const banner = await waitForControlBanner(page)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })

  await chooseQuestionOption('Beta')(banner)
  await modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextAnswer })).toHaveCount(1)
})

mimoTest('returns only the reply after the root steer was delivered', async ({ native, page, leapmuxServer, modelScript }) => {
  await waitForSettingsHydrated(page)
  await applyPermissionPreset(page, 'bypass')
  const { id: agentId, workingDir } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const output = createToolOutputControl(workingDir)
  const before = 'I will run the checklist command.'
  const after = 'The revised checklist passed.'
  const prompt = 'Include the revised checklist.'
  await modelScript.queue({
    text: before,
    toolCalls: [
      bashToolCall(native.provider, 'mimo-cli-steer-boundary', output.command),
    ],
  }, nativeTextStep(native, after))
  const root = [
    ...sendAndWait(agentId, modelScript.prompt('Run the controlled checklist command.'), 'mimo-root-initial'),
    '--all',
  ]
  const steer = [
    ...sendAndWait(agentId, modelScript.prompt(prompt), 'mimo-root-steer'),
    '--all',
  ]
  const deliverSteer = async () => {
    await output.waitForFirstOutput()
    return await Promise.all([
      runCLI(cli, steer) as Promise<{ input_id: string }>,
      (async () => {
        const row = queuedInputRow(page, prompt)
        await steerButton(row).click()
        await expect(row).toHaveCount(0)
        await output.releaseFirstOutput()
        await output.waitForSecondOutput()
        await output.releaseFinalOutput()
      })(),
    ])
  }
  const [initial, [result]] = await withCleanup(async () => await Promise.all([
    runCLI(cli, root),
    deliverSteer(),
  ]), async () => {
    await output.releaseFirstOutput()
    await output.releaseFinalOutput()
  })
  expect(initial).toMatchObject({
    turn_outcome: 'completed',
    replies: [before, after],
  })
  expect(result).toMatchObject({
    id: agentId,
    turn_outcome: 'completed',
    replies: [after],
  })
  expect(await runCLI(cli, steer)).toMatchObject({
    input_id: result.input_id,
    turn_outcome: 'completed',
    replies: [after],
  })
  await expect(assistantBubbles(page).filter({ hasText: before })).toHaveCount(1)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

mimoTest('returns the exact reply of a steer delivered to a child', async ({ native, page, leapmuxServer, modelScript }) => {
  await waitForSettingsHydrated(page)
  await applyPermissionPreset(page, 'bypass')
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'mimo-cli-child-tool'
  const childTask = 'Answer the revised checklist after the shell command.'
  const prompt = 'Use the revised child checklist.'
  const childAnswer = '**Status**: success\n**Summary**: replied\n\nThe child verified the revised checklist.'
  await modelScript.rule({
    name: 'the child runs its first tool',
    when: mimoChildTurn(childTask),
    respond: {
      gate,
      toolCalls: [
        bashToolCall(native.provider, 'mimo-cli-child-shell', 'printf mimo-cli-child-ready'),
      ],
    },
    once: true,
  }, {
    name: 'the child reads the queued CLI input',
    when: {
      body: ['mimo-cli-child-ready', prompt],
    },
    respond: { text: childAnswer },
    once: true,
  })
  await modelScript.queue({
    toolCalls: [
      spawnSubagentToolCall(native.provider, 'mimo-cli-spawn-child', {
        description: 'Check the revised checklist',
        prompt: modelScript.prompt(childTask),
      }),
    ],
  }, nativeTextStep(native, 'The child finished its work.'))
  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Spawn a child to check the checklist.'))
    await modelScript.waitForGate(gate)
    const row = await requireRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', 'running')
    const childId = await openChildTabFromRow(page, row)
    const steer = sendAndWait(childId, modelScript.prompt(prompt), 'mimo-child-steer')
    const [result] = await Promise.all([
      runCLI(cli, steer) as Promise<{ input_id: string }>,
      (async () => {
        const queued = queuedInputRow(page, prompt)
        await steerButton(queued).click()
        await expect(queued).toHaveCount(0)
        await modelScript.releaseGate(gate)
      })(),
    ])
    expect(result).toMatchObject({
      id: childId,
      turn_outcome: 'completed',
      replies: [childAnswer],
    })
    expect(await runCLI(cli, steer)).toMatchObject({
      input_id: result.input_id,
      turn_outcome: 'completed',
      replies: [childAnswer],
    })
    await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

mimoTest('delivers a new prompt to an idle child and waits for its own question', async ({ native, page, leapmuxServer, modelScript }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: rootId } = await currentNativeAgent(native)
  const task = 'Summarize the initial child checklist.'
  const prompt = 'Ask which checklist the idle child should use.'
  const question = 'Which checklist should the child use?'
  const answer = 'The child selected the second checklist.'
  await modelScript.rule({
    name: 'the child finishes its initial task',
    when: mimoChildTurn(task),
    respond: { text: '**Status**: success\n**Summary**: ready\n\nThe first checklist passed.' },
    once: true,
  }, {
    name: 'the idle child asks its own question',
    when: { user: prompt },
    respond: {
      toolCalls: [
        askUserQuestionToolCall(native.provider, 'mimo-idle-child-question', [
          {
            question,
            header: 'Checklist',
            options: [
              { label: 'Alpha', description: 'Use the first checklist.' },
              { label: 'Beta', description: 'Use the second checklist.' },
            ],
          },
        ]),
      ],
    },
    once: true,
  }, {
    name: 'the child reads the selected answer',
    when: {
      body: [prompt, 'Beta'],
    },
    respond: { text: answer },
    once: true,
  })
  const rootStep = await modelScript.queue({
    toolCalls: [
      spawnSubagentToolCall(native.provider, 'mimo-cli-idle-child', {
        description: 'Summarize the first checklist',
        prompt: modelScript.prompt(task),
      }),
    ],
  }, nativeTextStep(native, 'The initial child report is ready.'))
  await sendMessage(page, modelScript.prompt('Spawn a child to check the first checklist.'))
  const row = await requireRegistryRow(page)
  await expect(row).toHaveAttribute('data-status', 'succeeded')
  await modelScript.waitForSteps(rootStep + 2)
  await waitForAgentIdle(page)
  const childId = await openChildTabFromRow(page, row)
  const send = sendAndWait(childId, modelScript.prompt(prompt), 'mimo-idle-child-question')
  const waiting = await runCLI(cli, send) as { input_id: string }
  expect(waiting).toMatchObject({ id: childId, message: 'delivered', state: 'waiting_for_user' })
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText(question)
  expect(await runCLI(cli, ['agent', 'requests', rootId])).toMatchObject({ requests: [] })
  const { requests } = await runCLI(cli, ['agent', 'requests', childId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  await runCLI(cli, [
    'agent',
    'answer',
    childId,
    requests[0]!.id,
    '--option',
    '2',
  ])
  expect(await runCLI(cli, send)).toMatchObject({
    id: childId,
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  await expect(row).toHaveAttribute('data-status', 'succeeded')
})
