import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { bashToolCall, editToolCall, readToolCall, updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection } from './helpers/subagentRegistry'
import { assistantBubbles, expectSettingsOptionChosen, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build tool execution', () => {
  // Always Approve, so no permission request stands between the scripted calls
  // and the rows this test reads. `146-grok-control-requests` covers the
  // requests. The approval mode is LeapMux's own option, because Grok never
  // reports it.
  grokTest('runs a command, an edit with its diff, a read and a to-do list', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'grok-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    // The approval mode is LeapMux's own option, which the status bar does not
    // draw, so the menu states it.
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'grok-shell', 'echo "grok-$((40 + 2))"; exit 3')] },
      { toolCalls: [editToolCall(PROVIDER, 'grok-edit', { path: note, before: 'grok-before', after: 'grok-after' })] },
      { toolCalls: [readToolCall(PROVIDER, 'grok-read', note)] },
      {
        toolCalls: [updateTodosToolCall(PROVIDER, 'grok-todos', [
          { step: 'Run the shell command', status: 'completed' },
          { step: 'Edit the note', status: 'completed' },
          { step: 'Report the result', status: 'in_progress' },
        ])],
      },
      { text: 'All four tools ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the four scripted tools, then report.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const tools = page.locator('[data-tool-message]:visible')
    // The command text states no `grok-42`, so only the command's own output can
    // put it in a tool row. Grok states the exit code in its own record, and the
    // command header reads it.
    const command = tools.filter({ hasText: 'grok-42' }).first()
    await expect(command).toBeVisible()
    await expect(tools.filter({ hasText: 'Error (exit 3)' }).first()).toBeVisible()
    // The read row heads itself with the file, and the file's text reached the
    // model as the read's result.
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    const afterRead = (await modelScript.status()).requests.find(request => request.stepIndex === 3)
    expect(JSON.stringify(afterRead?.body)).toContain('grok-before')
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'grok-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('grok-before')
    expect(readFileSync(note, 'utf8')).toBe('grok-after\n')
    await expect(assistantBubbles(page).filter({ hasText: 'All four tools ran.' })).toBeVisible()

    await expandGoalsAndTodosSection(page)
    const todos = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(todos).toContainText('Run the shell command')
    await expect(todos).toContainText('Report the result')
  })
})
