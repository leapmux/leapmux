/**
 * End-to-end coverage for agent replies and safe retries of an initial
 * message over the Hub.
 *
 * The Go tests drive both commands over the local IPC socket against a fake
 * Worker. This spec runs them against a real Hub, a real Worker and a real
 * Claude Code process: the watch rides the end-to-end encrypted channel, and
 * the Worker reads the reply through Claude's own ReplyText. The CLI must
 * print the text that the transcript shows.
 */

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, chooseSettingsOption, tabById, userBubbles, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

const ANSWER = 'The release checklist has three open items.'

claudeTest('returns the exact reply when a queued steer starts the next native turn', async ({ native, authenticatedWorkspace, leapmuxServer }) => {
  const { agentId } = authenticatedWorkspace
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const leadReply = 'The original checklist is ready.'
  const steerPrompt = 'Check the second release checklist.'
  const steerReply = 'The second release checklist is ready.'
  const gate = 'claude-cli-steer-lead'
  const lead = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Check the original release checklist.'),
    '--request-id',
    'claude-cli-steer-lead',
  ]
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerPrompt),
    '--request-id',
    'claude-cli-steer',
  ]
  const probe = join(createTestDirectory('claude-cli-steer-permission-'), 'approved.txt')
  const start = await modelScript.queue(
    { text: leadReply, gate },
    {
      toolCalls: [
        bashToolCall(native.provider, 'claude-cli-steer-permission', `printf approved > ${quotePosixShellArgument(probe)}`),
      ],
    },
    { text: steerReply },
  )
  const steerWait = [
    ...steer,
    '--wait',
    '--all',
    '--timeout',
    '20s',
  ]
  await withCleanup(async () => {
    const initial = await runCLI(cli, lead) as { input_id: string }
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)
    await waitForControlBanner(page)
    expect(await runCLI(cli, steerWait)).toMatchObject({
      input_id: queued.input_id,
      state: 'waiting_for_user',
    })
    expect(await runCLI(cli, [...lead, '--wait', '--all'])).toMatchObject({
      input_id: initial.input_id,
      turn_outcome: 'completed',
      replies: [leadReply],
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
    expect(await runCLI(cli, steerWait)).toMatchObject({
      input_id: queued.input_id,
      turn_outcome: 'completed',
      replies: [steerReply],
    })
    await expect(assistantBubbles(page).filter({ hasText: leadReply })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: steerReply })).toHaveCount(1)
    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

claudeTest('returns the shared reply when a steer reaches the same native tool turn', async ({ native, authenticatedWorkspace, leapmuxServer }) => {
  const { agentId } = authenticatedWorkspace
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
  await waitForSettingsIdle(page)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const lead = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Run the marker tool and summarize the checklist.'),
    '--request-id',
    'claude-cli-in-turn-lead',
  ]
  const prompt = 'Include the changes from the follow-up checklist.'
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(prompt),
    '--request-id',
    'claude-cli-in-turn-steer',
  ]
  const gate = 'claude-cli-steer-tool'
  const answer = 'The follow-up checklist passed.'
  const start = await modelScript.queue({
    gate,
    toolCalls: [
      bashToolCall(native.provider, 'claude-cli-steer-tool', 'printf claude-cli-steer-ready'),
    ],
  }, { text: answer })
  await withCleanup(async () => {
    await runCLI(cli, lead)
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, prompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)
    await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
    expect(await runCLI(cli, [...steer, '--wait', '--all'])).toMatchObject({
      input_id: queued.input_id,
      turn_outcome: 'completed',
      replies: [answer],
    })
    expect(await runCLI(cli, [...lead, '--wait', '--all'])).toMatchObject({
      turn_outcome: 'completed',
      replies: [answer],
    })
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

claudeTest.describe('control CLI waits for an agent', () => {
  claudeTest('send --wait and an exact retry print the same transcript reply', async ({ authenticatedWorkspace, page, modelScript, leapmuxServer }) => {
    const { agentId } = authenticatedWorkspace
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    const step = await modelScript.queue({ text: ANSWER })
    const question = 'How many items of the release checklist are open?'
    const send = [
      'agent',
      'send',
      agentId,
      modelScript.prompt(question),
      '--wait',
      '--request-id',
      'release-checklist-reply',
    ]
    const sent = await runCLI(cli, send) as Record<string, unknown>
    await modelScript.waitForSteps(step + 1)

    expect(sent).toMatchObject({
      id: agentId,
      message: 'delivered',
      state: 'idle',
      turn_outcome: 'completed',
      replies: [ANSWER],
    })
    expect(typeof sent.input_id).toBe('string')
    // The browser draws the same text from the same row.
    await expect(assistantBubbles(page).filter({ hasText: ANSWER })).toHaveCount(1)
    await waitForAgentIdle(page)
    expect(await runCLI(cli, send)).toEqual(sent)
    await expect(userBubbles(page).filter({ hasText: question })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: ANSWER })).toHaveCount(1)

    const waited = await runCLI(cli, ['agent', 'wait', agentId]) as Record<string, unknown>
    expect(waited).toEqual({
      agents: [
        { id: agentId, state: 'idle', reached: true },
      ],
    })
  })
})

claudeTest('validates a retry key while the accepted input waits for permission', async ({ native, authenticatedWorkspace, leapmuxServer }) => {
  const { agentId } = authenticatedWorkspace
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const earlierReply = 'The earlier readiness check passed.'
  const earlierStep = await native.modelScript.queue({ text: earlierReply })
  const earlierInput = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt('Summarize readiness before the permission probe.'),
    '--request-id',
    'readiness-before-permission',
    '--wait',
    '--all',
  ]
  const earlier = await runCLI(cli, earlierInput) as { input_id: string }
  expect(earlier).toMatchObject({
    turn_outcome: 'completed',
    replies: [earlierReply],
  })
  await native.modelScript.waitForSteps(earlierStep + 1)
  await waitForAgentIdle(native.page)

  const probe = join(createTestDirectory('cli-waiting-retry-'), 'approved.txt')
  const command = `printf approved > ${quotePosixShellArgument(probe)}; cat ${quotePosixShellArgument(probe)}`
  const step = await native.modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'waiting-retry-permission', command),
    ],
  }, { text: ANSWER })
  const question = 'Run the scripted permission probe.'
  const prompt = native.modelScript.prompt(question)
  const keyedSend = [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    'permission-probe',
  ]
  const sent = await runCLI(cli, keyedSend) as { input_id: string }
  expect(typeof sent.input_id).toBe('string')
  await waitForControlBanner(native.page)
  await runCLI(cli, [
    'agent',
    'wait',
    agentId,
    '--until',
    'waiting_for_user',
  ])

  expect(await runCLI(cli, [...keyedSend, '--wait'])).toMatchObject({
    input_id: sent.input_id,
    message: 'delivered',
    state: 'waiting_for_user',
  })
  expect(await runCLI(cli, earlierInput)).toMatchObject({
    input_id: earlier.input_id,
    turn_outcome: 'completed',
    state: 'waiting_for_user',
    replies: [earlierReply],
  })
  await waitForControlBanner(native.page)
  await expect(runCLI(cli, [
    'agent',
    'send',
    agentId,
    'Run a different probe.',
    '--request-id',
    'permission-probe',
    '--wait',
  ])).rejects.toMatchObject({ code: 'conflict' })
  await expect(runCLI(cli, [
    'agent',
    'send',
    agentId,
    'Run a different probe.',
    '--request-id',
    'fresh-probe',
    '--wait',
  ])).rejects.toMatchObject({ code: 'agent_waiting' })
  expect(await runCLI(cli, ['agent', 'queue', 'list', agentId])).toMatchObject({ items: [] })

  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [request] = requests
  if (!request)
    throw new Error('The Worker returned no permission request.')
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    request.id,
  ])
  await native.modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, [...keyedSend, '--wait'])).toMatchObject({
    input_id: sent.input_id,
    turn_outcome: 'completed',
    replies: [ANSWER],
  })
  await expect(userBubbles(native.page).filter({ hasText: question })).toHaveCount(1)
  await expect(assistantBubbles(native.page).filter({ hasText: ANSWER })).toHaveCount(1)
})

claudeTest('queues the initial message once across agent open retries', async ({ authenticatedWorkspace, agentWorkingDir, page, modelScript, leapmuxServer }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const step = await modelScript.queue({ text: ANSWER })
  const question = 'How many items of the release checklist are open?'
  const prompt = modelScript.prompt(question)
  const open = [
    'agent',
    'open',
    '--provider',
    'claude',
    '--workspace',
    authenticatedWorkspace.workspaceId,
    '--worker',
    leapmuxServer.workerId,
    '--request-id',
    'review-checklist',
  ]
  if (agentWorkingDir !== undefined)
    open.push('--dir', agentWorkingDir)
  const request = [...open, '--message', prompt]
  const first = await runCLI(cli, request) as { id: string }
  expect(first.id).toMatch(/^[a-z0-9]{48}$/i)
  expect(await runCLI(cli, request)).toMatchObject({ id: first.id })
  await expect(runCLI(cli, [...open, '--message', 'Review a different checklist'])).rejects.toMatchObject({ code: 'conflict' })
  await expect(runCLI(cli, open)).rejects.toMatchObject({ code: 'conflict' })
  await modelScript.waitForSteps(step + 1)
  expect(await runCLI(cli, ['agent', 'wait', first.id])).toMatchObject({
    agents: [
      { id: first.id, reached: true, state: 'idle' },
    ],
  })
  await tabById(page, first.id).click()
  await expect(userBubbles(page).filter({ hasText: question })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: ANSWER })).toHaveCount(1)
})
