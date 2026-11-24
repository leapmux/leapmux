import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { ruleRequest } from '../helpers/mockModelScript'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeScenarioModelContextText, selectedAgentTabId } from '../helpers/nativeScenario'
import { bashToolCall, copilotWriteChildMessageToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand, quotePosixShellArgument } from '../helpers/shellArguments'
import { expectSteerAccepted, steerButton } from '../helpers/steer'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, tabById, userBubbles, waitForControlBanner } from '../helpers/ui'
import { copilotChildTaskMatcher } from './childIdentity'
import { bypassToolRequests } from './scenarios'

// Copilot passes a message only to a subagent that runs in the background: a foreground subagent never reads it. A
// background subagent stays idle after each turn, and the Worker keeps its row paused and its tab open. The Worker sends
// a message from the subagent's tab with session.tasks.sendMessage and an empty fromAgentId so client inputs stay
// disjoint from parent write_agent calls without altering the model prompt. Copilot queues a message to a running
// subagent for the next iteration and starts a turn of an idle subagent for it.
copilotTest('returns exact replies from a background subagent while it runs and while it waits', async ({ native }, testInfo) => {
  const { page, modelScript, leapmuxServer } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  await bypassToolRequests(native)
  const gate = 'copilot-child-send'
  const idleGate = 'copilot-child-later-send'
  const childTask = 'Run the child marker command, then wait for the reader.'
  const steeredRule = 'the child reads the message sent while it ran'
  const idleRule = 'the child reads the message sent while it waited'
  const steerPrompt = 'Also say CHILD_STEERED.'
  const idlePrompt = 'Now say CHILD_AGAIN.'
  const wait = ['--wait', '--all', '--timeout', '12s']
  await modelScript.rule(
    {
      name: 'the child runs its first tool',
      when: copilotChildTaskMatcher(childTask),
      respond: {
        gate,
        toolCalls: [
          bashToolCall(native.provider, 'child-shell', printfMarkerCommand('CHILD_READY_', 42)),
        ],
      },
      once: true,
    },
    {
      name: 'the child answers its tool',
      when: {
        ...copilotChildTaskMatcher(childTask),
        lastMessage: { role: 'tool' },
      },
      respond: { text: 'The child marker command ran.' },
      once: true,
    },
    {
      name: steeredRule,
      when: { user: 'Also say CHILD_STEERED' },
      respond: { text: 'CHILD_STEERED' },
      once: true,
    },
    {
      name: idleRule,
      when: { user: 'Now say CHILD_AGAIN' },
      respond: { text: 'CHILD_AGAIN', gate: idleGate },
      once: true,
    },
  )
  // Copilot tells the parent each time the background subagent goes idle, and the parent answers the notice.
  await modelScript.fallback({ text: 'The background subagent is idle.' })
  const { row, childId, parentId, steers } = await withCleanup(async () => {
    await modelScript.queue(
      {
        toolCalls: [
          spawnSubagentToolCall(native.provider, 'spawn-child-send', {
            description: 'Answer the messages from the child tab',
            prompt: modelScript.prompt(childTask),
            background: true,
          }),
        ],
      },
      { text: 'The child runs in the background.' },
    )
    const parent = await selectedAgentTabId(page)
    await sendMessage(page, modelScript.prompt('Spawn one background subagent for the messages from its tab.'))
    await modelScript.waitForGate(gate)
    const spawned = await requireRegistryRow(page)
    await expect(spawned).toHaveAttribute('data-status', 'running')
    const child = await openChildTabFromRow(page, spawned)
    const steers: Array<{ argv: string[], inputId: string }> = []
    for (const requestId of ['copilot-child-first-steer', 'copilot-child-second-steer']) {
      const argv = [
        'agent',
        'send',
        child,
        steerPrompt,
        '--request-id',
        requestId,
      ]
      const { input_id: inputId } = await runCLI(cli, argv) as { input_id: string }
      const queued = page.getByTestId(`queued-input-${inputId}`)
      await steerButton(queued).click()
      await expectSteerAccepted(page, queued)
      steers.push({ argv, inputId })
    }
    expect(new Set(steers.map(steer => steer.inputId)).size).toBe(2)
    await modelScript.releaseGate(gate)
    return {
      row: spawned,
      childId: child,
      parentId: parent,
      steers,
    }
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })

  await expect.poll(async () => (await modelScript.status()).ruleMatches[steeredRule] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: 'CHILD_STEERED' }).first()).toBeVisible()
  await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(2)
  const received = ruleRequest(await modelScript.status(), steeredRule)
  const receivedContext = nativeScenarioModelContextText(native, received)
  expect(receivedContext.split(steerPrompt)).toHaveLength(3)
  expect(receivedContext).not.toContain('[Message from ')
  // The subagent waits for the next message, so its row stays open.
  await expect(row).toHaveAttribute('data-status', 'paused')

  const idleSend = [
    'agent',
    'send',
    childId,
    idlePrompt,
    '--request-id',
    'copilot-background-child-idle-send',
  ]
  const { idleInputId, leadWhileLaterRuns } = await withCleanup(async () => {
    const { input_id: idleInputId } = await runCLI(cli, idleSend) as { input_id: string }
    await modelScript.waitForGate(idleGate)
    const leadWhileLaterRuns = await runCLI(cli, steers[0]!.argv.concat(wait))
    await modelScript.releaseGate(idleGate)
    return { idleInputId, leadWhileLaterRuns }
  }, async () => {
    await modelScript.releaseGateIfHeld(idleGate)
  })
  await expect.poll(async () => (await modelScript.status()).ruleMatches[idleRule] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: 'CHILD_AGAIN' }).first()).toBeVisible()
  await expect(row).toHaveAttribute('data-status', 'paused')

  // The parent's transcript keeps none of the subagent's answers.
  await tabById(page, parentId).click()
  await expect(assistantBubbles(page).filter({ hasText: /^CHILD_(?:STEERED|AGAIN)$/ })).toHaveCount(0)
  await tabById(page, childId).click()

  const steerReplies = await Promise.all(steers.map(steer => runCLI(cli, steer.argv.concat(wait))))
  const idleReply = await runCLI(cli, idleSend.concat(wait))
  const snapshot = await readNativeMessageSnapshot(native, childId)
  await testInfo.attach('copilot-child-replies', {
    body: JSON.stringify({
      leadWhileLaterRuns,
      steerReplies,
      idleReply,
      frames: snapshot.messages.map(nativeMessageBody),
    }),
    contentType: 'application/json',
  })
  expect(leadWhileLaterRuns).toMatchObject({
    input_id: steers[0]!.inputId,
    turn_outcome: 'completed',
    replies: ['CHILD_STEERED'],
  })
  for (const [index, steer] of steers.entries()) {
    expect(steerReplies[index]).toMatchObject({
      input_id: steer.inputId,
      turn_outcome: 'completed',
      replies: ['CHILD_STEERED'],
    })
  }
  expect(idleReply).toMatchObject({
    input_id: idleInputId,
    turn_outcome: 'completed',
    replies: ['CHILD_AGAIN'],
  })
})

copilotTest('isolates a background subagent steer from an identical parent write_agent message', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  await bypassToolRequests(native)
  const gate = 'copilot-child-overlap'
  const parentGate = 'copilot-parent-overlap'
  const childTask = 'Run the child marker command, then handle overlapping messages.'
  const steeredRule = 'the child reads both the client steer and the parent write_agent message'
  const sharedPrompt = 'Also say CHILD_OVERLAP_OK.'
  const wait = ['--wait', '--all', '--timeout', '12s']
  await modelScript.rule(
    {
      name: 'the child runs its first tool',
      when: copilotChildTaskMatcher(childTask),
      respond: {
        gate,
        toolCalls: [
          bashToolCall(native.provider, 'child-shell-overlap', printfMarkerCommand('CHILD_OVERLAP_', 7)),
        ],
      },
      once: true,
    },
    {
      name: 'the child answers its tool',
      when: {
        ...copilotChildTaskMatcher(childTask),
        lastMessage: { role: 'tool' },
      },
      respond: { text: 'The child overlap command ran.' },
      once: true,
    },
    {
      name: steeredRule,
      when: { user: 'Also say CHILD_OVERLAP_OK' },
      respond: { text: 'CHILD_OVERLAP_OK' },
      once: true,
    },
  )
  await modelScript.fallback({ text: 'The background subagent is idle.' })
  const { row, steer, steerInputId } = await withCleanup(async () => {
    const firstStep = await modelScript.queue(
      {
        toolCalls: [
          spawnSubagentToolCall(native.provider, 'spawn-child-overlap', {
            description: 'Handle overlapping parent and client messages',
            prompt: modelScript.prompt(childTask),
            background: true,
          }),
        ],
      },
      {
        gate: parentGate,
        toolCalls: [
          copilotWriteChildMessageToolCall('parent-writes-identical-input', sharedPrompt),
        ],
      },
      { text: 'The parent sent the same instruction.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one background subagent and send an identical parent message.'))
    await modelScript.waitForGate(gate)
    const spawned = await requireRegistryRow(page)
    await expect(spawned).toHaveAttribute('data-status', 'running')
    const child = await openChildTabFromRow(page, spawned)
    const steer = [
      'agent',
      'send',
      child,
      sharedPrompt,
      '--request-id',
      'copilot-background-child-parent-overlap',
    ]
    const { input_id: steerInputId } = await runCLI(cli, steer) as { input_id: string }
    const queued = page.getByTestId(`queued-input-${steerInputId}`)
    await steerButton(queued).click()
    await expectSteerAccepted(page, queued)
    await modelScript.waitForGate(parentGate)
    await modelScript.releaseGate(parentGate)
    await modelScript.waitForSteps(firstStep + 3)
    await modelScript.releaseGate(gate)
    return { row: spawned, steer, steerInputId }
  }, async () => {
    await modelScript.releaseGateIfHeld(parentGate)
    await modelScript.releaseGateIfHeld(gate)
  })

  await expect.poll(async () => (await modelScript.status()).ruleMatches[steeredRule] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: 'CHILD_OVERLAP_OK' }).first()).toBeVisible()
  await expect(row).toHaveAttribute('data-status', 'paused')
  expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
    input_id: steerInputId,
    turn_outcome: 'completed',
    replies: ['CHILD_OVERLAP_OK'],
  })
})

copilotTest('waits on a background subagent permission and approves it on the child tab', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const parent = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'copilot-child-perm-gate'
  const childTask = 'Hold before answering so a manual-mode steer can request permission.'
  const steerPrompt = 'Write the approved Copilot child record before reporting back.'
  const file = join(parent.workingDir, 'copilot-child-steer-approval.txt')
  const quotedPath = quotePosixShellArgument(file)
  const proof = 'The Copilot child command was approved.'
  const answer = 'The Copilot child record is ready.'
  const wait = ['--wait', '--all', '--timeout', '15s']
  await modelScript.rule(
    {
      name: 'the child holds its first turn',
      when: copilotChildTaskMatcher(childTask),
      respond: { gate, text: 'Initial child work finished.' },
      once: true,
    },
    {
      name: 'the child requests permission for the steered shell command',
      when: { user: steerPrompt },
      respond: {
        toolCalls: [
          bashToolCall(native.provider, 'copilot-child-steer-perm', `printf '%s' ${quotePosixShellArgument(proof)} > ${quotedPath} && cat ${quotedPath}`),
        ],
      },
      once: true,
    },
    {
      name: 'the child returns its approved reply',
      when: {
        lastMessage: { role: 'tool', text: proof },
      },
      respond: { text: answer },
      once: true,
    },
  )
  await modelScript.fallback({ text: 'The background subagent is idle.' })
  await withCleanup(async () => {
    await modelScript.queue(
      {
        toolCalls: [
          spawnSubagentToolCall(native.provider, 'spawn-child-perm', {
            description: 'Run a shell command that requires child permission',
            prompt: modelScript.prompt(childTask),
            background: true,
          }),
        ],
      },
      { text: 'The background child started in manual mode.' },
    )
    const parentId = await selectedAgentTabId(page)
    await sendMessage(page, modelScript.prompt('Spawn a background child in manual mode for permission routing.'))
    await modelScript.waitForGate(gate)
    const spawned = await requireRegistryRow(page)
    await expect(spawned).toHaveAttribute('data-status', 'running')
    const childId = await openChildTabFromRow(page, spawned)
    const send = [
      'agent',
      'send',
      childId,
      steerPrompt,
      '--request-id',
      'copilot-child-steer-permission',
    ]
    const { input_id: inputId } = await runCLI(cli, send) as { input_id: string }
    const queued = page.getByTestId(`queued-input-${inputId}`)
    await steerButton(queued).click()
    await expectSteerAccepted(page, queued)
    await modelScript.releaseGate(gate)

    await waitForControlBanner(page)
    expect(existsSync(file)).toBe(false)
    expect(await runCLI(cli, send.concat(wait))).toMatchObject({
      input_id: inputId,
      state: 'waiting_for_user',
      replies: [],
    })
    const { requests } = await runCLI(cli, ['agent', 'requests', childId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    expect(await runCLI(cli, ['agent', 'requests', parentId])).toMatchObject({ requests: [] })
    await runCLI(cli, [
      'agent',
      'approve',
      childId,
      requests[0]!.id,
    ])
    await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
    await expect(spawned).toHaveAttribute('data-status', 'paused')
    expect(readFileSync(file, 'utf8')).toBe(proof)
    expect(await runCLI(cli, send.concat(wait))).toMatchObject({
      input_id: inputId,
      turn_outcome: 'completed',
      replies: [answer],
    })
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/505.
