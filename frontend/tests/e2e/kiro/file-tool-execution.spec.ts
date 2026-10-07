import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { expectFileDiff, nativeFileReadResult, runNativeToolSteps } from '../helpers/nativeToolExecution'
import { editToolCall, readToolCall, updateTodosToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsOptionChosen, openWorkspace, toolRows } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_AGENT, nativeContext } from './scenarios'

kiroTest.describe('Kiro tool execution', () => {
  kiroTest('reads, edits and writes a file, and keeps a to-do list', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
    const directory = createNativeToolDirectory(workingDir)
    const note = join(directory, 'note.txt')
    const created = join(directory, 'created.txt')
    writeFileSync(note, 'kiro-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'policyPreset-allow-all')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    const start = await runNativeToolSteps(context, {
      steps: [
        { toolCalls: [readToolCall(context.provider, 'kiro-read', note)] },
        { toolCalls: [editToolCall(context.provider, 'kiro-edit', { path: note, before: 'kiro-before', after: 'kiro-after' })] },
        { toolCalls: [writeToolCall(context.provider, 'kiro-write', { path: created, content: 'kiro-created\n' })] },
        {
          toolCalls: [updateTodosToolCall(context.provider, 'kiro-todos', [
            { step: 'Edit the note', status: 'pending' },
            { step: 'Report the result', status: 'pending' },
          ])],
        },
      ],
      prompt: 'Run the four scripted tools, then report.',
      answer: 'All four tools ran.',
      permissions: 'none',
    })

    const tools = toolRows(page)
    // The read reached the model as the read's result, and the row draws the file.
    // The read runs before the edit, so its result holds the old text and not the
    // new text.
    await nativeFileReadResult(await modelScript.requestAt(start + 1), 'kiro-read', 'kiro-before', 'kiro-after', context.readToolResult)
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    // The edit draws its diff, and the file on disk changed.
    await expectFileDiff(page, { before: 'kiro-before', after: 'kiro-after' })
    expect(readFileSync(note, 'utf8')).toBe('kiro-after\n')
    expect(readFileSync(created, 'utf8')).toBe('kiro-created\n')
    await expect(tools.filter({ hasText: 'created.txt' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'All four tools ran.' })).toBeVisible()

    // Kiro's to-do list reaches the session's checklist.
    await expandGoalsAndTodosSection(page)
    const todos = goalsAndTodosList(page)
    await expect(todos).toContainText('Edit the note')
    await expect(todos).toContainText('Report the result')
  })
})
