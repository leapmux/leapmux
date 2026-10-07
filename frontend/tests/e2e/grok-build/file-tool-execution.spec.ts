import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { expectFileDiff, nativeFileReadResult, runNativeToolSteps } from '../helpers/nativeToolExecution'
import { editToolCall, readToolCall, updateTodosToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsOptionChosen, openWorkspace, toolRows } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest.describe('Grok Build tool execution', () => {
  // Always Approve, so no permission request stands between the scripted calls
  // and the rows this test reads. `grok-build/permissions.spec.ts` covers the
  // requests. The approval mode is LeapMux's own option, because Grok never
  // reports it.
  grokTest('runs an edit with its diff, a read and a to-do list', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    const note = join(createNativeToolDirectory(workingDir), 'note.txt')
    writeFileSync(note, 'grok-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    // The approval mode is LeapMux's own option, which the status bar does not
    // draw, so the menu states it.
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    const start = await runNativeToolSteps(context, {
      steps: [
        { toolCalls: [editToolCall(context.provider, 'grok-edit', { path: note, before: 'grok-before', after: 'grok-after' })] },
        { toolCalls: [readToolCall(context.provider, 'grok-read', note)] },
        {
          toolCalls: [updateTodosToolCall(context.provider, 'grok-todos', [
            { step: 'Run the shell command', status: 'completed' },
            { step: 'Edit the note', status: 'completed' },
            { step: 'Report the result', status: 'in_progress' },
          ])],
        },
      ],
      prompt: 'Run the three scripted tools, then report.',
      answer: 'All three tools ran.',
      permissions: 'none',
    })

    const tools = toolRows(page)
    // The read row heads itself with the file, and the file's text reached the
    // model as the read's result. The read runs after the edit, so its result
    // holds the new text and not the old text. The edit call's own arguments
    // hold the old text, so a check of the whole request cannot prove the read.
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    await nativeFileReadResult(await modelScript.requestAt(start + 2), 'grok-read', 'grok-after', 'grok-before')
    await expectFileDiff(page, { before: 'grok-before', after: 'grok-after' })
    expect(readFileSync(note, 'utf8')).toBe('grok-after\n')
    await expect(assistantBubbles(page).filter({ hasText: 'All three tools ran.' })).toBeVisible()

    await expandGoalsAndTodosSection(page)
    const todos = goalsAndTodosList(page)
    await expect(todos).toContainText('Run the shell command')
    await expect(todos).toContainText('Report the result')
  })
})
