import { Buffer } from 'node:buffer'
import { execFile } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider, MessageCompletion, MessageSource } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { prettifyJson } from '../../src/lib/jsonFormat'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createImageBytes } from './helpers/image'
import { createTestDirectory } from './helpers/runDirectory'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, listAgents, openGoalMenu } from './helpers/subagentRegistry'
import { openWorkspace, sendMessage } from './helpers/ui'
import { realAgentOpenOptions, realAgentSettings } from './realAgentSettings'

interface FixtureMessage {
  id: string
  provider: AgentProvider
  content: unknown
  spanId?: string
  spanType?: string
  supplemental?: unknown
  metadata?: unknown
  completion?: MessageCompletion
}

const sqlString = (value: string) => `'${value.replaceAll('\'', '\'\'')}'`
const blob = (value: unknown) => `X'${Buffer.from(JSON.stringify(value), 'utf8').toString('hex')}'`

/** Seed the actual worker database. The high-water trigger allocates later live messages safely. */
async function seedMessages(database: string, agentId: string, messages: FixtureMessage[], permission?: Record<string, unknown>, controlSourceId?: string): Promise<void> {
  const agent = sqlString(agentId)
  const inserts = messages.map(message => `INSERT INTO messages
    (id, agent_id, seq, source, content, content_compression, agent_provider, span_id, span_type, supplemental_content, supplemental_content_compression, supplemental_revision, completion)
    VALUES (${sqlString(`${agentId}-${message.id}`)}, ${agent},
      (SELECT message_seq_hwm + 1 FROM agents WHERE id = ${agent}), ${MessageSource.AGENT},
      ${blob(message.content)}, 1, ${message.provider}, ${sqlString(message.spanId ?? '')}, ${sqlString(message.spanType ?? '')},
      ${message.supplemental || message.metadata ? blob({ provider: message.supplemental, metadata: message.metadata }) : 'X\'\''}, 1, ${message.supplemental || message.metadata ? 1 : 0}, ${message.completion ?? MessageCompletion.UNSPECIFIED});`)
  if (permission) {
    const source = controlSourceId ? `(SELECT seq FROM messages WHERE agent_id=${agent} AND id=${sqlString(`${agentId}-${controlSourceId}`)})` : '0'
    inserts.push(`INSERT INTO control_requests (agent_id, request_id, payload, claim_token, source_seq) VALUES (${agent}, 'fixture-permission', ${blob(permission)}, 'fixture-claim', ${source});`)
  }
  const sql = `.timeout 10000\nBEGIN IMMEDIATE;\n${inserts.join('\n')}\nCOMMIT;\n`
  await new Promise<void>((resolve, reject) => {
    const child = execFile('sqlite3', ['-bail', database], (error) => {
      if (error)
        reject(error)
      else
        resolve()
    })
    child.stdin!.end(sql)
  })
}

function editFixture(provider: AgentProvider): FixtureMessage[] {
  const path = '/project/parity.ts'
  const before = 'const parityBefore = 1'
  const after = 'const parityAfter = 2'
  const shared = { provider, spanId: 'parity-edit' }
  if (provider === AgentProvider.CLAUDE_CODE) {
    return [
      { ...shared, id: 'request', spanType: 'Edit', content: { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'parity-edit', name: 'Edit', input: { file_path: path, old_string: before, new_string: after } }] } } },
      { ...shared, id: 'result', spanType: 'Edit', content: { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'parity-edit', content: 'Saved' }] }, tool_use_result: { filePath: path, oldString: before, newString: after } } },
    ]
  }
  if (provider === AgentProvider.CODEX) {
    return [{ ...shared, id: 'result', spanType: 'fileChange', content: { item: { id: 'parity-edit', type: 'fileChange', status: 'completed', changes: [{ path, kind: { type: 'update' }, diff: `@@ -1 +1 @@\n-${before}\n+${after}\n` }] } } }]
  }
  if (provider === AgentProvider.PI) {
    return [
      { ...shared, id: 'request', spanType: 'edit', content: { type: 'tool_execution_start', toolCallId: 'parity-edit', toolName: 'edit', args: { path, edits: [{ oldText: before, newText: after }] } } },
      { ...shared, id: 'result', spanType: 'edit', content: { type: 'tool_execution_end', toolCallId: 'parity-edit', toolName: 'edit', result: { content: [{ type: 'text', text: 'Saved' }] }, isError: false } },
    ]
  }
  if (provider === AgentProvider.ZCODE) {
    return [
      { ...shared, id: 'request', spanType: 'Edit', content: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'parity-edit', toolName: 'Edit', inputOmitted: true, inputRef: 'model_stream' } }, supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'parity-edit', input: { file_path: path, old_string: before, new_string: after } } } },
      { ...shared, id: 'result', spanType: 'Edit', content: { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'parity-edit', result: { success: true, content: 'Saved', display: { kind: 'file_diff', filePath: path, structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [`-${before}`, `+${after}`] }] } } } } },
    ]
  }
  const input = provider === AgentProvider.GOOSE
    ? { path, before, after }
    : provider === AgentProvider.REASONIX
      ? { path, old_string: before, new_string: after }
      : { filePath: path, oldString: before, newString: after }
  return [
    { ...shared, id: 'request', spanType: 'edit', content: {
      sessionUpdate: 'tool_call',
      toolCallId: 'parity-edit',
      kind: 'edit',
      status: 'pending',
      title: provider === AgentProvider.REASONIX ? 'edit_file' : 'edit',
      rawInput: input,
      ...(provider === AgentProvider.GOOSE ? { _meta: { goose: { toolCall: { toolName: 'edit', extensionName: 'developer' } } } } : {}),
    } },
    { ...shared, id: 'result', spanType: 'edit', content: { sessionUpdate: 'tool_call_update', toolCallId: 'parity-edit', status: 'completed', content: [{ type: 'diff', path, oldText: before, newText: after }] } },
  ]
}

const providers = [
  ['Claude Code', AgentProvider.CLAUDE_CODE],
  ['Codex', AgentProvider.CODEX],
  ['OpenCode', AgentProvider.OPENCODE],
  ['Kilo', AgentProvider.KILO],
  ['Cursor', AgentProvider.CURSOR],
  ['Copilot', AgentProvider.GITHUB_COPILOT],
  ['Goose', AgentProvider.GOOSE],
  ['Reasonix', AgentProvider.REASONIX],
  ['Pi', AgentProvider.PI],
  ['ZCode', AgentProvider.ZCODE],
] as const

test.describe('provider tool rendering', () => {
  test('renders recovered ZCode parent and child images and opens the full image', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const bytes = await createImageBytes(page, 160, 96)
    const dataUrl = `data:image/png;base64,${Buffer.from(bytes).toString('base64')}`
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-zcode-artifact-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const messages: FixtureMessage[] = []
    for (const child of [false, true]) {
      const nativeCallId = child ? 'child-image' : 'main-image'
      const toolCallId = child ? `tool_subagent_agent_${nativeCallId}` : nativeCallId
      const sessionId = child ? 'child-session' : 'session'
      const uri = `zcode-artifact://${sessionId}/tool-result-00000000-0000-4000-8000-000000000001`
      const identity = { toolCallId, ...(child ? { agentId: 'agent', childSessionId: sessionId } : {}) }
      const shared = { provider: AgentProvider.ZCODE, spanId: toolCallId, spanType: 'mcp__renderer_probe__echo' }
      messages.push(
        { ...shared, id: `${toolCallId}-request`, content: { type: 'tool.updated', payload: { ...identity, kind: 'scheduled', toolName: shared.spanType, inputOmitted: true } } },
        {
          ...shared,
          id: `${toolCallId}-result`,
          content: { type: 'tool.updated', payload: { ...identity, kind: 'result', result: { success: true, content: '[Attached image/png: MCP image]', display: { kind: 'mcp_tool', serverName: 'renderer_probe', toolName: 'echo' } } } },
          supplemental: {
            type: 'tool.updated',
            payload: { kind: 'result', toolCallId },
            nativeTool: { id: 'part', sessionId, messageId: 'message', data: {
              type: 'tool',
              callID: nativeCallId,
              tool: shared.spanType,
              state: {
                status: 'completed',
                input: { query: 'render image' },
                metadata: { modelContentLayout: [{ type: 'text', text: child ? 'Child preview' : 'Parent preview' }, { type: 'attachment', attachmentIndex: 0 }] },
                attachments: [{ type: 'file', sessionID: sessionId, messageID: 'message', mime: 'image/png', filename: 'MCP image', url: uri }],
              },
            } },
            artifacts: { [uri]: dataUrl },
          },
        },
      )
    }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, messages)
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    const images = chat.locator('img[src^="data:image/png"]').filter({ visible: true })
    await expect(images).toHaveCount(2)
    await expect(images.nth(0)).toHaveJSProperty('naturalWidth', 160)
    await expect(images.nth(1)).toHaveJSProperty('naturalWidth', 160)
    await expect(chat.getByText('renderer_probe / echo', { exact: true })).toHaveCount(2)
    await expect(chat.getByText('"render image"', { exact: true })).toHaveCount(2)
    await expect(chat).not.toContainText('[Attached image')
    await chat.getByRole('button', { name: 'Open image', exact: true }).first().click()
    await expect(page.locator('img[src^="blob:"]').filter({ visible: true })).toHaveJSProperty('naturalWidth', 160)
  })

  test('renders retained tool completion from the separate message field', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-completion-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const providers = [AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.PI, AgentProvider.ZCODE]
    const messages: FixtureMessage[] = []
    for (const provider of providers) {
      const id = `completion-${provider}`
      const output = `retained stdout from provider ${provider}`
      const command = 'printf partial'
      const pair = provider === AgentProvider.CODEX
        ? [
            { item: { type: 'commandExecution', id, command, status: 'inProgress' } },
            { item: { type: 'commandExecution', id, command, status: 'inProgress', aggregatedOutput: output } },
          ]
        : provider === AgentProvider.PI
          ? [
              { type: 'tool_execution_start', toolCallId: id, toolName: 'bash', args: { command } },
              { type: 'tool_execution_end', toolCallId: id, toolName: 'bash', isError: true, result: { content: [{ type: 'text', text: output }] } },
            ]
          : provider === AgentProvider.ZCODE
            ? [
                { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: id, toolName: 'Bash', input: { command } } },
                { type: 'tool.updated', payload: { kind: 'result', toolCallId: id, toolName: 'Bash', result: { success: false, content: output } } },
              ]
            : [
                { sessionUpdate: 'tool_call', toolCallId: id, kind: 'execute', title: 'Run command', status: 'pending', rawInput: { command } },
                { sessionUpdate: 'tool_call_update', toolCallId: id, status: 'in_progress', kind: 'execute', content: [{ type: 'content', content: { type: 'text', text: output } }] },
              ]
      messages.push(
        { id: `${id}-request`, provider, spanId: id, content: pair[0] },
        { id: `${id}-result`, provider, spanId: id, completion: MessageCompletion.INTERRUPTED, content: { ...pair[1], _leapmux: { completion: 'complete' } } },
      )
    }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, messages)
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    for (const provider of providers) {
      const result = chat.getByTestId('message-bubble').filter({ hasText: `retained stdout from provider ${provider}` })
      await expect(result).toBeVisible()
      await expect(result.getByText('Interrupted', { exact: true })).toHaveCount(1)
      await expect(result).not.toContainText('Text truncated')
      await expect(result.getByText('Error', { exact: true })).toHaveCount(0)
    }
  })

  for (const provider of [AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.REASONIX]) {
    test(`renders one checklist in the to-do result row (${provider})`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-todo-'), {
        agentProvider: AgentProvider.CLAUDE_CODE,
        ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
      })
      const todos = [{ content: 'Inspect the shared checklist', status: 'pending' }]
      const spanId = 'todo-call'
      let request: unknown
      let result: unknown
      let spanType = 'TodoWrite'
      if (provider === AgentProvider.CLAUDE_CODE) {
        request = { type: 'assistant', message: { content: [{ type: 'tool_use', id: spanId, name: spanType, input: { todos } }] } }
        result = { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: spanId, content: 'Todos updated' }] }, tool_use_result: { newTodos: todos } }
      }
      else if (provider === AgentProvider.ZCODE) {
        request = { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: spanId, toolName: spanType, input: { todos } } }
        result = { type: 'tool.updated', payload: { kind: 'result', toolCallId: spanId, result: { success: true, content: 'Todos updated' } } }
      }
      else {
        spanType = 'other'
        request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: provider === AgentProvider.REASONIX ? 'todo_write' : 'todowrite', kind: 'other', status: 'pending', rawInput: { todos } }
        result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', rawOutput: { metadata: { todos } }, content: [{ type: 'content', content: { type: 'text', text: JSON.stringify(todos) } }] }
      }
      await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
        { id: 'todo-request', provider, spanId, spanType, content: request },
        { id: 'todo-result', provider, spanId, spanType, content: result },
      ])
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
      const header = chat.getByTestId('message-bubble').filter({ hasText: '1 task' })
      await expect(header).toHaveCount(1)
      await expect(header.locator('[data-task-checkbox]')).toHaveCount(0)
      const body = chat.getByTestId('message-bubble').filter({ hasText: 'Inspect the shared checklist' })
      await expect(body).toHaveCount(1)
      await expect(body.locator('[data-task-checkbox="pending"]')).toHaveCount(1)
      await expect(body).not.toContainText('Todos updated')
    })
  }

  for (const provider of [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.GITHUB_COPILOT, AgentProvider.ZCODE, AgentProvider.GOOSE, AgentProvider.REASONIX, AgentProvider.PI, AgentProvider.CURSOR]) {
    test(`opens and copies shared agent prompts and reports (${provider})`, async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer }) => {
      await context.grantPermissions(['clipboard-read', 'clipboard-write'])
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-agent-'), {
        agentProvider: AgentProvider.CLAUDE_CODE,
        ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
      })
      const spanId = 'shared-agent-call'
      const prompt = '**Instruction marker**\n\nRead the fixture and report the findings.'
      const report = '**Report marker**\n\n- First finding\n- Second finding\n- Third finding\n- Fourth finding\n- Last finding marker'
      const args = { description: 'Inspect the fixture', prompt, subagent_type: 'explore', agent_type: 'explore', mode: 'sync' }
      let request: Record<string, unknown>
      let result: Record<string, unknown>
      let spanType = 'Agent'
      let supplemental: Record<string, unknown> | undefined
      if (provider === AgentProvider.CLAUDE_CODE) {
        request = { type: 'assistant', message: { content: [{ type: 'tool_use', id: spanId, name: 'Agent', input: args }] } }
        result = { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: spanId, content: report }] }, tool_use_result: { status: 'completed', agentId: 'child', content: [{ type: 'text', text: report }] } }
      }
      else if (provider === AgentProvider.CODEX) {
        spanType = 'collabAgentToolCall'
        const item = { id: spanId, type: spanType, tool: 'spawnAgent', prompt, receiverThreadIds: [] }
        request = { item: { ...item, status: 'inProgress' } }
        result = { item: { ...item, status: 'completed', receiverThreadIds: ['child'], agentsStates: { child: { status: 'completed', message: report } } } }
      }
      else if (provider === AgentProvider.ZCODE) {
        request = { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: spanId, toolName: 'Agent', input: args } }
        result = { type: 'tool.updated', payload: { kind: 'result', toolCallId: spanId, result: { success: true, content: `${report}\nagentId: child (use SendMessage with to: 'child' to continue this agent)\n<usage>tool_uses: 2\nduration_ms: 1000</usage>` } } }
      }
      else if (provider === AgentProvider.PI) {
        request = { type: 'tool_execution_start', toolCallId: spanId, toolName: 'Agent', args }
        result = { type: 'tool_execution_end', toolCallId: spanId, toolName: 'Agent', isError: false, result: {
          content: [{ type: 'text', text: `Agent completed in 1.0s (2 tool uses).\n\n${report}` }],
          details: { description: args.description, status: 'completed', agentId: 'child', toolUses: 2, durationMs: 1000 },
        } }
      }
      else if (provider === AgentProvider.CURSOR) {
        spanType = 'other'
        request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: 'Task: Inspect the fixture', kind: 'other', status: 'pending', rawInput: { _toolName: 'task', description: args.description, prompt } }
        result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', rawOutput: { durationMs: 1000, isBackground: false } }
        supplemental = { rawOutput: {
          content: [{ type: 'tool-result', toolCallId: spanId, toolName: 'Task', result: report }],
          providerOptions: { cursor: { highLevelToolCallResult: { output: { success: { agentId: 'child', conversationSteps: [{ assistantMessage: { text: report } }] } } } } },
        } }
      }
      else if (provider === AgentProvider.GOOSE) {
        spanType = 'other'
        request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: 'delegate', kind: 'other', status: 'pending', rawInput: { instructions: prompt, source: 'explore' }, _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } } }
        result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', content: [{ type: 'content', content: { type: 'text', text: report } }] }
      }
      else if (provider === AgentProvider.REASONIX) {
        spanType = 'other'
        request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: 'use_capability', kind: 'other', status: 'pending', rawInput: { action: 'call', capability_id: 'tool:task', arguments: args } }
        result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', content: [{ type: 'content', content: { type: 'text', text: `Subagent reference: sa_fixture\nSubagent outcome: status=completed retryable=false\n\nFinal answer:\n${report}` } }] }
      }
      else {
        const copilot = provider === AgentProvider.GITHUB_COPILOT
        spanType = copilot ? 'other' : 'think'
        request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: 'task', kind: spanType, status: 'pending', rawInput: args }
        result = copilot
          ? { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', rawOutput: { content: report } }
          : { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', rawOutput: { metadata: { sessionId: 'child' } }, content: [{ type: 'content', content: { type: 'text', text: `<task id="child" state="completed">\n<task_result>\n${report}\n</task_result>\n</task>` } }] }
        if (copilot) {
          supplemental = { nativeEvents: [
            { type: 'tool.execution_start', data: { toolCallId: spanId, toolName: 'task', arguments: args } },
            { type: 'subagent.completed', agentId: 'child', data: { toolCallId: spanId } },
          ] }
        }
      }
      const supplement = (content: Record<string, unknown>) => supplemental ? { sessionUpdate: content.sessionUpdate, toolCallId: spanId, status: content.status, ...supplemental } : undefined
      await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
        { id: 'request', provider, spanId, spanType, content: request, supplemental: supplement(request) },
        { id: 'result', provider, spanId, spanType, content: result, supplemental: supplement(result) },
      ])
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
      const body = chat.getByTestId('message-bubble').filter({ hasText: 'Report marker' })
      await expect(body).toHaveCount(1)
      await expect(chat.getByText('Instruction marker', { exact: true })).toHaveCount(0)
      await chat.getByRole('button', { name: 'Show prompt', exact: true }).click()
      await expect(chat.locator('strong').filter({ hasText: 'Instruction marker' })).toHaveCount(1)
      const resultRow = body.locator('..')
      await resultRow.hover()
      await resultRow.getByRole('button', { name: 'Expand', exact: true }).click()
      await expect(resultRow.getByRole('button', { name: 'Collapse', exact: true })).toBeVisible()
      await expect(body.getByText('Last finding marker', { exact: true })).toBeVisible()
      await resultRow.getByRole('button', { name: 'Copy', exact: true }).click()
      await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(report)
    })
  }

  test('renders Reasonix read-only reports without interpreting quoted status text', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-readonly-agent-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const report = 'Subagent outcome: status=failed retryable=false\n\nFinal answer:\n- **Quoted finding**'
    const shared = { provider: AgentProvider.REASONIX, spanId: 'readonly', spanType: 'other' }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { ...shared, id: 'request', content: { sessionUpdate: 'tool_call', toolCallId: 'readonly', title: 'use_capability', kind: 'other', status: 'pending', rawInput: {
        action: 'call',
        capability_id: 'tool:read_only_task',
        arguments: { description: 'Read a protocol example', prompt: 'Read **the example** without changes.' },
      } } },
      { ...shared, id: 'result', content: { sessionUpdate: 'tool_call_update', toolCallId: 'readonly', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: report } }] } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    const body = chat.getByTestId('message-bubble').filter({ hasText: 'Quoted finding' })
    await expect(body.getByText('Agent "Read a protocol example" completed', { exact: true })).toBeVisible()
    await expect(body).toContainText('Subagent outcome: status=failed retryable=false')
    await body.locator('..').hover()
    await body.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(report)
    await chat.getByRole('button', { name: 'Show prompt', exact: true }).click()
    await expect(chat.locator('strong').filter({ hasText: 'the example' })).toBeVisible()
  })

  test('renders recovered Reasonix error details and preserves the cancellation outcome', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-agent-error-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const shared = { provider: AgentProvider.REASONIX, spanId: 'cancelled-agent', spanType: 'other' }
    const result = { sessionUpdate: 'tool_call_update', toolCallId: shared.spanId, status: 'failed', content: [{ type: 'content', content: { type: 'text', text: 'context canceled' } }] }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { ...shared, id: 'request', content: { sessionUpdate: 'tool_call', toolCallId: shared.spanId, title: 'task', kind: 'other', status: 'pending', rawInput: { description: 'Inspect sample', prompt: 'Read the sample' } } },
      { ...shared, id: 'result', content: result, supplemental: {
        sessionUpdate: result.sessionUpdate,
        toolCallId: shared.spanId,
        status: result.status,
        rawOutput: { reasonix: { role: 'tool', tool_call_id: shared.spanId, name: 'task', content: 'error: context canceled\nSubagent outcome: status=cancelled retryable=false\n\nFinal answer:\n- **Partial finding**' } },
      } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    const body = chat.getByTestId('message-bubble').filter({ hasText: 'Partial finding' })
    await expect(body.getByText('Agent "Inspect sample" stopped', { exact: true })).toBeVisible()
    await expect(body).toContainText('context canceled')
    await expect(body.locator('li strong')).toHaveText('Partial finding')
    await expect(body).not.toContainText('Subagent outcome:')
    await body.locator('..').hover()
    await body.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('context canceled\n\n- **Partial finding**')
  })

  test('renders Pi result retrieval and rejected steering through the shared agent components', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-control-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const report = '- **Retrieved finding**\n\nA complete report.'
    const messages: FixtureMessage[] = []
    for (const [toolName, args, text] of [
      ['get_subagent_result', { agent_id: 'child-1', verbose: false }, `Agent: child-1\nType: Explore | Status: completed | Tool uses: 1 | Duration: 1.0s\nDescription: Inspect sample\n\n${report}`],
      ['steer_subagent', { agent_id: 'child-1', message: 'Read **steering instruction**' }, 'Agent "child-1" is not running (status: completed). Cannot steer a non-running agent.'],
    ] as const) {
      const shared = { provider: AgentProvider.PI, spanId: toolName, spanType: toolName }
      messages.push(
        { ...shared, id: `${toolName}-request`, content: { type: 'tool_execution_start', toolCallId: toolName, toolName, args } },
        { ...shared, id: `${toolName}-result`, content: { type: 'tool_execution_end', toolCallId: toolName, toolName, isError: false, result: { content: [{ type: 'text', text }] } } },
      )
    }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, messages)
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('Agent "Inspect sample" completed', { exact: true })).toBeVisible()
    const body = chat.getByTestId('message-bubble').filter({ hasText: 'Retrieved finding' })
    await expect(body.locator('li strong')).toHaveText('Retrieved finding')
    await body.locator('..').hover()
    await body.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(report)
    const failure = chat.getByTestId('message-bubble').filter({ hasText: 'Cannot steer a non-running agent.' })
    await expect(failure.locator('.lucide-circle-alert')).toHaveCount(1)
    await expect(chat.getByText('steering instruction', { exact: true })).toHaveCount(0)
    await chat.getByRole('button', { name: 'Show prompt', exact: true }).click()
    await expect(chat.locator('strong').filter({ hasText: 'steering instruction' })).toBeVisible()
  })

  test('renders Pi workflow completion, custom plans, and native MCP resources', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-extensions-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const provider = AgentProvider.PI
    const report = '- **Workflow finding** & details'
    const script = 'export const meta = { name: "Probe", description: "Read the sample" };\nreturn "<strong>literal script</strong>";'
    const image = Buffer.from(await createImageBytes(page, 32, 32)).toString('base64')
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { provider, id: 'workflow-request', spanId: 'workflow', spanType: 'SubagentWorkflow', content: { type: 'tool_execution_start', toolCallId: 'workflow', toolName: 'SubagentWorkflow', args: { script } } },
      { provider, id: 'workflow-result', spanId: 'workflow', spanType: 'SubagentWorkflow', content: { type: 'tool_execution_end', toolCallId: 'workflow', toolName: 'SubagentWorkflow', result: { content: [{ type: 'text', text: 'Workflow "Probe" started in the background.\nTask ID: wf_probe\nScript: /project/probe.workflow.js\n' }], details: { taskId: 'wf_probe' } } } },
      { provider, id: 'workflow-notification', content: { type: 'message_end', message: { role: 'custom', customType: 'subagent-notification', display: true, content: `<task-notification><task-id>wf_probe</task-id><result>${report.replace('&', '&amp;')}</result></task-notification>`, details: { id: 'wf_probe', description: 'Workflow probe', status: 'completed', resultPreview: 'Short preview' } } } },
      { provider, id: 'plan', content: { type: 'message_end', message: { role: 'custom', customType: 'proposed-plan', display: true, content: '## Proposed plan\n\n- Read **plan sample**.' } } },
      { provider, id: 'mcp-request', spanId: 'mcp', spanType: 'mcp', content: { type: 'tool_execution_start', toolCallId: 'mcp', toolName: 'mcp', args: { tool: 'sample_echo', args: { query: 'fixture' } } } },
      { provider, id: 'mcp-result', spanId: 'mcp', spanType: 'mcp', content: { type: 'tool_execution_end', toolCallId: 'mcp', toolName: 'mcp', result: { content: [{ type: 'text', text: '[Resource: probe://sample]\nNative resource body' }, { type: 'image', data: image, mimeType: 'image/png' }], details: { mode: 'call', server: 'sample', tool: 'echo', mcpResult: { content: [{ type: 'resource', resource: { uri: 'probe://sample', text: 'Native resource body' } }, { type: 'image', data: image, mimeType: 'image/png' }], structuredContent: { count: 0 } } } } } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('Agent "Workflow probe" completed', { exact: true })).toBeVisible()
    await chat.getByRole('button', { name: 'Show script', exact: true }).click()
    await expect(chat.getByText(script, { exact: true })).toBeVisible()
    const body = chat.getByTestId('message-bubble').filter({ hasText: 'Workflow finding' })
    await expect(body.locator('li strong')).toHaveText('Workflow finding')
    await body.locator('..').hover()
    await body.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(report)
    await expect(chat.getByRole('heading', { name: 'Proposed plan' })).toBeVisible()
    await expect(chat.getByText('sample / echo', { exact: true })).toHaveCount(1)
    await expect(chat.getByText('Native resource body', { exact: true })).toHaveCount(1)
    await expect(chat.locator('img')).toHaveCount(1)
    await expect.poll(() => chat.locator('img').evaluate((element: HTMLImageElement) => element.naturalWidth)).toBe(32)
  })

  test('recovers complete output from a real Pi MCP artifact and retains it after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.PI
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-artifact-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const code = 'emit(Array.from({length:3000},(_,i)=>"artifact-line-"+i).join("\\n")+"\\n"+["PI","ARTIFACT","RECOVERED"].join("_"))'
    await sendMessage(page, `Call mcpScript once with this exact code, then reply DONE. Do not call other tools: ${code}`)
    const output = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true }).getByTestId('message-bubble').locator('p').filter({ hasText: 'artifact-line-0' })
    await expect(output).toHaveCount(1)
    await expect(output).toContainText('artifact-line-2999')
    await expect(output).toContainText('PI_ARTIFACT_RECOVERED')
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(output).toHaveCount(1)
    await expect(output).toContainText('PI_ARTIFACT_RECOVERED')
  })

  test('renders Pi MCP script failures and opens an embedded resource image', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.PI
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-mcp-resource-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    const data = Buffer.from(await createImageBytes(page, 32, 32)).toString('base64')
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { provider, id: 'script-request', spanId: 'script', spanType: 'mcpScript', content: { type: 'tool_execution_start', toolCallId: 'script', toolName: 'mcpScript', args: { code: 'throw new Error("MCP_SCRIPT_PROBE_FAILURE")' } } },
      { provider, id: 'script-result', spanId: 'script', spanType: 'mcpScript', content: { type: 'tool_execution_end', toolCallId: 'script', toolName: 'mcpScript', isError: false, result: { content: [{ type: 'text', text: 'Error: MCP_SCRIPT_PROBE_FAILURE' }], details: { mode: 'script', error: 'script_error', timeoutMs: 30000 } } } },
      { provider, id: 'resource-request', spanId: 'resource', spanType: 'sample_read_resource', content: { type: 'tool_execution_start', toolCallId: 'resource', toolName: 'sample_read_resource', args: {} } },
      { provider, id: 'resource-result', spanId: 'resource', spanType: 'sample_read_resource', content: { type: 'tool_execution_end', toolCallId: 'resource', toolName: 'sample_read_resource', result: {
        content: [{ type: 'image', data, mimeType: 'image/png' }],
        details: { server: 'sample', resourceUri: 'probe://image', mcpResult: { contents: [{ uri: 'probe://image', blob: data, mimeType: 'image/png' }] } },
      } } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    const failure = chat.getByTestId('message-bubble').filter({ hasText: 'Error: MCP_SCRIPT_PROBE_FAILURE' })
    await expect(failure.locator('.lucide-circle-alert')).toHaveCount(1)
    await expect(chat.locator('img')).toHaveCount(1)
    await expect(chat.locator('img')).toHaveJSProperty('naturalWidth', 32)
    await expect(chat).not.toContainText(data)
    await chat.locator('img').click()
    const opened = page.locator('img[src^="blob:"]').filter({ visible: true })
    await expect(opened).toHaveJSProperty('naturalWidth', 32)
    await expect(opened).toHaveJSProperty('naturalHeight', 32)
  })

  test('renders Pi saved todos, filtered empty lists, and failed updates', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-todos-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const tasks = [{ id: 1, subject: 'Inspect sample', status: 'in_progress', activeForm: 'Inspecting sample', description: 'Read **todo sample**.' }, { id: 2, subject: 'Report findings', status: 'pending' }]
    const messages: FixtureMessage[] = []
    for (const [id, params, snapshot, error] of [
      ['list', { action: 'list' }, tasks, undefined],
      ['get', { action: 'get', id: 1 }, tasks, undefined],
      ['filtered', { action: 'list', status: 'completed' }, tasks, undefined],
      ['failed', { action: 'update', id: 99, status: 'completed' }, tasks, '#99 not found'],
      ['clear', { action: 'clear' }, [], undefined],
    ] as const) {
      const base = { provider: AgentProvider.PI, spanId: id, spanType: 'todo' }
      messages.push({ ...base, id: `${id}-request`, content: { type: 'tool_execution_start', toolCallId: id, toolName: 'todo', args: params } })
      messages.push({ ...base, id: `${id}-result`, content: { type: 'tool_execution_end', toolCallId: id, toolName: 'todo', isError: false, result: { content: [{ type: 'text', text: error ?? 'Saved' }], details: { action: params.action, params, tasks: snapshot, nextId: 3, error } } } })
    }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, messages)
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('Report findings', { exact: true })).toHaveCount(1)
    await expect(chat.locator('[data-task-checkbox="in_progress"]')).toHaveCount(2)
    await expect(chat.locator('strong').filter({ hasText: 'todo sample' })).toBeVisible()
    await expect(chat.getByText('No matching tasks', { exact: true })).toBeVisible()
    await expect(chat.getByText('To-do list cleared', { exact: true })).toBeVisible()
    const failure = chat.getByTestId('message-bubble').filter({ hasText: '#99 not found' })
    await expect(failure.locator('.lucide-circle-alert')).toHaveCount(1)
  })

  test('renders supplemental duration and opens an image from native MCP details', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-metadata-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const image = Buffer.from(await createImageBytes(page, 64, 64)).toString('base64')
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { id: 'native-image', provider: AgentProvider.PI, spanId: 'native-image', spanType: 'mcp', content: { type: 'tool_execution_end', toolCallId: 'native-image', toolName: 'mcp', result: { content: [], details: { mode: 'call', server: 'probe', tool: 'image', mcpResult: { content: [{ type: 'image', mimeType: 'image/png', data: image }] } } } } },
      { id: 'duration', provider: AgentProvider.PI, content: { type: 'agent_end', messages: [], duration_ms: 'provider value' }, metadata: { duration_ms: 2500, num_tool_uses: 0 } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('Turn ended (2.5s)', { exact: true })).toBeVisible()
    await expect(chat.locator('img')).toHaveCount(1)
    await chat.locator('img').click()
    const opened = page.locator('img[src^="blob:"]').filter({ visible: true })
    await expect(opened).toHaveJSProperty('naturalWidth', 64)
    await expect(opened).toHaveJSProperty('naturalHeight', 64)
  })

  test('renders ZCode native question descriptions and selects an answer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await page.setViewportSize({ width: 600, height: 900 })
    const provider = AgentProvider.ZCODE
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-zcode-question-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    const diagram = '┌────────┐\n│ sample │\n└────────┘'
    const questions = [{ question: 'Pick a color.', header: 'Color', options: [{ label: 'Blue', value: 'Blue', description: 'Choose the color blue.', preview: diagram }, { label: 'Green', value: 'Green', description: 'Choose the color green.', preview: '```ts\nconst color = "green"\n```' }] }]
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [], {
      type: 'control_request',
      request_id: 'fixture-permission',
      id: 'server-3',
      method: 'interaction/requestUserInput',
      request: { tool_name: 'AskUserQuestion', input: { questions: questions.map(question => ({ ...question, options: question.options.map(({ label, value }) => ({ label, value })) })) } },
      params: { requestId: 'fixture-permission', toolName: 'AskUserQuestion', questions, schema: { toolName: 'AskUserQuestion' } },
    })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner.getByText('Choose the color blue.', { exact: true })).toBeVisible()
    await expect(banner.getByText('Choose the color green.', { exact: true })).toBeVisible()
    const preview = banner.getByRole('region', { name: 'Blue preview' })
    await expect(preview).toContainText('│ sample │')
    await expect(preview).toHaveCSS('white-space', 'pre')
    await expect(banner.getByRole('region', { name: 'Green preview' }).locator('pre code')).toContainText('const color = "green"')
    await expect(banner.getByRole('region', { name: 'Green preview' })).toBeInViewport()
    expect(await banner.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
    const option = banner.getByTestId('question-option-Blue')
    await option.click()
    await expect(option.getByRole('radio')).toBeChecked()
    await expect(page.getByTestId('control-submit-btn')).toBeEnabled()
  })

  test('loads complete Pi previews through the persisted control source', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.PI
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-question-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    const preview = `\`\`\`txt\n${'Diagram line\n'.repeat(80)}complete-preview-end\n\`\`\``
    const questions = [{ question: 'Choose a layout', header: 'Layout', options: [{ label: 'Compact', description: 'Small', preview }, { label: 'Wide', description: 'Large' }] }]
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { id: 'question-source', provider, spanId: 'question-tool', spanType: 'ask_user_question', content: { type: 'tool_execution_start', toolCallId: 'question-tool', toolName: 'ask_user_question', args: { questions } } },
    ], {
      type: 'extension_ui_request',
      id: 'fixture-permission',
      method: 'select',
      title: `[Layout] Choose a layout\n\n--- 1. Compact preview ---\n${preview.slice(0, 600)}`,
      options: ['1. Compact — Small', '2. Wide — Large', '3. Type something.'],
    }, 'question-source')
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner.getByRole('region', { name: 'Compact preview' })).toContainText('complete-preview-end')
    await expect(banner.getByText('Choose a layout', { exact: true })).toBeVisible()
    await expect(banner.getByText('Small', { exact: true })).toBeVisible()
    const option = banner.getByTestId('question-option-Compact')
    await option.click()
    await expect(option.getByRole('radio')).toBeChecked()
  })

  for (const answerKind of ['custom', 'selected']) {
    test(`delivers a ${answerKind} answer to the real Pi question extension`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
      const provider = AgentProvider.PI
      await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-custom-'), {
        agentProvider: provider,
        ...realAgentOpenOptions(realAgentSettings(provider)),
      })
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await sendMessage(page, 'Call ask_user_question exactly once with {"questions":[{"question":"Choose a style","header":"Style","options":[{"label":"Alpha","description":"Use the first style."},{"label":"Beta","description":"Use the second style."}]}]}. Do not use other tools. After the answer arrives, reply with one short sentence.')
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toContainText('Choose a style')
      if (answerKind === 'custom') {
        const editor = page.locator('[data-testid="composer-editor"] .ProseMirror').filter({ visible: true })
        await editor.click()
        await page.keyboard.insertText('A custom style')
      }
      else {
        await banner.getByTestId('question-option-Beta').click()
      }
      await page.getByTestId('control-submit-btn').click()
      const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
      const result = chat.locator('[data-testid="message-bubble"][data-role="agent"]').filter({ hasText: 'User has answered your questions:' }).filter({ visible: true })
      await expect(result).toContainText(answerKind === 'custom' ? 'A custom style' : 'Beta')
      await expect(banner).toHaveCount(0)
      await expect(chat.getByText('User declined to answer questions', { exact: true })).toHaveCount(0)
    })
  }

  test('controls a real Pi goal through the shared goal panel and confirmation', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.PI
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-goal-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(page.locator('[data-testid="section-header-todos"]:visible')).toBeVisible()
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill('Keep this disposable goal active until the operator pauses or clears it. Do not call tools or change files.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expectGoalStatus(page, 'active')
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expectGoalStatus(page, 'paused')
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Clear goal?')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })

  test('keeps plan options reachable before the rightmost decisions in a narrow composer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await page.setViewportSize({ width: 360, height: 900 })
    const provider = AgentProvider.PI
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-plan-footer-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [], {
      type: 'extension_ui_request',
      id: 'fixture-permission',
      method: 'select',
      title: 'Proposed plan ready. What next?',
      options: ['Implement here', 'Start fresh and implement', 'Export plan…', 'Stay in Plan mode'],
    })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const footer = page.getByTestId('control-footer').filter({ visible: true })
    const clearContext = footer.getByTestId('plan-clear-context-checkbox')
    await expect(clearContext).toBeInViewport({ ratio: 1 })
    await expect(clearContext).toHaveCSS('white-space', 'nowrap')
    await clearContext.click()
    await expect(clearContext.getByRole('switch')).toBeChecked()
    const more = footer.getByRole('button', { name: 'More actions' })
    await more.click()
    await expect(page.getByRole('menuitem', { name: 'Export plan…' })).toBeVisible()
    await page.keyboard.press('Escape')
    const reject = footer.getByRole('button', { name: 'Reject', exact: true })
    const approve = footer.getByRole('button', { name: 'Approve', exact: true })
    await approve.scrollIntoViewIfNeeded()
    // Native scroll offsets round to whole pixels, while button widths can contain fractions.
    await expect(reject).toBeInViewport({ ratio: 0.99 })
    await expect(approve).toBeInViewport({ ratio: 0.99 })
    const moreBox = await more.boundingBox()
    const rejectBox = await reject.boundingBox()
    const approveBox = await approve.boundingBox()
    expect(moreBox!.x + moreBox!.width).toBeLessThanOrEqual(rejectBox!.x)
    expect(rejectBox!.x + rejectBox!.width).toBeLessThanOrEqual(approveBox!.x)
  })

  test('tracks a fresh Pi implementation session after plan approval', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.PI
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-fresh-plan-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    const readSession = async () => (await listAgents(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, [agentId]))?.[0]?.agentSessionId ?? ''
    await expect.poll(readSession).not.toBe('')
    const originalSession = await readSession()
    expect(originalSession).not.toBe('')
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, '/plan start')
    await sendMessage(page, 'Call plan_mode_complete with exactly {"plan":"# Fresh implementation probe\\n\\n- Reply with FRESH_PLAN_DONE. Do not call tools or change files."}. Do not call other tools or implement the plan before approval.')
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-clear-context-checkbox').filter({ visible: true }).click()
    await page.getByTestId('plan-approve-btn').click()
    await expect.poll(async () => {
      const current = await readSession()
      return current !== '' && current !== originalSession
    }).toBe(true)
    await expect(page.locator('[data-chat-scroll-container="true"]').filter({ visible: true }).getByText('FRESH_PLAN_DONE', { exact: true })).toBeVisible()
  })

  test('formats native argument JSON and preserves large numeric literals', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.ZCODE
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-argument-json-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    const args = '{"filters":{"limit":0,"enabled":false},"values":[1,2],"count":900719925474099312345}'
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [{
      provider,
      id: 'mcp-arguments',
      spanId: 'arguments',
      spanType: 'mcp__docs__lookup',
      content: { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'arguments', result: { success: true, content: 'Argument formatting verified', display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup', input: args } } } },
    }])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const row = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true }).getByTestId('message-bubble').filter({ hasText: 'Argument formatting verified' }).locator('..')
    await row.hover()
    await expect(row.getByRole('button', { name: 'Expand', exact: true })).toHaveCount(1)
    await row.getByRole('button', { name: 'Expand', exact: true }).click()
    await expect.poll(() => row.textContent()).toContain(prettifyJson(args))
    await expect(row).toContainText('900719925474099312345')
  })

  test('renders a persisted ZCode plan control frame in the transcript', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.ZCODE
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-plan-fallback-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [{
      id: 'plan-source',
      provider,
      spanId: 'plan',
      spanType: 'ExitPlanMode',
      content: { id: 'server-1', method: 'interaction/requestUserInput', params: { requestId: 'plan', schema: { interaction: 'plan_approval' }, input: { plan: '# Persisted plan\n\n- Keep **original bytes**.' } } },
    }], { type: 'control_request', request_id: 'fixture-permission', request: { tool_name: 'ExitPlanMode', input: {} } })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByRole('heading', { name: 'Persisted plan' }).filter({ visible: true })).toHaveCount(1)
    await expect(page.getByTestId('control-banner').getByRole('heading', { name: 'Persisted plan' })).toHaveCount(0)
  })

  for (const provider of [AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE, AgentProvider.CODEX]) {
    test(`renders shared plan content for provider ${provider}`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-plan-content-'), {
        agentProvider: provider,
        ...realAgentOpenOptions(realAgentSettings(provider)),
      })
      const plan = '# Proposed change\n\n- Keep **original bytes**.'
      const request = provider === AgentProvider.CLAUDE_CODE
        ? { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'plan', name: 'ExitPlanMode', input: { plan } }] } }
        : provider === AgentProvider.CODEX
          ? { item: { type: 'plan', id: 'plan', text: plan } }
          : { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'plan', toolName: 'ExitPlanMode', input: { allowedPrompts: [] } } }
      const supplemental = provider === AgentProvider.ZCODE
        ? { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'plan', input: { plan } } }
        : undefined
      await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [{ id: 'plan', provider, spanId: 'plan', spanType: 'ExitPlanMode', content: request, supplemental }], {
        type: 'control_request',
        request_id: 'fixture-permission',
        request: { tool_name: provider === AgentProvider.CODEX ? 'CodexPlanModePrompt' : 'ExitPlanMode', input: { plan, allowedPrompts: provider === AgentProvider.CLAUDE_CODE ? [null, { tool: 'Bash', prompt: 'run tests' }] : undefined } },
      })
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner.getByText('Plan Ready for Review', { exact: true })).toBeVisible()
      await expect(banner.getByRole('heading', { name: 'Proposed change' })).toHaveCount(0)
      const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
      await expect(chat.getByRole('heading', { name: 'Proposed change' }).filter({ visible: true })).toHaveCount(1)
      await expect(chat.locator('li strong').filter({ visible: true })).toHaveText('original bytes')
      const bodyFont = await page.locator('body').evaluate(element => getComputedStyle(element).fontFamily)
      await expect(chat.locator('li strong').filter({ visible: true })).toHaveCSS('font-family', bodyFont)
      if (provider === AgentProvider.CLAUDE_CODE)
        await expect(banner.getByText('Bash: run tests', { exact: true })).toBeVisible()
    })
  }

  test('renders a Codex file image and opens its full resolution in the file viewer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const directory = createTestDirectory('renderer-file-image-')
    const path = join(directory, 'transcript-image.png')
    const bytes = await createImageBytes(page, 512, 512)
    expect(bytes.length).toBeGreaterThan(256 * 1024)
    writeFileSync(path, bytes)
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const content = { item: { type: 'imageView', id: 'view-image', path } }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { id: 'image-request', provider: AgentProvider.CODEX, spanId: 'view-image', spanType: 'imageView', content: { ...content, startedAtMs: 1 } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('transcript-image.png', { exact: true }).filter({ visible: true })).toHaveCount(1)
    await expect(chat.locator('img[src^="data:image/png"]').filter({ visible: true })).toHaveCount(0)
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      { id: 'image-result', provider: AgentProvider.CODEX, spanId: 'view-image', spanType: 'imageView', content: { ...content, completedAtMs: 2 } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const preview = chat.locator('img[src^="data:image/png"]').filter({ visible: true })
    await expect(preview).toHaveCount(1)
    await expect(preview).toHaveJSProperty('naturalWidth', 512)
    await expect(chat.getByText('transcript-image.png', { exact: true }).filter({ visible: true })).toHaveCount(1)
    await chat.getByRole('button', { name: 'Open image', exact: true }).filter({ visible: true }).click()
    const opened = page.locator('img[src^="blob:"]').filter({ visible: true })
    await expect(opened).toHaveJSProperty('naturalWidth', 512)
    await expect(opened).toHaveJSProperty('naturalHeight', 512)
  })

  test('renders ZCode read errors and fetched Markdown with streamed request input', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.ZCODE
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-zcode-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      {
        id: 'read-request',
        provider,
        spanId: 'read-failure',
        spanType: 'Read',
        content: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'read-failure', toolName: 'Read', inputOmitted: true, inputRef: 'model_stream' } },
        supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'read-failure', input: { file_path: '/project/missing-renderer-fixture.ts' } } },
      },
      { id: 'read-result', provider, spanId: 'read-failure', spanType: 'Read', content: { type: 'tool.updated', payload: { kind: 'error', toolCallId: 'read-failure', error: { message: 'Renderer fixture does not exist' } } } },
      {
        id: 'fetch-request',
        provider,
        spanId: 'fetch',
        spanType: 'WebFetch',
        content: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'fetch', toolName: 'WebFetch', inputOmitted: true, inputRef: 'model_stream' } },
        supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'fetch', input: { url: 'https://example.com', prompt: 'Return the page title' } } },
      },
      { id: 'fetch-result', provider, spanId: 'fetch', spanType: 'WebFetch', content: { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'fetch', result: { success: true, content: '# Native page heading' } } } },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(page.getByText('/project/missing-renderer-fixture.ts', { exact: true }).filter({ visible: true })).toBeVisible()
    await expect(page.getByText('Failed', { exact: true }).filter({ visible: true })).toBeVisible()
    await expect(page.getByText('Renderer fixture does not exist', { exact: true }).filter({ visible: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Native page heading' }).filter({ visible: true })).toBeVisible()
  })

  test('loads permission arguments from an earlier request', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.OPENCODE
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-permission-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    const request: FixtureMessage = {
      id: 'permission-request',
      provider,
      spanId: 'permission-call',
      spanType: 'execute',
      content: { sessionUpdate: 'tool_call', toolCallId: 'permission-call', status: 'pending', kind: 'execute', rawInput: { command: 'printf recovered-permission-command' } },
    }
    const filler: FixtureMessage[] = Array.from({ length: 350 }, (_, index) => ({
      id: `permission-filler-${index}`,
      provider,
      content: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: `Earlier text ${index}` } },
    }))
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [request, ...filler], {
      jsonrpc: '2.0',
      id: 'fixture-permission',
      method: 'session/request_permission',
      params: {
        toolCall: { toolCallId: 'permission-call', title: 'Review command', kind: 'execute' },
        options: [{ optionId: 'once', kind: 'allow_once', name: 'Allow' }, { optionId: 'reject', kind: 'reject_once', name: 'Deny' }],
      },
    })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const banner = page.getByTestId('control-banner')
    await expect(banner).toContainText('Review command')
    await expect(banner.locator('pre')).toContainText('printf recovered-permission-command')
    await expect(page.getByRole('button', { name: 'Allow', exact: true })).toBeVisible()
  })

  test('renders task lists and fetched Markdown with shared components', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-rich-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [
      {
        id: 'goose-todo',
        provider: AgentProvider.GOOSE,
        spanId: 'goose-todo',
        spanType: 'edit',
        content: { sessionUpdate: 'tool_call_update', toolCallId: 'goose-todo', status: 'completed', kind: 'edit', rawInput: { content: '- [x] Inspect sources\n- [ ] Verify display' }, _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } } },
      },
      {
        id: 'reasonix-fetch',
        provider: AgentProvider.REASONIX,
        spanId: 'reasonix-fetch',
        spanType: 'fetch',
        content: { sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-fetch', status: 'completed', title: 'web_fetch', rawInput: { url: 'https://example.com' }, content: [{ type: 'content', content: { type: 'text', text: '## Recovered page title\n\n**Formatted page body**' } }] },
      },
      {
        id: 'reasonix-receipt',
        provider: AgentProvider.REASONIX,
        spanId: 'reasonix-receipt',
        spanType: 'edit',
        content: { sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-receipt', status: 'completed', title: 'edit_file', rawInput: { path: '/project/receipt.ts', old_string: 'requested', new_string: 'actualAfter' }, content: [{ type: 'content', content: { type: 'text', text: 'edited /project/receipt.ts (fuzzy match)\nActual replacement receipt after write:\n@@ replacement 1 of 1 (1 occurrence(s), fuzzy match) @@\n-actualBefore\n+actualAfter\n' } }] },
      },
    ])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(page.getByText('Inspect sources', { exact: true }).filter({ visible: true })).toBeVisible()
    await expect(page.getByText('Verify display', { exact: true }).filter({ visible: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Recovered page title' }).filter({ visible: true })).toBeVisible()
    await expect(page.locator('strong:visible').filter({ hasText: 'Formatted page body' })).toBeVisible()
    await expect(page.locator('[data-file-diff]:visible').filter({ hasText: 'actualAfter' })).toBeVisible()
    await expect(page.getByText('Fuzzy match', { exact: true }).filter({ visible: true })).toBeVisible()
  })

  for (const [label, provider] of providers) {
    test(`${label} renders an applied file edit`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-'), {
        agentProvider: AgentProvider.CLAUDE_CODE,
        ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
      })
      await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, editFixture(provider))
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const content = page.locator('[data-file-diff]:visible')
      await expect(content.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
      await expect(content.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
    })
  }

  test('loads a missing request and opens a supplemented image', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const imageData = (await createImageBytes(page, 24, 16)).toString('base64')
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-context-'), {
      agentProvider: AgentProvider.CLAUDE_CODE,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CLAUDE_CODE)),
    })
    const pair = editFixture(AgentProvider.PI)
    const filler: FixtureMessage[] = Array.from({ length: 350 }, (_, index) => ({
      id: `filler-${index}`,
      provider: AgentProvider.CLAUDE_CODE,
      content: { type: 'assistant', message: { content: [{ type: 'text', text: `History row ${index}` }] } },
    }))
    const image: FixtureMessage = {
      id: 'image',
      provider: AgentProvider.CURSOR,
      spanId: 'image',
      spanType: 'other',
      content: { sessionUpdate: 'tool_call_update', toolCallId: 'image', kind: 'other', status: 'completed', title: 'Recovered image' },
      supplemental: {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'image',
        status: 'completed',
        rawOutput: {
          content: [{ type: 'tool-result', toolCallId: 'image', toolName: 'mcp_probe_image', providerOptions: { cursor: { imageDescriptions: { 0: 'Recovered image description' } } } }],
          providerOptions: { cursor: { highLevelToolCallResult: { output: { success: { content: [{ image: { mimeType: 'image/png', data: imageData } }] } } } } },
        },
      },
    }
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [pair[0], ...filler, pair[1], image])
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(page.locator('[data-file-diff]:visible').filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await page.getByRole('button', { name: 'Open image', exact: true }).filter({ visible: true }).click()
    await expect(page.locator('[data-tab-type="image"]')).toBeVisible()
    await expect(page.locator('img:visible').last()).toBeVisible()
    await expect(page.locator('img:visible').last()).toHaveAttribute('alt', 'Recovered image description')
  })
})

for (const [label, provider] of [['Claude Code', AgentProvider.CLAUDE_CODE], ['Codex', AgentProvider.CODEX], ['Reasonix', AgentProvider.REASONIX], ['ZCode', AgentProvider.ZCODE]] as const) {
  test(`formats ${label} permission JSON for a narrow panel`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await page.setViewportSize({ width: 800, height: 1000 })
    const input = provider === AgentProvider.CODEX
      ? { network: { enabled: true }, fileSystem: { readOnly: ['sample.py'] } }
      : { query: 'answer', path: 'sample.py', limit: 20 }
    const permission = provider === AgentProvider.CODEX
      ? { method: 'item/permissions/requestApproval', params: { permissions: input } }
      : provider === AgentProvider.REASONIX
        ? { method: 'session/request_permission', params: { toolCall: { toolCallId: 'probe-permission', title: 'Probe', rawInput: input }, options: [{ optionId: 'once', name: 'Allow once', kind: 'allow_once' }] } }
        : { request: { tool_name: 'Probe', input } }
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('permission-json-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    await seedMessages(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId, [], permission)
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    const code = banner.locator('pre')
    await expect(code).toBeVisible()
    await expect(code).toHaveCSS('white-space', 'pre')
    await expect.poll(async () => (await code.textContent())!.trim().split('\n').length).toBeGreaterThan(1)
    const expand = banner.getByRole('button', { name: /Show .*more line/ })
    if (await expand.count())
      await expand.click()
    expect(JSON.parse((await code.textContent())!)).toEqual(input)
  })
}
