import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, editToolCall, readToolCall, updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection } from './helpers/subagentRegistry'
import { assistantBubbles, expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QWEN_CODE

qwenTest.describe('Qwen Code tool execution', () => {
  // YOLO, so no permission request stands between the scripted calls and the
  // rows this test reads. `123-qwen-control-requests` covers the requests.
  qwenTest('runs a command, an edit with its diff, a read and a to-do list', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'yolo' })
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'qwen-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'YOLO')

    // The read comes first: Qwen refuses to edit a file this session has not read.
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'qwen-shell', 'echo "qwen-$((40 + 2))"')] },
      { toolCalls: [readToolCall(PROVIDER, 'qwen-read', note)] },
      { toolCalls: [editToolCall(PROVIDER, 'qwen-edit', { path: note, before: 'qwen-before', after: 'qwen-after' })] },
      {
        toolCalls: [updateTodosToolCall(PROVIDER, 'qwen-todos', [
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
    // The command text states no `qwen-42`, so only the command's own output can
    // put it in a tool row. The command body draws the output from Qwen's own
    // record rather than the sentence block Qwen gives the model.
    const command = tools.filter({ hasText: 'qwen-42' }).first()
    await expect(command).toBeVisible()
    await expect(command).not.toContainText('Process Group PGID')
    // The read row heads itself with the file, and the file's text reached the
    // model as the read's result.
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    const afterRead = (await modelScript.status()).requests.find(request => request.stepIndex === 2)
    expect(JSON.stringify(afterRead?.body)).toContain('qwen-before')
    // The edit's row draws its diff as its result.
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'qwen-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('qwen-before')
    expect(readFileSync(note, 'utf8')).toBe('qwen-after\n')
    await expect(assistantBubbles(page).filter({ hasText: 'All four tools ran.' })).toBeVisible()

    await expandGoalsAndTodosSection(page)
    const todos = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(todos).toContainText('Run the shell command')
    await expect(todos).toContainText('Report the result')
  })
})
