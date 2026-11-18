/**
 * End-to-end coverage for `leapmux control agent send --wait` and
 * `leapmux control agent wait` over the Hub.
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
import { assistantBubbles, waitForAgentIdle } from '../helpers/ui'

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
