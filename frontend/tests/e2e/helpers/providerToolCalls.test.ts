import type { TodoStatus } from './providerToolCalls'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { runInNewContext } from 'node:vm'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import {

  askUserQuestionToolCall,
  backgroundBashToolCall,
  bashToolCall,
  blockGoalToolCall,
  claudeSubagentHandbackToolCall,

  codeExecutionToolCall,

  codexEscalatedCommandToolCall,
  codexExecToolCall,
  codexWaitAgentToolCall,

  completeGoalToolCall,
  copilotApplyPatchToolCall,
  createGoalToolCall,
  cursorCreatePlanToolCall,
  cursorGenerateImageToolCall,
  cursorMergeTodosToolCall,
  cursorWebFetchPermissionToolCall,

  editToolCall,
  enterPlanModeToolCall,
  exitPlanModeFromFileToolCall,
  exitPlanModeToolCall,

  goosePermissionJudgmentToolCall,

  hasToolFor,
  mcpToolCall,
  mimoTaskToolCall,
  mimoWorkflowToolCall,
  piCodemodeToolCall,
  piEditorProbeToolCall,
  piMcpResourceToolCall,
  piTodoToolCall,
  piWorkflowToolCall,

  readToolCall,
  reasonixInspectCapabilityToolCall,
  reasonixListCapabilitiesToolCall,
  spawnSubagentBatchToolCall,
  spawnSubagentToolCall,
  updateTodosToolCall,
  writeToolCall,
  zcodeReadRangeToolCall,
} from './providerToolCalls'
import { quotePosixShellArgument } from './shellArguments'

describe('reasonix capability discovery', () => {
  it('requests the complete installed list without an unsupported page cursor', () => {
    const call = reasonixListCapabilitiesToolCall('catalog')
    expect(call.name).toBe('use_capability')
    expect(call.arguments).toEqual({ action: 'list' })
  })

  it('keeps an exact deferred capability identity during inspect', () => {
    expect(reasonixInspectCapabilityToolCall('inspect', 'tool:notebook_edit').arguments)
      .toEqual({ action: 'inspect', capability_id: 'tool:notebook_edit' })
  })

  it.each(['', ' ', '\n'])('rejects an empty list call ID: %j', (id) => {
    expect(() => reasonixListCapabilitiesToolCall(id)).toThrow('nonempty call ID')
  })

  it.each(['', ' ', ' tool:notebook_edit', 'tool:notebook_edit '])('rejects an absent or padded inspect identity: %j', (id) => {
    expect(() => reasonixInspectCapabilityToolCall('inspect', id)).toThrow('exact nonempty')
  })

  it('rejects an absent inspect call identity', () => {
    expect(() => reasonixInspectCapabilityToolCall('', 'tool:grep')).toThrow('exact nonempty')
  })
})

describe('native Codex code-mode result metadata', () => {
  it.each([0, 7])('retains the actual computed exit %s and output from the generated shell wrapper', async (exitCode) => {
    const program = `process.stdout.write('CALCULATED'+String(40+2));process.stderr.write('STDERR'+String(70+7));process.exitCode=${exitCode}`
    const executable = process.platform === 'win32' ? JSON.stringify(process.execPath) : quotePosixShellArgument(process.execPath)
    const command = `${executable} -e ${JSON.stringify(program)}`
    const outputs: unknown[] = []
    const call = bashToolCall(AgentProvider.CODEX, 'native-exit', command)
    await runInNewContext(`(async () => { ${call.input} })()`, {
      tools: { exec_command: async (request: { cmd: string }) => {
        const result = spawnSync(request.cmd, { shell: true, encoding: 'utf8' })
        if (result.error)
          throw result.error
        return { output: `${result.stdout}${result.stderr}`, exit_code: result.status, wall_time_seconds: 0 }
      } },
      text: (value: unknown) => outputs.push(value),
    })
    expect(outputs).toHaveLength(1)
    expect(outputs).toEqual([JSON.stringify({ output: 'CALCULATED42STDERR77', exit_code: exitCode, wall_time_seconds: 0 })])
  })

  it('retains the escalated command result instead of discarding its native status', async () => {
    const outputs: unknown[] = []
    const call = codexEscalatedCommandToolCall('native-escalated', 'printf native-result')
    await runInNewContext(`(async () => { ${call.input} })()`, {
      tools: { exec_command: async (request: { cmd: string, sandbox_permissions: string }) => {
        expect(request.cmd).toBe('printf native-result')
        expect(request.sandbox_permissions).toBe('require_escalated')
        return { output: 'native-result', exit_code: 0, wall_time_seconds: 0 }
      } },
      text: (value: unknown) => outputs.push(value),
    })
    expect(outputs).toEqual([JSON.stringify({ output: 'native-result', exit_code: 0, wall_time_seconds: 0 })])
  })

  it('retains native read output and its zero exit status', async () => {
    const outputs: unknown[] = []
    const call = readToolCall(AgentProvider.CODEX, 'native-read', '/private/file with spaces.txt')
    await runInNewContext(`(async () => { ${call.input} })()`, {
      tools: { exec_command: async (request: { cmd: string }) => {
        expect(request.cmd).toBe('cat \'/private/file with spaces.txt\'')
        return { output: 'native file\n', exit_code: 0 }
      } },
      text: (value: unknown) => outputs.push(value),
    })
    expect(outputs).toEqual([JSON.stringify({ output: 'native file\n', exit_code: 0 })])
  })
})

describe('native Goose text Read tool', () => {
  it.each(['/private/note.txt', '/private/path $(touch marker); \'quoted\'/note.txt'])('uses the actual read operation for %j', (path) => {
    expect(readToolCall(AgentProvider.GOOSE, 'native-goose-read', path)).toEqual({ id: 'native-goose-read', name: 'read', arguments: { path } })
  })
})

describe('zcodeReadRangeToolCall', () => {
  it.each([
    { offset: 0, limit: 1 },
    { offset: 1, limit: 1 },
    { offset: Number.MAX_SAFE_INTEGER, limit: Number.MAX_SAFE_INTEGER },
  ])('passes the exact source-valid native Read range %j', (range) => {
    expect(zcodeReadRangeToolCall('native-fresh-read', '/private/native file.txt', range))
      .toEqual({ id: 'native-fresh-read', name: 'Read', arguments: { file_path: '/private/native file.txt', ...range } })
  })
  it.each([
    { offset: -1, limit: 1 },
    { offset: 0.5, limit: 1 },
    { offset: Number.MAX_SAFE_INTEGER + 1, limit: 1 },
    { offset: Number.NaN, limit: 1 },
    { offset: 1, limit: 0 },
    { offset: 1, limit: -1 },
    { offset: 1, limit: 0.5 },
    { offset: 1, limit: Number.POSITIVE_INFINITY },
  ])('refuses a range outside the native integer schema: %j', (range) => {
    expect(() => zcodeReadRangeToolCall('native-fresh-read', '/private/native file.txt', range)).toThrow('The native ZCode Read range requires a nonnegative integer offset and a positive integer limit.')
  })
})

describe('goosePermissionJudgmentToolCall', () => {
  it.each([{ ids: [] }, { ids: ['actual-read'] }, { ids: ['실제 요청', 'quoted"request', ''] }])('preserves the native read-only IDs without sharing the input array: $ids', ({ ids }) => {
    const result = goosePermissionJudgmentToolCall('native-judge', ids)
    expect(result).toEqual({ id: 'native-judge', name: 'platform__tool_by_tool_permission', arguments: { read_only_request_ids: ids } })
    expect(result.arguments?.read_only_request_ids).not.toBe(ids)
  })
})

describe('piCodemodeToolCall', () => {
  it.each([{ label: 'empty', code: '' }, { label: 'Unicode', code: 'text("실제 출력");\n' }, { label: 'large', code: 'text("line");\n'.repeat(2_000) }])('preserves every character of the $label native script', ({ code }) => {
    expect(piCodemodeToolCall('native-mcp-script', code)).toEqual({ id: 'native-mcp-script', name: 'codemode', arguments: { code } })
  })
})

describe('piMcpResourceToolCall', () => {
  it.each([
    { operation: 'list' as const, name: 'list_mcp_resources' },
    { operation: 'templates' as const, name: 'list_mcp_resource_templates' },
  ])('uses the native $operation resource catalog', ({ operation, name }) => {
    expect(piMcpResourceToolCall('resource', { operation, server: 'probe' })).toEqual({ id: 'resource', name, arguments: { server: 'probe' } })
  })

  it('preserves an exact resource URI in the native read', () => {
    expect(piMcpResourceToolCall('resource', { operation: 'read', server: 'probe', uri: 'probe://text/출력?zero=0' }))
      .toEqual({ id: 'resource', name: 'read_mcp_resource', arguments: { server: 'probe', uri: 'probe://text/출력?zero=0' } })
  })

  it('refuses absent call identity, server identity, and read URI', () => {
    expect(() => piMcpResourceToolCall('', { operation: 'list', server: 'probe' })).toThrow('requires a call ID')
    expect(() => piMcpResourceToolCall('resource', { operation: 'list', server: '' })).toThrow('requires a call ID')
    expect(() => piMcpResourceToolCall('resource', { operation: 'read', server: 'probe' })).toThrow('URI for a read')
  })
})

describe('piEditorProbeToolCall', () => {
  it('uses the exact disposable editor extension vocabulary', () => {
    expect(piEditorProbeToolCall('native-editor')).toEqual({ id: 'native-editor', name: 'editor_probe', arguments: {} })
  })
})

describe('claudeSubagentHandbackToolCall', () => {
  it.each(['', '  Original report.\nSecond line: 실제 내용 🧪\t  '])('preserves the exact native report: %j', (message) => {
    expect(claudeSubagentHandbackToolCall('native-handback', message)).toEqual({
      id: 'native-handback',
      name: 'SubagentHandback',
      arguments: { message },
    })
  })
})

describe('copilotApplyPatchToolCall', () => {
  it('preserves the native multiline freeform patch input', () => {
    const patch = '*** Begin Patch\n*** Update File: /project/native.ts\n@@\n-before\n+after\n*** End Patch\n'
    expect(copilotApplyPatchToolCall('native-patch', patch)).toEqual({ id: 'native-patch', name: 'apply_patch', input: patch })
  })

  it('preserves empty native input for the runtime refusal', () => {
    expect(copilotApplyPatchToolCall('empty-patch', '')).toEqual({ id: 'empty-patch', name: 'apply_patch', input: '' })
  })
})

describe('piWorkflowToolCall', () => {
  it('preserves the native metadata and two-stage workflow source', () => {
    const script = 'export const meta = { name: \'Probe\', description: \'Run two native stages.\' };\nphase(\'First\');\nconst first = await agent(\'Run the first stage.\');\nphase(\'Second\');\nreturn [first, await agent(\'Run the second stage.\')];'
    expect(piWorkflowToolCall('native-workflow', script)).toEqual({ id: 'native-workflow', name: 'SubagentWorkflow', arguments: { script } })
  })

  it('preserves an empty script for native metadata validation', () => {
    expect(piWorkflowToolCall('empty-workflow', '').arguments).toEqual({ script: '' })
  })
})

describe('cursor execution tool calls', () => {
  it('requests an actual local child without adding mock metadata to the native Task arguments', () => {
    const call = spawnSubagentToolCall(AgentProvider.CURSOR, 'native-task', {
      description: 'Actual local child',
      prompt: 'Run the native child.',
      nativeExecution: { modelId: 'mock-grok' },
    })
    expect(call).toEqual({
      id: 'native-task',
      name: 'task',
      arguments: { description: 'Actual local child', prompt: 'Run the native child.', report: '' },
      nativeExecution: { modelId: 'mock-grok' },
    })
  })
  it('builds source-backed shell and file operations for the native Run surface', () => {
    expect(bashToolCall(AgentProvider.CURSOR, 'shell', 'printf actual')).toEqual({ id: 'shell', name: 'shell', arguments: { command: 'printf actual' } })
    expect(readToolCall(AgentProvider.CURSOR, 'read', '/project/a.txt')).toEqual({ id: 'read', name: 'read', arguments: { path: '/project/a.txt' } })
    expect(writeToolCall(AgentProvider.CURSOR, 'write', { path: '/project/a.txt', content: '' })).toEqual({ id: 'write', name: 'write', arguments: { path: '/project/a.txt', content: '' } })
    expect(editToolCall(AgentProvider.CURSOR, 'edit', { path: '/project/a.txt', before: 'old', after: '' })).toEqual({ id: 'edit', name: 'edit', arguments: { path: '/project/a.txt', before: 'old', after: '' } })
  })

  it('keeps a task completion gate outside the native task arguments', () => {
    const call = spawnSubagentToolCall(AgentProvider.CURSOR, 'task', { description: 'Hold the child', prompt: 'Do the child work.', report: 'Child complete.', completionGate: 'child-completion' })
    expect(call).toMatchObject({ completionGate: 'child-completion' })
    expect(call.arguments).toEqual({ description: 'Hold the child', prompt: 'Do the child work.', report: 'Child complete.' })
  })

  it('keeps native child progress in service metadata instead of TaskArgs', () => {
    const call = spawnSubagentToolCall(AgentProvider.CURSOR, 'task-progress', { description: 'Actual child delta', prompt: 'Do the child work.', report: 'Child complete.', taskProgress: 'CHILD_NATIVE_PROGRESS' })
    expect(call).toMatchObject({ taskProgress: 'CHILD_NATIVE_PROGRESS' })
    expect(call.arguments).toEqual({ description: 'Actual child delta', prompt: 'Do the child work.', report: 'Child complete.' })
  })
})

describe('zcode MCP tool calls', () => {
  it('uses the actual native catalog tool ID and preserves empty, zero, and false arguments', () => {
    expect(mcpToolCall(AgentProvider.ZCODE, 'native-ask', { server: 'form_probe', tool: 'ask', input: {} }))
      .toEqual({ id: 'native-ask', name: 'mcp__form_probe__ask', arguments: {} })
    expect(mcpToolCall(AgentProvider.ZCODE, 'native-echo', { server: 'form_probe', tool: 'echo', input: { count: 0, enabled: false, text: '' } }))
      .toEqual({ id: 'native-echo', name: 'mcp__form_probe__echo', arguments: { count: 0, enabled: false, text: '' } })
  })
})

describe('readToolCall', () => {
  it.skipIf(process.platform === 'win32')('reads an actual Codex file path with spaces and shell metacharacters as one argument', async () => {
    const scratch = fileURLToPath(new URL('../../../../.tmp/', import.meta.url))
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'codex-read-argument-'))
    const path = join(directory, 'source $(printf WRONG_PATH) with spaces.txt')
    const expected = 'ACTUAL_NATIVE_FILE_CONTENT\n'
    writeFileSync(path, expected)
    try {
      const call = readToolCall(AgentProvider.CODEX, 'native-read', path)
      if (call.input === undefined)
        throw new Error('The native Codex read has no executable input.')
      let output: string | undefined
      await runInNewContext(`(async () => { ${call.input} })()`, {
        tools: {
          exec_command: async (options: { cmd: string }) => ({ output: execFileSync('/bin/sh', ['-c', options.cmd], { cwd: directory, encoding: 'utf8' }), exit_code: 0 }),
        },
        text: (value: string) => { output = value },
      })
      expect(typeof output).toBe('string')
      if (output === undefined)
        throw new Error('The actual native Read wrapper emitted no result.')
      expect(JSON.parse(output)).toEqual({ output: expected, exit_code: 0 })
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })
})

describe('codexExecToolCall', () => {
  it('preserves the native freeform JavaScript source without adding function arguments', () => {
    const source = 'const values = await Promise.all([tools.first(), tools.second()]);\ntext(values)'
    expect(codexExecToolCall('native-exec', source)).toEqual({ id: 'native-exec', name: 'exec', input: source })
  })

  it('keeps an empty native source distinct from missing custom input', () => {
    expect(codexExecToolCall('empty', '')).toEqual({ id: 'empty', name: 'exec', input: '' })
  })
})

describe('codexWaitAgentToolCall', () => {
  it('uses the native collaboration namespace for an empty agent wait', () => {
    expect(codexWaitAgentToolCall('empty-agent-wait', 1)).toEqual({
      id: 'empty-agent-wait',
      name: 'wait_agent',
      namespace: 'collaboration',
      arguments: { timeout_ms: 1 },
    })
  })

  it('omits the timeout so the native handler selects its default', () => {
    expect(codexWaitAgentToolCall('default-agent-wait').arguments).toEqual({})
  })

  it.each([0, -1, Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER])('preserves the integer timeout %s for native validation', (timeoutMs) => {
    expect(codexWaitAgentToolCall('native-timeout', timeoutMs).arguments).toEqual({ timeout_ms: timeoutMs })
  })

  it.each([0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('rejects a timeout that JSON cannot preserve as a safe integer: %s', (timeoutMs) => {
    expect(() => codexWaitAgentToolCall('invalid-timeout', timeoutMs)).toThrow('must be a safe integer')
  })
})

describe('Cursor interactive tool calls', () => {
  it('builds a question with stable native question and option ids', () => {
    expect(askUserQuestionToolCall(AgentProvider.CURSOR, 'cursor-q', [{
      header: 'Color',
      question: 'Which color?',
      options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
    }])).toEqual({
      id: 'cursor-q',
      name: 'askQuestion',
      arguments: {
        title: 'Color',
        questions: [{
          id: 'question-1',
          prompt: 'Which color?',
          allowMultiple: false,
          options: [{ id: 'option-1-1', label: 'Blue' }, { id: 'option-1-2', label: 'Green' }],
        }],
      },
    })
  })

  it('refuses a question call with no questions', () => {
    expect(() => askUserQuestionToolCall(AgentProvider.CURSOR, 'cursor-q', [])).toThrow('at least one question')
  })

  it('builds native plan and web-fetch approval queries', () => {
    expect(cursorCreatePlanToolCall('plan-1', 'Review', 'Review the change.', '# Plan')).toEqual({
      id: 'plan-1',
      name: 'createPlan',
      arguments: { name: 'Review', overview: 'Review the change.', plan: '# Plan' },
    })
    expect(cursorWebFetchPermissionToolCall('fetch-1', 'https://example.invalid/probe')).toEqual({
      id: 'fetch-1',
      name: 'webFetch',
      arguments: { url: 'https://example.invalid/probe' },
    })
  })
})

describe('Copilot native tool calls', () => {
  it('builds the installed exit_plan_mode request', () => {
    expect(exitPlanModeToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-plan', 'Review the change.')).toEqual({
      id: 'copilot-plan',
      name: 'exit_plan_mode',
      arguments: {
        summary: 'Review the change.',
        actions: ['autopilot', 'interactive', 'exit_only'],
        recommendedAction: 'interactive',
      },
    })
  })

  it('builds the installed MCP tool request', () => {
    expect(mcpToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-form', { server: 'form_probe', tool: 'ask', input: {} })).toEqual({
      id: 'copilot-form',
      name: 'form_probe-ask',
      arguments: {},
    })
  })
})

/**
 * Every provider this project supports. Read off the proto enum rather than
 * written out, so a provider added there fails this file until the vocabulary
 * table answers for it -- the runtime half of the `satisfies` clause, which
 * only covers the keys the table already spells.
 */
const PROVIDERS = (Object.values(AgentProvider) as unknown[])
  .filter((value): value is AgentProvider => typeof value === 'number' && value !== AgentProvider.UNSPECIFIED)

/** Each status that the to-do sidebar draws. */
const ALL_TODO_STATUSES: readonly TodoStatus[] = ['pending', 'in_progress', 'completed', 'cancelled', 'deleted', 'abandoned', 'blocked']

/**
 * Each operation, paired with a call that exercises it. The pair is what lets
 * one table drive both directions of the `hasToolFor` contract below.
 */
const OPERATIONS = [
  { operation: 'bash', call: (p: AgentProvider) => bashToolCall(p, 'call-1', 'echo hi') },
  { operation: 'edit', call: (p: AgentProvider) => editToolCall(p, 'call-1', { path: '/tmp/a.txt', before: 'a', after: 'b' }) },
  { operation: 'write', call: (p: AgentProvider) => writeToolCall(p, 'call-1', { path: '/tmp/a.txt', content: 'x\ny' }) },
  { operation: 'read', call: (p: AgentProvider) => readToolCall(p, 'call-1', '/tmp/a.txt') },
  { operation: 'enterPlanMode', call: (p: AgentProvider) => enterPlanModeToolCall(p, 'call-1') },
  { operation: 'exitPlanMode', call: (p: AgentProvider) => exitPlanModeToolCall(p, 'call-1', 'The plan.') },
  { operation: 'exitPlanModeFromFile', call: (p: AgentProvider) => exitPlanModeFromFileToolCall(p, 'call-1', [{ label: 'A', description: 'The first' }, { label: 'B', description: 'The second' }]) },
  { operation: 'askUserQuestion', call: (p: AgentProvider) => askUserQuestionToolCall(p, 'call-1', [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }, { label: 'B', description: 'The second' }] }]) },
  { operation: 'spawnSubagent', call: (p: AgentProvider) => spawnSubagentToolCall(p, 'call-1', { description: 'Probe the subagent path', prompt: 'Reply with PONG.' }) },
  { operation: 'spawnSubagentBatch', call: (p: AgentProvider) => spawnSubagentBatchToolCall(p, 'call-1', [{ description: 'First child', prompt: 'Reply with ONE.' }, { description: 'Second child', prompt: 'Reply with TWO.' }]) },
  { operation: 'backgroundBash', call: (p: AgentProvider) => backgroundBashToolCall(p, 'call-1', 'sleep 60') },
  { operation: 'updateTodos', call: (p: AgentProvider) => updateTodosToolCall(p, 'call-1', [{ step: 'First', status: 'pending' }]) },
  { operation: 'createGoal', call: (p: AgentProvider) => createGoalToolCall(p, 'call-1', 'Ship the feature.') },
  { operation: 'completeGoal', call: (p: AgentProvider) => completeGoalToolCall(p, 'call-1') },
  { operation: 'blockGoal', call: (p: AgentProvider) => blockGoalToolCall(p, 'call-1', 'Blocked here.') },
  { operation: 'mcpTool', call: (p: AgentProvider) => mcpToolCall(p, 'call-1', { server: 'form_probe', tool: 'echo', input: { text: 'hi' } }) },
  { operation: 'codeExecution', call: (p: AgentProvider) => codeExecutionToolCall(p, 'call-1', 'return 40 + 2;') },
] as const

describe('TOOL_VOCABULARY', () => {
  it('answers for every provider the proto enum declares', () => {
    expect(PROVIDERS).toHaveLength(10)
    for (const provider of PROVIDERS)
      expect(() => hasToolFor(provider, 'bash'), `provider ${provider}`).not.toThrow()
  })

  it('rejects a provider outside the enum by name', () => {
    expect(() => bashToolCall(9999 as AgentProvider, 'call-1', 'echo hi'))
      .toThrow('No tool vocabulary for AgentProvider 9999')
  })

  it('offers a shell tool for every provider', () => {
    // Every provider has a native shell operation in the scripted vocabulary.
    for (const provider of PROVIDERS)
      expect(hasToolFor(provider, 'bash'), `provider ${provider}`).toBe(true)
  })
})

describe('hasToolFor', () => {
  // hasToolFor and the builder must agree about this table.
  // A missing builder must produce a clear error before a native request starts.
  for (const { operation, call } of OPERATIONS) {
    it(`agrees with the builder for ${operation}`, () => {
      for (const provider of PROVIDERS) {
        const offered = hasToolFor(provider, operation)
        if (offered) {
          expect(() => call(provider), `provider ${provider} offers ${operation}`).not.toThrow()
          continue
        }
        expect(() => call(provider), `provider ${provider} lacks ${operation}`)
          .toThrow(`AgentProvider ${provider} has no`)
      }
    })
  }
})

describe('backgroundBashToolCall', () => {
  it('sets the flag that detaches the command, which the foreground call omits', () => {
    // Claude's background shell is the SAME tool plus one flag, so the two
    // builders differ by exactly that key. A copy that lost it would open a
    // blocking row and the registry would never gain a shell entry.
    const background = backgroundBashToolCall(AgentProvider.CLAUDE_CODE, 'call-1', 'sleep 60')
    const foreground = bashToolCall(AgentProvider.CLAUDE_CODE, 'call-1', 'sleep 60')
    expect(background.name).toBe(foreground.name)
    expect(background.arguments).toMatchObject({ run_in_background: true })
    expect(foreground.arguments).not.toHaveProperty('run_in_background')
  })
})

describe('mimoTaskToolCall', () => {
  it('carries one operation of the to-do tool', () => {
    expect(mimoTaskToolCall('call-1', { action: 'create', summary: 'Write the parser' })).toEqual({
      id: 'call-1',
      name: 'task',
      arguments: { operation: { action: 'create', summary: 'Write the parser' } },
    })
    expect(mimoTaskToolCall('call-2', { action: 'done', id: 'T1' }).arguments).toEqual({ operation: { action: 'done', id: 'T1' } })
  })
})

describe('piTodoToolCall', () => {
  it('uses the native incremental create, update, and clear actions', () => {
    expect(piTodoToolCall('create', { action: 'create', subject: 'Inspect the repository' })).toEqual({
      id: 'create',
      name: 'todo',
      arguments: { action: 'create', subject: 'Inspect the repository' },
    })
    expect(piTodoToolCall('update', { action: 'update', id: 1, status: 'completed' })).toEqual({
      id: 'update',
      name: 'todo',
      arguments: { action: 'update', id: 1, status: 'completed' },
    })
    expect(piTodoToolCall('clear', { action: 'clear' })).toEqual({
      id: 'clear',
      name: 'todo',
      arguments: { action: 'clear' },
    })
  })
})

/**
 * The statuses that each bulk to-do builder accepts, from each native tool schema. Kiro's create command and Oh My Pi's
 * init command take no status at all, so their builders read none and refuse none.
 */
const TODO_STATUSES_BY_PROVIDER: ReadonlyMap<AgentProvider, readonly TodoStatus[]> = new Map([
  [AgentProvider.CURSOR, ['pending', 'in_progress', 'completed', 'cancelled']],
  [AgentProvider.OPENCODE, ['pending', 'in_progress', 'completed', 'cancelled']],
  [AgentProvider.KILO, ['pending', 'in_progress', 'completed', 'cancelled']],

])

describe('updateTodosToolCall statuses', () => {
  for (const provider of PROVIDERS.filter(p => hasToolFor(p, 'updateTodos'))) {
    const accepted = TODO_STATUSES_BY_PROVIDER.get(provider) ?? ['pending', 'in_progress', 'completed']
    it(`builds each status that the native tool of provider ${provider} accepts, and refuses every other one`, () => {
      for (const status of ALL_TODO_STATUSES) {
        const build = () => updateTodosToolCall(provider, 'call-1', [{ step: 'One', status }])
        if (accepted.includes(status))
          expect(build, status).not.toThrow()
        else
          expect(build, status).toThrow(`accepts no status ${status}`)
      }
    })
  }
})

describe('cursorMergeTodosToolCall', () => {
  it('merges the items by their IDs into the list, with Cursor\'s own status words', () => {
    expect(cursorMergeTodosToolCall('merge-1', [{ id: '2', content: 'Second', status: 'completed' }, { id: '9', content: 'New', status: 'cancelled' }])).toEqual({
      id: 'merge-1',
      name: 'updateTodos',
      arguments: { todos: [{ id: '2', content: 'Second', status: 'TODO_STATUS_COMPLETED' }, { id: '9', content: 'New', status: 'TODO_STATUS_CANCELLED' }], merge: true },
    })
  })

  it('refuses an empty merge, an item with no ID, and a status that Cursor lacks', () => {
    expect(() => cursorMergeTodosToolCall('merge-1', [])).toThrow('at least one item')
    expect(() => cursorMergeTodosToolCall('merge-1', [{ id: '', content: 'x', status: 'pending' }])).toThrow('nonempty ID')
    expect(() => cursorMergeTodosToolCall('merge-1', [{ id: '1', content: 'x', status: 'blocked' }])).toThrow('accepts no status blocked')
  })
})

// Kimi Code's tool schemas set `additionalProperties: false`, so an extra field
// fails the call and a renamed one fails it too. These cases pin the shapes
// that the schemas in its model requests state.

// These providers state a spawn's foreground or background choice on the native
// wire. An omitted flag lets a CLI release choose which path the test drives.

describe('Cursor GenerateImage tool call', () => {
  it('keeps the native image path and bytes in its scripted result', () => {
    expect(cursorGenerateImageToolCall('image-1', 'A teal square', '/work/square.png', 'iVBORw0KGgo')).toEqual({
      id: 'image-1',
      name: 'generateImage',
      arguments: { description: 'A teal square', filePath: '/work/square.png', imageData: 'iVBORw0KGgo' },
    })
  })
})

// omp 18.2.11's own tool schemas. Only the specs 133 to 139 script these shapes, and
// CI runs no E2E, so these cases are the check that runs on every change.

// The Amp specs alone script these shapes, and CI runs no E2E, so these cases are
// the check that runs on every change.

/**
 * The patch text of an apply_patch call: Amp states it as an argument, and Codex
 * states it as the JSON string literal that its `exec` source passes.
 * A patch marks each hunk line. An unmarked line is not part of the hunk,
 * so apply_patch refuses the patch or applies another change.
 */

// The MiMo Code specs alone script these shapes. Every input field is snake_case.

describe('mimoWorkflowToolCall', () => {
  it('runs the script through the workflow tool', () => {
    expect(mimoWorkflowToolCall('c', 'await agent("Go.")')).toEqual({ id: 'c', name: 'workflow', arguments: { operation: 'run', script: 'await agent("Go.")' } })
  })
})

describe('the ZCode background command', () => {
  it('backgrounds the command through the run_in_background flag of the same Bash tool', () => {
    expect(backgroundBashToolCall(AgentProvider.ZCODE, 'call-1', 'sleep 60')).toEqual({ id: 'call-1', name: 'Bash', arguments: { command: 'sleep 60', description: 'Run the scripted command in the background', run_in_background: true } })
    expect(bashToolCall(AgentProvider.ZCODE, 'call-1', 'sleep 60').arguments).not.toHaveProperty('run_in_background')
  })
})
