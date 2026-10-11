import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { expectFileDiff, nativeFileReadResult, runNativeToolSteps } from '../helpers/nativeToolExecution'
import { bashToolCall, editToolCall, readToolCall, updateTodosToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, openWorkspace, toolRows } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { qwenTest } from '../qwen-fixtures'
import { nativeContext, QWEN_AGENT } from './scenarios'

qwenTest.describe('Qwen Code tool execution', () => {
  // YOLO, so no permission request stands between the scripted calls and the
  // rows this test reads. `qwen-code/permissions.spec.ts` covers the requests.
  qwenTest('runs a command, an edit with its diff, a read and a to-do list', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    const note = join(createNativeToolDirectory(workingDir), 'note.txt')
    writeFileSync(note, 'qwen-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'YOLO')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    // The read comes first: Qwen refuses to edit a file this session has not read.
    const start = await runNativeToolSteps(context, {
      steps: [
        { toolCalls: [bashToolCall(context.provider, 'qwen-shell', 'echo "qwen-$((40 + 2))"')] },
        { toolCalls: [readToolCall(context.provider, 'qwen-read', note)] },
        { toolCalls: [editToolCall(context.provider, 'qwen-edit', { path: note, before: 'qwen-before', after: 'qwen-after' })] },
        {
          toolCalls: [updateTodosToolCall(context.provider, 'qwen-todos', [
            { step: 'Run the shell command', status: 'completed' },
            { step: 'Edit the note', status: 'completed' },
            { step: 'Report the result', status: 'in_progress' },
          ])],
        },
      ],
      prompt: 'Run the four scripted tools, then report.',
      answer: 'All four tools ran.',
      permissions: 'none',
    })

    const tools = toolRows(page)
    // The command text states no `qwen-42`, so only the command's own output can
    // put it in a tool row. The command body draws the output from Qwen's own
    // record rather than the sentence block Qwen gives the model.
    const command = tools.filter({ hasText: 'qwen-42' }).first()
    await expect(command).toBeVisible()
    await expect(command).not.toContainText('Process Group PGID')
    // The read row heads itself with the file, and the file's text reached the
    // model as the read's result. The read runs before the edit, so its result
    // holds the old text and not the new text.
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    await nativeFileReadResult(await modelScript.requestAt(start + 2), 'qwen-read', 'qwen-before', 'qwen-after')
    // The edit's row draws its diff as its result.
    await expectFileDiff(page, { before: 'qwen-before', after: 'qwen-after' })
    expect(readFileSync(note, 'utf8')).toBe('qwen-after\n')
    await expect(assistantBubbles(page).filter({ hasText: 'All four tools ran.' })).toBeVisible()

    await expandGoalsAndTodosSection(page)
    const todos = goalsAndTodosList(page)
    await expect(todos).toContainText('Run the shell command')
    await expect(todos).toContainText('Report the result')
  })
})
