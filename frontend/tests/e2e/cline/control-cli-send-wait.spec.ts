import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { exerciseExactCLIReply } from '../helpers/nativeExactCLIReply'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { userBubbles, waitForControlBanner } from '../helpers/ui'
import { nativeContext } from './scenarios'

clineTest('retries the exact reply while a later native question is open', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseExactCLIReply(await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingClineWorkspace.workspaceId,
  }))
})

clineTest('retrieves the exact reply to a native Cline steer', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingClineWorkspace.workspaceId,
  })
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'cline-cli-cut-answer'
  const stoppedText = 'The draft needs more work.'
  const approvedText = 'The revised answer passed.'
  const command = 'echo cline-exact-steer'
  const steering = 'Use the revised checklist.'
  const original = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Prepare the draft answer.'),
    '--request-id',
    'cline-steer-original',
  ]
  const revised = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steering),
    '--request-id',
    'cline-steer-revised',
  ]
  const wait = ['--wait', '--all', '--timeout', '25s']
  await modelScript.queue(
    {
      ...nativeTextStep(native, stoppedText),
      gate,
    },
    {
      toolCalls: [
        bashToolCall(native.provider, 'cline-steered-permission', command),
      ],
    },
    nativeTextStep(native, approvedText),
  )

  await runCLI(cli, original)
  await withCleanup(async () => {
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, revised) as { input_id: string }
    const row = queuedInputRow(page, steering)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGateIfHeld(gate)
    expect(await runCLI(cli, [
      ...revised,
      ...wait,
    ])).toMatchObject({
      input_id: queued.input_id,
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
      input_id: queued.input_id,
      message: 'delivered',
      turn_outcome: 'completed',
      replies: [approvedText],
    }
    expect(await runCLI(cli, [
      ...revised,
      ...wait,
    ])).toMatchObject(exact)
    expect(await runCLI(cli, [
      ...revised,
      ...wait,
    ])).toMatchObject(exact)
    expect(await runCLI(cli, [
      ...original,
      ...wait,
    ])).toMatchObject({
      turn_outcome: 'completed',
      replies: [approvedText],
    })
    await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

clineTest('proves the reply to a steer drained after interrupting the lead', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingClineWorkspace.workspaceId,
  })
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const initialCommand = 'echo cline-initial-permission'
  const laterCommand = 'echo cline-drained-permission'
  const laterPrompt = 'Verify the checklist in the next run.'
  const answer = 'The next run verified the checklist.'
  const original = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Hold the lead at its permission.'),
    '--request-id',
    'cline-drain-lead',
  ]
  const drained = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(laterPrompt),
    '--request-id',
    'cline-drain-later',
  ]
  const wait = ['--wait', '--all', '--timeout', '25s']
  await modelScript.queue(
    {
      toolCalls: [
        bashToolCall(native.provider, 'cline-drain-lead-command', initialCommand),
      ],
    },
    {
      toolCalls: [
        bashToolCall(native.provider, 'cline-drain-later-command', laterCommand),
      ],
    },
    nativeTextStep(native, answer),
  )
  const first = await runCLI(cli, original) as { input_id: string }
  await expect(await waitForControlBanner(page)).toContainText(initialCommand)
  const queued = await runCLI(cli, drained) as { input_id: string }
  const row = queuedInputRow(page, laterPrompt)
  await steerButton(row).click()
  await expectSteerAccepted(page, row)
  await runCLI(cli, ['agent', 'interrupt', agentId])
  await expect(await waitForControlBanner(page)).toContainText(laterCommand)

  expect(await runCLI(cli, [
    ...original,
    ...wait,
  ])).toMatchObject({
    input_id: first.input_id,
    turn_outcome: 'interrupted',
    replies: [],
  })
  expect(await runCLI(cli, [
    ...drained,
    ...wait,
  ])).toMatchObject({
    input_id: queued.input_id,
    state: 'waiting_for_user',
    replies: [],
  })
  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    requests[0]!.id,
  ])
  expect(await runCLI(cli, [
    ...drained,
    ...wait,
  ])).toMatchObject({
    input_id: queued.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await expect(userBubbles(page).filter({ hasText: laterPrompt })).toHaveCount(1)
})
