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

import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { assistantBubbles, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'

const ANSWER = 'The release checklist has three open items.'

claudeTest.describe('control CLI waits for an agent', () => {
  claudeTest('send --wait prints the reply that the transcript shows, and wait then ends at once', async ({ authenticatedWorkspace, page, modelScript, leapmuxServer }) => {
    const { agentId } = authenticatedWorkspace
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    const step = await modelScript.queue({ text: ANSWER })
    const sent = await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('How many items of the release checklist are open?'),
      '--wait',
    ]) as Record<string, unknown>
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

    const waited = await runCLI(cli, ['agent', 'wait', agentId]) as Record<string, unknown>
    expect(waited).toEqual({
      agents: [
        { id: agentId, state: 'idle', reached: true },
      ],
    })
  })
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
