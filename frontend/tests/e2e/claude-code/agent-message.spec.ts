/**
 * End-to-end coverage of a message that one agent sends to another through
 * `leapmux control agent send`.
 *
 * The Go tests stamp the sender from a fake caller and deliver the header to a
 * fake provider. This spec runs the whole path: a real Claude Code process runs
 * the CLI in its own Bash tool, the CLI reaches the Worker through the tab's
 * control socket, the Worker stamps the sending tab from the socket's token, and
 * a second real Claude Code process reads the message under the sender's name.
 * The browser and the CLI then state the sender of the queued input and of the
 * transcript row.
 */

import type { AgentServer } from '../helpers/api'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { escapeRegExp } from '../../../src/lib/regexp'
import { agentOpenOptions } from '../agentSettings'
import { claudeTest } from '../claude-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { lastUserText } from '../helpers/mockModelScript'
import { bashToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { openWorkspace, sendMessage, tabById, userBubbles, visibleOnly, waitForAgentIdle, waitForAgentStarted } from '../helpers/ui'
import { CLAUDE_AGENT } from './scenarios'

const SENDER_TITLE = 'Release captain'
const RECIPIENT_TITLE = 'Checklist runner'
/** The line that the Worker puts before the text, which names the sending agent. */
const HEADER = `[Message from agent "${SENDER_TITLE}" (Claude Code), not from your operator]`
/** The words that the browser states above the row and on the queued input. */
const SENDER_LABEL = `Message from ${SENDER_TITLE} (Claude Code)`
const RECIPIENT_RULE = 'the recipient reads the message under the sender\'s name'
const RECIPIENT_REPLY = 'The release checklist has three open items.'

/** Open a Claude Code agent with a title, so the header and the browser state a known name. */
function openTitledClaude(server: AgentServer, workspaceId: string, title: string, optionValues: Record<string, string> = {}): Promise<string> {
  return openAgentViaAPI(server, workspaceId, newProviderWorkingDir(CLAUDE_AGENT), {
    ...agentOpenOptions(AgentProvider.CLAUDE_CODE, { optionValues }),
    title,
  })
}

/** The result of `agent queue list --json`. */
interface QueueListing {
  paused: boolean
  items: Array<{ id: string, kind: string, text: string, sender?: { tab_id: string, provider: string } }>
}

/** The result of `agent messages --json`. */
interface MessageListing {
  messages: Array<{ source: string, content?: { content?: string }, sender?: { tab_id: string, provider: string } }>
}

claudeTest.describe('a message between agents', () => {
  claudeTest('reaches the other agent under the sender\'s name, and LeapMux states the sender', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const server = { hubUrl, adminToken, workerId }
    const { workspaceId } = authenticatedEmptyWorkspace
    // The sender runs the CLI in its Bash tool, which Bypass runs without a prompt.
    const sender = await openTitledClaude(server, workspaceId, SENDER_TITLE, { permissionMode: 'bypassPermissions' })
    const recipient = await openTitledClaude(server, workspaceId, RECIPIENT_TITLE)
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    await openWorkspace(page, workspaceId)

    // The paused queue holds the message, so the browser and the CLI can read
    // the queued input before the recipient reads it.
    await runCLI(cli, ['agent', 'queue', 'pause', recipient])

    const message = modelScript.prompt('Run the release checklist and report the open items.')
    const start = await modelScript.queue(
      {
        toolCalls: [
          bashToolCall(AgentProvider.CLAUDE_CODE, 'send-to-recipient', `leapmux control agent send ${recipient} ${quotePosixShellArgument(message)}`),
        ],
      },
      { text: 'I asked the checklist runner to report the open items.' },
    )
    await tabById(page, sender).click()
    await waitForAgentStarted(page)
    await sendMessage(page, modelScript.prompt('Ask the other agent to run the release checklist.'))
    // The second step answers the tool result, so the CLI had finished.
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    const queued = await runCLI(cli, ['agent', 'queue', 'list', recipient]) as QueueListing
    expect(queued.paused).toBe(true)
    expect(queued.items).toHaveLength(1)
    const [item] = queued.items
    if (item === undefined)
      throw new Error('The recipient\'s queue holds no input.')
    expect(item).toMatchObject({
      kind: 'agent_message',
      text: message,
      sender: { tab_id: sender, provider: 'Claude Code' },
    })

    // The recipient's queue states the sender, and offers no edit of the
    // other agent's words.
    await tabById(page, recipient).click()
    await waitForAgentStarted(page)
    await expect(visibleOnly(page.getByTestId(`queued-input-sender-${item.id}`))).toHaveText(SENDER_LABEL)
    await expect(visibleOnly(page.getByTestId(`queued-input-${item.id}`)).getByRole('button', { name: 'An agent wrote this message, so it cannot be edited; delete it and send your own' })).toBeDisabled()

    // The recipient's model request must carry the header before the text. A
    // request without it matches no rule and fails the test with its record.
    await modelScript.rule({
      name: RECIPIENT_RULE,
      once: true,
      when: {
        user: [
          escapeRegExp(HEADER),
          escapeRegExp(message),
        ],
      },
      respond: { text: RECIPIENT_REPLY },
    })
    await runCLI(cli, ['agent', 'queue', 'resume', recipient])
    await expect.poll(async () => (await modelScript.status()).ruleMatches[RECIPIENT_RULE] ?? 0).toBe(1)
    const request = (await modelScript.status()).requests.find(record => record.rule === RECIPIENT_RULE)
    if (request === undefined)
      throw new Error('The mock holds no record of the recipient\'s request.')
    const read = lastUserText(request.protocol, request.body)
    expect(read).toContain(`${HEADER}\n\n${message}`)

    // The transcript row keeps the sender's own text, and states the sender
    // above it, where the user's own message states nothing.
    const row = userBubbles(page).filter({ hasText: message })
    await expect(row).toHaveCount(1)
    await expect(row.getByTestId('agent-message-sender')).toHaveText(SENDER_LABEL)
    await expect(row).not.toContainText(HEADER)
    await waitForAgentIdle(page)

    // A reload reads the row from the Worker's store, and still states the sender.
    await page.reload()
    await waitForAgentStarted(page)
    await expect(userBubbles(page).filter({ hasText: message }).getByTestId('agent-message-sender')).toHaveText(SENDER_LABEL)

    const listed = await runCLI(cli, [
      'agent',
      'messages',
      recipient,
      '--anchor',
      'oldest',
    ]) as MessageListing
    const fromSender = listed.messages.filter(entry => entry.sender !== undefined)
    expect(fromSender).toHaveLength(1)
    expect(fromSender[0]).toMatchObject({
      source: 'user',
      content: { content: message },
      sender: { tab_id: sender, provider: 'Claude Code' },
    })
  })
})
