import type { Page } from '@playwright/test'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, codewhaleToolMessages, expect, expectCodewhalePosture } from './codewhale-fixtures'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, fileChangeRow, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

const CODEWHALE = AgentProvider.CODEWHALE

/**
 * The transcript rows on screen.
 *
 * A tool call draws two rows: the request row holds the header and the command,
 * and the result row holds what the call answered. An assertion about the
 * answer reads the result row, so it reads rows rather than one tool header.
 */
function transcriptRows(page: Page) {
  return page.locator('[data-seq]:visible')
}

/**
 * Switch the agent to Full Access, so no call waits for an approval.
 *
 * The Ask posture asks before every shell command that the runtime does not
 * classify as read-only, and `echo` is one of them. The approvals have their own
 * spec, so this one runs its tools without them.
 */
async function runWithoutApprovals(page: Page): Promise<void> {
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'permissionMode-full_access')
  await waitForSettingsIdle(page)
  await expectCodewhalePosture(page, 'full_access')
}

codewhaleTest.describe('Codewhale tool execution', () => {
  codewhaleTest('runs a command and draws its output', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await runWithoutApprovals(page)
    // The command text states no `codewhale-42`, so only the command's own output
    // can put it in a tool row. A command that printed its own text would match the
    // row's header whether or not the output reached the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'echo-call', 'echo "codewhale-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command and report what it printed.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(codewhaleToolMessages(page).filter({ hasText: 'codewhale-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The command printed its number.' })).toBeVisible()
  })

  codewhaleTest('draws the error of a command that fails', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await runWithoutApprovals(page)
    // A listing of a path that does not exist, which `ls` refuses. The runtime
    // fails the call and states the command's own error, with no exit code.
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'ls-call', 'ls codewhale-missing-path')] },
      { text: 'The listing failed.' },
    )
    await sendMessage(page, modelScript.prompt('List codewhale-missing-path and report the result.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(codewhaleToolMessages(page).filter({ hasText: 'ls codewhale-missing-path' }).first()).toBeVisible()
    const failure = transcriptRows(page).filter({ hasText: 'No such file or directory' }).first()
    await expect(failure).toContainText('Error')
    await expect(failure).toContainText('codewhale-missing-path')
    await expect(assistantBubbles(page).filter({ hasText: 'The listing failed.' })).toBeVisible()
  })

  codewhaleTest('writes, edits and reads a file, and draws the edit as a diff', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await runWithoutApprovals(page)
    // A relative path resolves against the workspace.
    await modelScript.queue(
      { toolCalls: [writeToolCall(CODEWHALE, 'write-call', { path: 'notes.txt', content: 'alpha\nold line\n' })] },
      { toolCalls: [editToolCall(CODEWHALE, 'edit-call', { path: 'notes.txt', before: 'old line', after: 'new line' })] },
      { toolCalls: [readToolCall(CODEWHALE, 'read-call', 'notes.txt')] },
      { text: 'notes.txt now holds the new line.' },
    )
    await sendMessage(page, modelScript.prompt('Create notes.txt, change its old line, and read it back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The edit's header counts the runtime's own diff: one line out, one line in.
    const edit = fileChangeRow(page, 'notes.txt')
    await expect(edit).toBeVisible()
    await expect(edit.getByTestId('git-diff-stats')).toContainText('+1')
    await expect(edit.getByTestId('git-diff-stats')).toContainText('-1')
    // The diff itself is the one row that holds both sides of the change. The
    // prompt and the write hold the old line alone, and the read and the reply
    // hold the new line alone.
    await expect(transcriptRows(page).filter({ hasText: 'old line' }).filter({ hasText: 'new line' }).first()).toBeVisible()

    // The read draws the file as the edit left it.
    await expect(transcriptRows(page).filter({ hasText: 'alpha' }).filter({ hasText: 'new line' }).filter({ hasNotText: 'old line' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'notes.txt now holds the new line.' })).toBeVisible()
  })
})
