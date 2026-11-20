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
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { assistantBubbles, chooseSettingsOption, tabById, userBubbles, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

const ANSWER = 'The release checklist has three open items.'

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

  const probe = join(createTestDirectory('cli-waiting-retry-'), 'approved.txt')
  const command = `printf approved > ${quotePosixShellArgument(probe)}; cat ${quotePosixShellArgument(probe)}`
  const step = await native.modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'waiting-retry-permission', command),
    ],
  }, { text: ANSWER })
  const question = 'Run the scripted permission probe.'
  const prompt = native.modelScript.prompt(question)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
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
