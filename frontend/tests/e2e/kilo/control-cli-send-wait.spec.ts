import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { createNativePermissionFileWrite } from '../helpers/nativePermission'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand } from '../helpers/shellArguments'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, sendMessage, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'
import { KILO_CHILD } from './childScenario'

const waitFlags = [
  '--wait',
  '--all',
  '--timeout',
  '20s',
]

function heldKiloReply(native: ManagedNativeScenarioContext, firstChunk: string, rest: string, gate: string) {
  return {
    ...nativeTextStep(native, firstChunk + rest),
    stream: {
      chunkChars: firstChunk.length,
      delayMs: 0,
      gates: [
        { afterChunk: 1, name: gate },
      ],
    },
  }
}

kiloTest('returns the reply after the native turn consumes a queued steer', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const steerPrompt = 'Check the revised release checklist.'
  const steerReply = 'The revised checklist has two open items.'
  const gate = 'kilo-cli-steer-lead'
  const lead = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Check the first release checklist.'),
    '--request-id',
    'kilo-cli-lead',
  ]
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerPrompt),
    '--request-id',
    'kilo-cli-steer',
  ]

  await withCleanup(async () => {
    const start = await modelScript.queue(
      heldKiloReply(native, 'The first ', 'checklist is ready.', gate),
      {
        toolCalls: [
          askUserQuestionToolCall(native.provider, 'kilo-cli-steer-question', [NATIVE_CONTROL_QUESTION]),
        ],
      },
      nativeTextStep(native, steerReply),
    )
    await runCLI(cli, lead)
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)
    expect(nativeScenarioModelContextText(native, await modelScript.requestAt(start + 1))).toContain(steerPrompt)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
    const wait = steer.concat(waitFlags)
    const waiting = await runCLI(cli, wait)
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    const [request] = requests
    if (!request)
      throw new Error('The Worker returned no Kilo question.')
    await runCLI(cli, [
      'agent',
      'answer',
      agentId,
      request.id,
      '--option',
      '2',
    ])
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: steerReply })).toHaveCount(1)
    const completed = await runCLI(cli, wait)
    const leadCompleted = await runCLI(cli, lead.concat(waitFlags)) as { turn_outcome: string, replies: string[] }
    expect({ waiting, completed }).toMatchObject({
      waiting: {
        input_id: queued.input_id,
        message: 'delivered',
        state: 'waiting_for_user',
        replies: [],
      },
      completed: {
        input_id: queued.input_id,
        message: 'delivered',
        turn_outcome: 'completed',
        replies: [steerReply],
      },
    })
    expect(leadCompleted.turn_outcome).toBe('completed')
    expect(leadCompleted.replies).toEqual(['The first', 'checklist is ready.'])
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

kiloTest('keeps the replies to identical steers separate', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const steerPrompt = 'Check the next release candidate.'
  const leadGate = 'hold-lead-before-first-steer'
  const firstGate = 'hold-first-steer-before-second-steer'
  const firstHead = 'The first candidate '
  const firstTail = 'needs a change.'
  const secondReply = 'The second candidate is ready.'
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerPrompt),
    '--request-id',
  ]
  const first = steer.concat('kilo-first-identical-steer')
  const second = steer.concat('kilo-second-identical-steer')
  const row = queuedInputRow(page, steerPrompt)

  await withCleanup(async () => {
    const start = await modelScript.queue(
      heldKiloReply(native, 'The release list ', 'has two candidates.', leadGate),
      heldKiloReply(native, firstHead, firstTail, firstGate),
      nativeTextStep(native, secondReply),
    )
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Check the release list.'),
    ])
    await modelScript.waitForGate(leadGate)
    const firstInput = await runCLI(cli, first) as { input_id: string }
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(leadGate)
    await modelScript.waitForGate(firstGate)

    const secondInput = await runCLI(cli, second) as { input_id: string }
    expect(secondInput.input_id).not.toBe(firstInput.input_id)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(firstGate)
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    const firstResult = await runCLI(cli, first.concat(waitFlags)) as { replies: string[] }
    expect(firstResult).toMatchObject({ input_id: firstInput.input_id, turn_outcome: 'completed' })
    expect(firstResult.replies.join(' ')).toBe(firstHead + firstTail)
    expect(await runCLI(cli, second.concat(waitFlags))).toMatchObject({
      input_id: secondInput.input_id,
      turn_outcome: 'completed',
      replies: [secondReply],
    })
    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(2)
  }, async () => {
    await modelScript.releaseGateIfHeld(leadGate)
    await modelScript.releaseGateIfHeld(firstGate)
  })
})

kiloTest('returns an unproved outcome for a steer interrupted before consumption', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const leadFragment = 'The original analysis'
  const steerPrompt = 'Check the revised analysis.'
  const gate = 'hold-kilo-lead-until-interrupt'
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerPrompt),
    '--request-id',
    'kilo-unread-steer',
  ]

  await withCleanup(async () => {
    await modelScript.queue(heldKiloReply(native, leadFragment, ' ends here.', gate))
    modelScript.allowUnconsumed('The interrupt ends the lead while its model response is held.')
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Hold the original analysis.'),
    ])
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await runCLI(cli, ['agent', 'interrupt', agentId])

    expect(await runCLI(cli, steer.concat(waitFlags))).toMatchObject({
      input_id: queued.input_id,
      message: 'delivered',
      turn_outcome: 'unproved',
      replies: [],
    })
    await waitForAgentIdle(page)
    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

kiloTest('returns only the reply owned by a child steer', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  await applyPermissionPreset(page, 'bypass')
  const gate = 'hold-kilo-child-before-its-tool'
  const childTask = 'Run the child marker command and report its result.'
  const steerPrompt = 'Verify the updated child checklist.'
  const steerReply = 'The updated child checklist is ready.'
  await modelScript.rule({
    name: 'the child runs its first tool',
    when: KILO_CHILD.childTask(childTask),
    respond: {
      gate,
      toolCalls: [
        bashToolCall(native.provider, 'kilo-cli-child-tool', printfMarkerCommand('KILO_CHILD_READY_', 42)),
      ],
    },
    once: true,
  }, {
    name: 'the child finishes the original task',
    when: {
      ...KILO_CHILD.childTask(childTask),
      lastMessage: { role: 'tool' },
    },
    respond: { text: 'The original child marker command finished.' },
    once: true,
  }, {
    name: 'the child reads the queued steer',
    when: { user: '^Verify the updated child checklist' },
    respond: { text: steerReply },
    once: true,
  })

  const start = await modelScript.queue({
    toolCalls: [
      spawnSubagentToolCall(native.provider, 'kilo-cli-child-spawn', {
        description: 'Run the child marker command',
        prompt: modelScript.prompt(childTask),
      }),
    ],
  }, { text: 'The parent received the child result.' })

  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Spawn one child to check the marker.'))
    await modelScript.waitForGate(gate)
    const child = await requireRegistryRow(page)
    const childId = await openChildTabFromRow(page, child)
    const steer = [
      'agent',
      'send',
      childId,
      modelScript.prompt(steerPrompt),
      '--request-id',
      'kilo-cli-child-steer',
    ]
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)

    expect(await runCLI(cli, steer.concat(waitFlags))).toMatchObject({
      id: childId,
      input_id: queued.input_id,
      turn_outcome: 'completed',
      replies: [steerReply],
    })
    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: steerReply })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

kiloTest('waits for a tool permission owned by a child steer', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const { id: parentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const operation = await createNativePermissionFileWrite(native, {
    fileName: 'kilo-child-approved.txt',
    callId: 'kilo-cli-child-permission',
    outputPrefix: 'KILO_CHILD_WROTE',
  })
  const gate = 'hold-kilo-child-before-permission-steer'
  const childTask = 'Report the original child checklist.'
  const steerPrompt = 'Write the approved child checklist.'
  const steerReply = 'The child wrote the approved checklist.'
  const permissionRule = 'the steered child asks for permission to write'
  const resultRule = 'the steered child reads the write result'
  await modelScript.rule({
    name: 'the child answers the original task',
    when: KILO_CHILD.childTask(childTask),
    respond: heldKiloReply(native, 'The initial checklist ', 'is ready.', gate),
    once: true,
  }, {
    name: permissionRule,
    when: { user: '^Write the approved child checklist' },
    respond: {
      toolCalls: [operation.toolCall],
    },
    once: true,
  }, {
    name: resultRule,
    when: {
      user: '^Write the approved child checklist',
      lastMessage: { role: 'tool' },
    },
    respond: { text: steerReply },
    once: true,
  })
  const start = await modelScript.queue({
    toolCalls: [
      spawnSubagentToolCall(native.provider, 'kilo-cli-child-permission-spawn', {
        description: 'Report the child checklist',
        prompt: modelScript.prompt(childTask),
      }),
    ],
  }, { text: 'The parent received the child result.' })

  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Spawn one child to report its checklist.'))
    await modelScript.waitForGate(gate)
    const childId = await openChildTabFromRow(page, await requireRegistryRow(page))
    const steer = [
      'agent',
      'send',
      childId,
      modelScript.prompt(steerPrompt),
      '--request-id',
      'kilo-cli-child-permission-steer',
    ]
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    await expect.poll(async () => (await modelScript.status()).ruleMatches[permissionRule] ?? 0).toBe(1)

    const wait = steer.concat(waitFlags)
    expect(await runCLI(cli, wait)).toMatchObject({
      id: childId,
      input_id: queued.input_id,
      state: 'waiting_for_user',
      replies: [],
    })
    await waitForControlBanner(page)
    await operation.beforeDecision()
    const { requests } = await runCLI(cli, ['agent', 'requests', childId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    const parent = await runCLI(cli, ['agent', 'requests', parentId]) as { requests: unknown[] }
    expect(parent.requests).toEqual([])
    await runCLI(cli, [
      'agent',
      'approve',
      childId,
      requests[0]!.id,
    ])

    await modelScript.waitForSteps(start + 2)
    const result = (await modelScript.status()).requests.find(request => request.rule === resultRule)
    if (!result)
      throw new Error('The child did not read the approved tool result.')
    await operation.nativeProof(result)
    expect(await runCLI(cli, wait)).toMatchObject({
      id: childId,
      input_id: queued.input_id,
      turn_outcome: 'completed',
      replies: [steerReply],
    })
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})
