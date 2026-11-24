import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, userBubbles, waitForControlBanner } from '../helpers/ui'
import { nativeContext } from './scenarios'

ampTest('retrieves the first exact reply while a later input waits for permission', async ({ askingAmpWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingAmpWorkspace.workspaceId,
  })
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Verify the release checklist.'
  const firstAnswer = 'The release checklist passed.'
  const laterAnswer = 'The second verification passed.'
  const firstArgs = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(prompt),
    '--request-id',
    'amp-first-checklist',
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
  const laterArgs = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(prompt),
    '--request-id',
    'amp-later-checklist',
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]

  const command = 'printf amp-second-checklist'
  await modelScript.queue(
    nativeTextStep(native, firstAnswer),
    {
      toolCalls: [
        bashToolCall(native.provider, 'amp-cli-later-permission', command),
      ],
    },
    nativeTextStep(native, laterAnswer),
  )

  const first = await runCLI(cli, firstArgs) as { input_id: string }
  expect(first).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [firstAnswer],
  })
  await expect(assistantBubbles(page).filter({ hasText: firstAnswer })).toHaveCount(1)

  const later = await withCleanup(async () => {
    const waiting = await runCLI(cli, laterArgs) as { input_id: string }
    expect(waiting).toMatchObject({ state: 'waiting_for_user', replies: [] })
    await expect(await waitForControlBanner(page)).toContainText(command)
    expect(await runCLI(cli, firstArgs)).toMatchObject({
      input_id: first.input_id,
      turn_outcome: 'completed',
      replies: [firstAnswer],
    })
    return waiting
  }, async () => {
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      requests[0]!.id,
    ])
  })

  expect(await runCLI(cli, laterArgs)).toMatchObject({
    input_id: later.input_id,
    turn_outcome: 'completed',
    replies: [laterAnswer],
  })
  expect(later.input_id).not.toBe(first.input_id)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(2)
  await expect(assistantBubbles(page).filter({ hasText: laterAnswer })).toHaveCount(1)
})

ampTest('returns the reply after the exact steer in a running Amp turn', async ({ askingAmpWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingAmpWorkspace.workspaceId,
  })
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'amp-cli-before-steer'
  const before = 'The original checklist was checked.'
  const after = 'The revised checklist was checked.'
  const command = 'printf amp-exact-steer'
  const steer = 'Use the revised checklist.'
  const prompt = (text: string, key: string) => [
    'agent',
    'send',
    agentId,
    modelScript.prompt(text),
    '--request-id',
    key,
  ]
  const wait = ['--wait', '--all', '--timeout', '25s']
  const initialArgs = prompt('Check the original checklist.', 'amp-original-checks')
  const steerArgs = prompt(steer, 'amp-revised-checks')
  await modelScript.queue(
    nativeTextStep(native, 'The native Amp thread is ready.'),
    {
      ...nativeTextStep(native, before),
      gate,
    },
    {
      toolCalls: [
        bashToolCall(native.provider, 'amp-cli-steer-permission', command),
      ],
    },
    nativeTextStep(native, after),
  )
  await runCLI(cli, [
    ...prompt('Open the checklist.', 'amp-steer-warmup'),
    ...wait,
  ])
  await runCLI(cli, initialArgs)

  await withCleanup(async () => {
    await modelScript.waitForGate(gate)
    const delivered = await runCLI(cli, steerArgs) as { input_id: string }
    const queued = queuedInputRow(page, steer)
    await steerButton(queued).click()
    await expectSteerAccepted(page, queued)
    await modelScript.releaseGate(gate)

    expect(await runCLI(cli, [
      ...steerArgs,
      ...wait,
    ])).toMatchObject({
      input_id: delivered.input_id,
      state: 'waiting_for_user',
      replies: [],
    })
    await expect(await waitForControlBanner(page)).toContainText(command)
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      requests[0]!.id,
    ])

    const exact = {
      input_id: delivered.input_id,
      message: 'delivered',
      turn_outcome: 'completed',
      replies: [after],
    }
    expect(await runCLI(cli, [
      ...steerArgs,
      ...wait,
    ])).toMatchObject(exact)
    expect(await runCLI(cli, [
      ...initialArgs,
      ...wait,
    ])).toMatchObject({
      turn_outcome: 'completed',
      replies: [before, after],
    })
    expect(await runCLI(cli, [
      ...steerArgs,
      ...wait,
    ])).toMatchObject(exact)
    await expect(userBubbles(page).filter({ hasText: steer })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})
