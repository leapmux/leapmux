import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, editToolCall, readToolCall, updateTodosToolCall, writeToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection } from './helpers/subagentRegistry'
import { assistantBubbles, expectSettingsOptionChosen, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.KIRO

/**
 * 224 -- Kiro tool execution.
 *
 * The agent runs the Allow all policy, so no permission request stands between the
 * scripted calls and the rows that this test reads. `225-kiro-control-requests`
 * covers the requests.
 * The policy is LeapMux's own option, because Kiro never reports its preset.
 */
kiroTest.describe('Kiro tool execution', () => {
  kiroTest('reads, edits and writes a file, runs a command, and keeps a to-do list', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    const note = join(workingDir, 'note.txt')
    const created = join(workingDir, 'created.txt')
    writeFileSync(note, 'kiro-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'policyPreset-allow-all')

    await modelScript.queue(
      { toolCalls: [readToolCall(PROVIDER, 'kiro-read', note)] },
      { toolCalls: [editToolCall(PROVIDER, 'kiro-edit', { path: note, before: 'kiro-before', after: 'kiro-after' })] },
      { toolCalls: [writeToolCall(PROVIDER, 'kiro-write', { path: created, content: 'kiro-created\n' })] },
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-shell', 'echo "kiro-$((40 + 2))"; exit 3')] },
      {
        toolCalls: [updateTodosToolCall(PROVIDER, 'kiro-todos', [
          { step: 'Edit the note', status: 'pending' },
          { step: 'Report the result', status: 'pending' },
        ])],
      },
      { text: 'All five tools ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the five scripted tools, then report.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const tools = page.locator('[data-tool-message]:visible')
    // The read reached the model as the read's result, and the row draws the file.
    const afterRead = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(afterRead?.body)).toContain('kiro-before')
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    // The edit draws its diff, and the file on disk changed.
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'kiro-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('kiro-before')
    expect(readFileSync(note, 'utf8')).toBe('kiro-after\n')
    expect(readFileSync(created, 'utf8')).toBe('kiro-created\n')
    await expect(tools.filter({ hasText: 'created.txt' }).first()).toBeVisible()
    // The command text states no `kiro-42`, so only the command's own output can
    // put it in a tool row. Kiro states the exit code beside the output, and the
    // command header reads it.
    await expect(tools.filter({ hasText: 'kiro-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'Error (exit 3)' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'All five tools ran.' })).toBeVisible()

    // Kiro's to-do list reaches the session's checklist.
    await expandGoalsAndTodosSection(page)
    const todos = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(todos).toContainText('Edit the note')
    await expect(todos).toContainText('Report the result')
  })
})
