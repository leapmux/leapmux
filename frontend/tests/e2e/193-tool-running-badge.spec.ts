import { mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, sendMessage, waitForSettingsIdle } from './helpers/ui'

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
 * The command waits on a FILE rather than sleeping for a fixed time. The test
 * therefore owns when the tool ends, which is what lets it assert the clear as
 * well as the appearance -- and a standalone `sleep` is refused outright by the
 * Bash hook that this machine's Claude config installs.
 */
test.describe('Tool Running Badge', () => {
  test('shows a long Claude tool\'s elapsed time, and clears it when the tool ends', async ({ page, authenticatedWorkspace }) => {
    const dir = await mkdtemp(join(tmpdir(), 'leapmux-badge-'))
    const flag = join(dir, 'release')

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
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
    await sendMessage(page, ARITHMETIC_PROMPT)
    await expectAssistantAnswer(page)

    await sendMessage(
      page,
      `Run this exact bash command with a 180000 ms timeout, then reply DONE: until [ -f ${flag} ]; do sleep 2; done`,
    )
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

    const badge = page.locator('[data-testid="tool-running-badge"]:visible')

    // The first heartbeat lands 30 seconds into the call, so this assertion
    // spends that long waiting. The global expect timeout (120s) covers it.
    await expect(badge).toBeVisible()
    // formatSecondsParts' output, which the badge always takes: "30s", "1m",
    // "1m 30s". Anchored, so a badge rendering "NaNs", a decimal "5.0s" or an
    // empty string fails rather than passing on a substring. Not pinned to "30s"
    // exactly -- a slow worker can deliver the second heartbeat first, and "1m"
    // is just as correct an answer.
    await expect(badge).toHaveText(/^\d+[dhms]( \d+[hms])*$/)

    // End the tool. Its result row lands, and the frontend -- not the worker --
    // is what drops the badge, so this is the half no Go test can reach.
    await writeFile(flag, '')
    await expect(badge).not.toBeVisible()
  })
})
