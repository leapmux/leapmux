import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, sendMessage, waitForSettingsIdle } from './helpers/ui'

/**
 * The final step of the Claude `tool_progress` handling: a real CLI heartbeat
 * reaching a real browser as a badge on the running tool's card.
 *
 * Everything below the browser is covered by unit tests -- the Go handler
 * replays verbatim CLI frames, and the store, the wire translation and the badge
 * each have their own suite. What none of them can show is that the three meet:
 * that the worker's broadcast reaches THIS row's card, and that the badge goes
 * away when the tool stops.
 *
 * Two facts make this spec possible, and both were mis-diagnosed when the
 * feature was written:
 *
 *   - Claude Code starts a 30-second heartbeat for every tool call of the MAIN
 *     agent, so the tool must run PAST 30 seconds. A tool that finishes sooner
 *     emits nothing, which reads exactly like a broken badge.
 *   - The tool must be allowed to run at all. Under the default permission mode
 *     a Bash call waits on a prompt nothing answers, so it never starts and
 *     sends no heartbeat.
 *
 * The command runs a local HTTP test server. The test controls its exit through a request.
 * This verifies both badge visibility and removal without a fixed tool duration.
 * The configured agent refuses file-polling loops, so use this request protocol instead.
 */
/**
 * The deadline for the FIRST heartbeat, and the one assertion in this suite that
 * needs more than the project's 30-second `expect` timeout.
 *
 * That timeout is sized for a deterministic mock endpoint, which answers in
 * milliseconds. This assertion does not wait on the mock at all -- it waits on a
 * `setInterval` inside the Claude CLI, whose period is a hard-coded 30 000 ms
 * with no environment override (`claude` 2.1.277, `var A5n=30000`). The two
 * numbers are therefore equal, and the assertion's own clock starts only after
 * the tool card renders, so the badge must cross worker, hub, socket and render
 * inside the few milliseconds that separate the two events. It usually did, and
 * under suite load it did not.
 *
 * 90 seconds is the CLI's period plus room for the second heartbeat, so a
 * genuine regression still fails well inside the 120-second test timeout.
 */
const FIRST_HEARTBEAT_DEADLINE_MS = 90_000

/**
 * The port the scripted server bound, once it reports one.
 *
 * `expect.poll` rather than a fixed wait: the command starts when the agent
 * runs it, which is some way after the send, and a listen on port 0 answers as
 * soon as the process is up.
 */
async function waitForBoundPort(portFile: string): Promise<number> {
  let port = 0
  await expect.poll(async () => {
    port = Number.parseInt(await readFile(portFile, 'utf8').catch(() => ''), 10)
    return Number.isInteger(port) && port > 0
  }, { message: `the scripted server never reported a port at ${portFile}` }).toBe(true)
  return port
}

test.describe('Tool Running Badge', () => {
  test('shows a long Claude tool\'s elapsed time, and clears it when the tool ends', async ({ page, authenticatedWorkspace, modelScript }) => {
    const dir = createTestDirectory('leapmux-badge-')
    const script = join(dir, 'tool-server.mjs')
    const portFile = join(dir, 'port')
    // The server picks its own port and REPORTS it, rather than taking one this
    // test reserved. `findFreePort` answers for the moment it is asked, and the
    // agent binds it a good forty seconds later -- after a settings change, an
    // agent restart and a round trip. In a suite that runs half an hour,
    // something else took it: the command died with an unhandled `EADDRINUSE`,
    // the CLI cleared its heartbeat timer in the tool call's `finally`, and no
    // heartbeat could ever arrive. Port 0 closes that window entirely.
    await writeFile(script, `
import { createServer } from 'node:http'
import { writeFileSync } from 'node:fs'
const server = createServer((_request, response) => {
  response.end('DONE')
  server.close()
})
server.listen(0, '127.0.0.1', () => {
  writeFileSync(${JSON.stringify(portFile)}, String(server.address().port))
})
setTimeout(() => server.close(), 180000).unref()
`)

    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await expect(page.getByText(/^Starting /)).not.toBeVisible()

    // Without this the Bash call blocks on a permission prompt, the tool never
    // starts, and no heartbeat is ever emitted.
    await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
    await expectSettingsChip(page, 'Bypass Permissions')
    await waitForSettingsIdle(page)

    // The mode change restarts the agent process, and a send that lands in that
    // gap is refused outright -- the message renders "Failed to deliver" and the
    // agent runs nothing, which looks exactly like a badge that never appeared.
    // Neither the chip nor the settings spinner marks the end of that gap, so
    // one trivial round-trip is used as the proof that the agent is live again.
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(1)
    await expectAssistantAnswer(page)

    // The tool call is SCRIPTED, but the tool itself is real: the CLI runs the
    // command locally and emits its own 30-second heartbeat, which is the whole
    // subject here. Only the decision to call Bash comes from the script.
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CLAUDE_CODE, 'run-server', `node ${script}`)] })
    await sendMessage(page, modelScript.prompt(`Run the Node.js integration test server at ${script} in the foreground.`))
    await modelScript.waitForSteps(2)
    // Wait for the tool's own card, which is the POSITIVE proof that the send
    // reached the agent and the Bash call started. It is also the fast failure:
    // a refused send never produces a card, so the run stops here naming the
    // missing tool rather than 120 s later naming a missing badge.
    //
    // The absence of the "Failed to deliver" banner cannot serve as that proof.
    // sendMessage returns as soon as the composer clears, which happens before
    // the round trip, so at that moment the banner has not rendered either way --
    // and `not.toBeVisible()` passes at once for a locator matching no element.
    //
    // :visible-scoped throughout, because ChatView mounts an off-screen
    // premeasure copy of every unmeasured row and it renders these nodes too.
    await expect(page.locator('[data-tool-message]:visible').first()).toBeVisible()

    // The port file is the POSITIVE proof that the command is running. The tool
    // card above appears for a command that died one line in, and the badge is
    // then absent for a reason that has nothing to do with the badge -- which
    // is exactly how an `EADDRINUSE` read here for a whole suite. Waiting for
    // the file separates "the tool never ran" from "the heartbeat never
    // arrived", and it fails in seconds rather than after the deadline below.
    const port = await waitForBoundPort(portFile)

    const badge = page.locator('[data-testid="tool-running-badge"]:visible')

    // The first heartbeat lands 30 seconds into the call, so this assertion
    // spends that long waiting -- past the project's expect timeout on its own.
    // See FIRST_HEARTBEAT_DEADLINE_MS for why this is the one override here.
    await expect(badge).toBeVisible({ timeout: FIRST_HEARTBEAT_DEADLINE_MS })
    // formatSecondsParts' output, which the badge always takes: "30s", "1m",
    // "1m 30s". Anchored, so a badge rendering "NaNs", a decimal "5.0s" or an
    // empty string fails rather than passing on a substring. Not pinned to "30s"
    // exactly -- a slow worker can deliver the second heartbeat first, and "1m"
    // is just as correct an answer.
    await expect(badge).toHaveText(/^\d+[dhms]( \d+[hms])*$/)

    // Queued BEFORE the request below: the agent asks for this answer the moment
    // the tool result lands, and a queue that is empty then fails the turn.
    await modelScript.queue({ text: 'DONE' })

    // End the tool. Its result row lands, and the frontend -- not the worker --
    // is what drops the badge, so this is the half no Go test can reach.
    const response = await fetch(`http://127.0.0.1:${port}/complete`)
    expect(response.ok).toBe(true)
    expect(await response.text()).toBe('DONE')
    await expect(badge).not.toBeVisible()
  })
})
