import type { MockModelToolCall } from './mockModelScript'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
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
  clineRunTeammateTaskToolCall,
  clineSpawnTeammateToolCall,
  codebuddyFindToolsToolCall,
  codebuddyFindWorkflowToolCall,
  codebuddyReplToolCall,
  codebuddyTaskCreateToolCall,
  codebuddyTaskUpdateToolCall,
  codebuddyWaitForMcpServersToolCall,
  codebuddyWorkflowToolCall,
  codeExecutionToolCall,
  codewhaleReadMediaToolCall,
  codewhaleToolSearchToolCall,
  codewhaleWorkflowToolCall,
  codexEscalatedCommandToolCall,
  codexExecToolCall,
  codexWaitAgentToolCall,
  completeGoalToolCall,
  copilotApplyPatchToolCall,
  createGoalToolCall,
  cursorCreatePlanToolCall,
  cursorGenerateImageToolCall,
  cursorWebFetchPermissionToolCall,
  deepseekHarnessEscalatedBashToolCall,
  deepseekHarnessReadImageToolCall,
  deepseekHarnessRunCodeToolCall,
  diracCondenseToolCall,
  diracEditAnchorCapture,
  diracRespondToolCall,
  droidExecuteToolCall,
  droidScriptToolCall,
  droidToolSearchToolCall,
  droidWaitForScriptToolCall,
  editToolCall,
  enterPlanModeToolCall,
  exitPlanModeFromFileToolCall,
  exitPlanModeToolCall,
  fastAgentHumanInputToolCall,
  geminiCompleteTaskToolCall,
  geminiTodoSnapshotToolCall,
  goosePermissionJudgmentToolCall,
  gooseReadImageToolCall,
  grokWorkflowToolCall,
  hasToolFor,
  junieSubagentSubmitToolCall,
  junieSubmitPlanToolCall,
  kimiAgentSwarmToolCall,
  kimiReadMediaFileToolCall,
  kiroCompleteTodosToolCall,
  kiroSwitchToExecutionToolCall,
  kiroToolSearchToolCall,
  lettaMcpCatalogArguments,
  lettaMcpCatalogToolCall,
  lettaMcpCliToolCall,
  lettaTaskCreateToolCall,
  lettaTaskListToolCall,
  lettaTaskUpdateToolCall,
  lettaViewImageToolCall,
  mcpToolCall,
  mimoInteractiveBashToolCall,
  mimoTaskToolCall,
  mimoWorkflowToolCall,
  ohMyPiYieldToolCall,
  piCodemodeToolCall,
  piEditorProbeToolCall,
  piMcpResourceToolCall,
  piTodoToolCall,
  piWorkflowToolCall,
  qoderWorkflowToolCall,
  qwenWorkflowToolCall,
  readToolCall,
  reasonixInspectCapabilityToolCall,
  reasonixListCapabilitiesToolCall,
  reasonixViewImageToolCall,
  spawnSubagentToolCall,
  updateTodosToolCall,
  writeToolCall,
  zcodeCreateWorkflowToolCall,
  zcodeGetWorkflowRunToolCall,
  zcodeNodeImageToolCall,
  zcodeReadRangeToolCall,
  zcodeWorkflowSkillToolCall,
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

describe('native added-file patch encoding', () => {
  it.each([AgentProvider.CODEX, AgentProvider.AMP])('does not add a blank line for a final LF in provider %s', async (provider) => {
    const call = writeToolCall(provider, 'native-add', { path: 'exact.txt', content: 'computed42\n' })
    let actualPatch: unknown
    if (provider === AgentProvider.CODEX) {
      await runInNewContext(`(async () => { ${call.input} })()`, {
        tools: { apply_patch: async (patch: unknown) => {
          actualPatch = patch
          return {}
        } },
        text: () => {},
      })
    }
    else {
      actualPatch = call.arguments?.patchText
    }
    expect(actualPatch).toBe('*** Begin Patch\n*** Add File: exact.txt\n+computed42\n*** End Patch')
  })
  it.each([
    { content: '', body: '' },
    { content: '\n', body: '+\n' },
    { content: 'a\n\n', body: '+a\n+\n' },
    { content: ' a \nb\n', body: '+ a \n+b\n' },
    { content: 'a\nb', body: '+a\n+b\n' },
  ])('preserves every intentional added line for %j', ({ content, body }) => {
    const call = writeToolCall(AgentProvider.CODEX, 'native-add', { path: 'exact.txt', content })
    expect(patchTextOf(call)).toBe(`*** Begin Patch\n*** Add File: exact.txt\n${body}*** End Patch`)
  })
})

describe('native Fast Agent label schema', () => {
  it.each(['', 'a'.repeat(33), 'invalid label!', '_first', 'last_', '한글'])('rejects a label that the native subagent refuses: %j', (description) => {
    expect(() => spawnSubagentToolCall(AgentProvider.FAST_AGENT, 'native-child', { description, prompt: 'Actual child task.' })).toThrow()
  })
  it.each(['a', 'a'.repeat(32), 'native-child_1', ' native child '])('preserves valid native label bytes: %j', (description) => {
    expect(spawnSubagentToolCall(AgentProvider.FAST_AGENT, 'native-child', { description, prompt: 'Actual child task.' }).arguments)
      .toEqual({ message: 'Actual child task.', label: description })
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
    expect(() => zcodeReadRangeToolCall('native-fresh-read', '/private/native file.txt', range)).toThrow()
  })
})

describe('lettaMcpCatalogArguments', () => {
  it('uses the exact native registered catalog operation and agent identity', () => {
    expect(lettaMcpCatalogArguments('agent-native', 'echo_probe')).toEqual(['mcp', 'tools', 'echo_probe', '--full', '--agent', 'agent-native'])
  })
  it.each(['', ' ', 'agent native', 'agent\n', '$(touch unexpected)', '한글'])('rejects an invalid native agent ID: %j', (agentId) => {
    expect(() => lettaMcpCatalogArguments(agentId, 'echo_probe')).toThrow('valid agent and server IDs')
  })
  it.each(['', ' ', 'echo probe', 'echo\n', 'echo;touch unexpected', '한글'])('rejects an invalid native server ID: %j', (server) => {
    expect(() => lettaMcpCatalogArguments('agent-native', server)).toThrow('valid agent and server IDs')
  })
  it('preserves underscores and long valid native identifiers', () => {
    const agent = `agent_${'a'.repeat(50_000)}`
    const server = `server_${'_'.repeat(50_000)}`
    expect(lettaMcpCatalogArguments(agent, server)).toEqual(['mcp', 'tools', server, '--full', '--agent', agent])
  })
})

describe('lettaMcpCatalogToolCall', () => {
  it('passes exact quoted capture and native argv through the actual Bash command', () => {
    const scratch = resolve(process.cwd(), '../.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'letta-catalog-argv-'))
    try {
      const recorder = join(directory, 'capture "한글" $(touch forbidden).cjs')
      writeFileSync(recorder, 'process.stdout.write(JSON.stringify(process.argv.slice(2)))')
      const executable = join(directory, 'native "한글";`echo forbidden`')
      const options = { executable, nodeExecutable: process.execPath, captureScriptPath: recorder, receiptId: 'actual-receipt', agentId: 'agent-native', server: 'echo_probe' }
      const call = lettaMcpCatalogToolCall('catalog-native', options)
      expect(call.name).toBe('Bash')
      expect(call.id).toBe('catalog-native')
      const command = call.arguments?.command
      if (typeof command !== 'string')
        throw new Error('The native Letta catalog call contains no shell command.')
      const args = JSON.parse(execFileSync('/bin/sh', ['-c', command], { cwd: directory, encoding: 'utf8' }))
      expect(args).toEqual(['actual-receipt', 'catalog-native', executable, 'mcp', 'tools', 'echo_probe', '--full', '--agent', 'agent-native'])
      expect(readdirSync(directory)).toEqual(['capture "한글" $(touch forbidden).cjs'])
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })
  it.each([
    { executable: 'letta' },
    { nodeExecutable: 'node' },
    { captureScriptPath: 'capture.cjs' },
    { receiptId: '' },
    { receiptId: 'receipt:invalid' },
  ])('rejects an invalid capture identity or relative native path: %j', (overrides) => {
    const options = { executable: resolve('letta'), nodeExecutable: process.execPath, captureScriptPath: resolve('capture.cjs'), receiptId: 'receipt-native', agentId: 'agent-native', server: 'echo_probe', ...overrides }
    expect(() => lettaMcpCatalogToolCall('catalog-native', options)).toThrow('valid identities and absolute executable paths')
  })
  it.each(['', 'catalog:invalid', 'catalog\n', '$(touch unexpected)'])('rejects an invalid native call ID: %j', (callId) => {
    expect(() => lettaMcpCatalogToolCall(callId, { executable: resolve('letta'), nodeExecutable: process.execPath, captureScriptPath: resolve('capture.cjs'), receiptId: 'receipt-native', agentId: 'agent-native', server: 'echo_probe' })).toThrow('valid identities and absolute executable paths')
  })
})

describe('lettaMcpCliToolCall', () => {
  it.each([
    { label: 'empty', input: { value: '' } },
    { label: 'zero', input: { value: 0 } },
    { label: 'false', input: { value: false } },
    { label: 'Unicode', input: { value: '한글 "quoted" \\ path' } },
    { label: 'shell substitution', input: { value: '$(touch BAD_SHELL_EXPANSION) `touch BAD_BACKTICK_EXPANSION` \' " 한글\\' } },
  ])('passes the $label payload as unchanged native arguments', ({ input }) => {
    const scratch = resolve(process.cwd(), '../.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'letta-mcp-argv-'))
    try {
      const recorder = join(directory, 'arguments.cjs')
      writeFileSync(recorder, 'process.stdout.write(JSON.stringify(process.argv.slice(2)))')
      const call = lettaMcpCliToolCall('native-mcp', 'agent-native', 'mcp__echo_probe__echo', input)
      const command = call.arguments?.command
      if (typeof command !== 'string')
        throw new Error('The native Letta MCP builder returned no Bash command.')
      const script = `letta() { node ${quotePosixShellArgument(recorder)} "$@"; }\n${command}`
      const output = execFileSync('sh', ['-c', script], { cwd: directory, encoding: 'utf8' })
      expect(JSON.parse(output)).toEqual(['mcp', 'call', 'mcp__echo_probe__echo', '--agent', 'agent-native', '--args', JSON.stringify(input)])
      expect(readdirSync(directory)).toEqual(['arguments.cjs'])
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it.each(['', ' ', 'agent native', 'agent-native ', '$(unsafe)'])('rejects an invalid native agent ID: %j', (agentId) => {
    expect(() => lettaMcpCliToolCall('native-mcp', agentId, 'mcp__echo_probe__echo', {})).toThrow('valid agent ID')
  })
  it.each(['', 'echo', 'mcp__server', 'mcp__server__tool;unsafe', 'mcp__a__', 'mcp____tool', 'mcp_____'])('rejects an invalid native MCP tool ID: %j', (toolId) => {
    expect(() => lettaMcpCliToolCall('native-mcp', 'agent-native', toolId, {})).toThrow('server and tool')
  })

  it.each(['a', '_', '__', 'a_b', 'a__b', '-'])('preserves underscores and separators inside the native server and tool: %j', (part) => {
    for (const other of ['a', '_', '__', 'a_b', 'a__b', '-']) {
      expect(() => lettaMcpCliToolCall('native-mcp', 'agent-native', `mcp__${part}__${other}`, {})).not.toThrow()
    }
  })

  it('accepts a long valid native identifier without ambiguous matching', () => {
    const toolId = `mcp__${'_'.repeat(50_000)}`
    const call = lettaMcpCliToolCall('native-mcp', `agent-${'a'.repeat(50_000)}`, toolId, {})
    expect(call.arguments?.command).toContain(toolId)
  })

  it('rejects a long native identifier with an invalid final character', () => {
    expect(() => lettaMcpCliToolCall('native-mcp', 'agent-native', `mcp__${'_'.repeat(50_000)}!`, {})).toThrow('server and tool')
  })
})

describe('goosePermissionJudgmentToolCall', () => {
  it.each([{ ids: [] }, { ids: ['actual-read'] }, { ids: ['실제 요청', 'quoted"request', ''] }])('preserves the native read-only IDs without sharing the input array: $ids', ({ ids }) => {
    const result = goosePermissionJudgmentToolCall('native-judge', ids)
    expect(result).toEqual({ id: 'native-judge', name: 'platform__tool_by_tool_permission', arguments: { read_only_request_ids: ids } })
    expect(result.arguments?.read_only_request_ids).not.toBe(ids)
  })
})

describe('codebuddyFindToolsToolCall', () => {
  it('preserves exact deferred names without sharing the caller array', () => {
    const names = ['REPL', 'Workflow']
    const call = codebuddyFindToolsToolCall('native-search', names)
    expect(call).toEqual({ id: 'native-search', name: 'ToolSearch', arguments: { tool_names: names } })
    expect(call.arguments?.tool_names).not.toBe(names)
  })

  it.each([{ names: [] }, { names: [''] }])('refuses missing deferred tool names: %j', ({ names }) => {
    expect(() => codebuddyFindToolsToolCall('native-search', names)).toThrow('exact tool names')
  })
})

describe('kiroToolSearchToolCall', () => {
  it('preserves an empty native query and every optional field', () => {
    expect(kiroToolSearchToolCall('native-search')).toEqual({ id: 'native-search', name: 'tool_search', arguments: {} })
    const options = { toolId: 'server_한글::tool-name', query: '', maxResults: 100 }
    expect(kiroToolSearchToolCall('native-search', options)).toEqual({
      id: 'native-search',
      name: 'tool_search',
      arguments: { tool_id: options.toolId, query: '', max_results: 100 },
    })
    expect(options).toEqual({ toolId: 'server_한글::tool-name', query: '', maxResults: 100 })
  })

  it.each(['', 'tool', '::tool', 'server::', 'server::tool::extra', 'server name::tool'])('rejects an incomplete native tool identity: %s', (toolId) => {
    expect(() => kiroToolSearchToolCall('native-search', { toolId })).toThrow('exact server and tool identity')
  })

  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('rejects an invalid native result maximum: %s', (maxResults) => {
    expect(() => kiroToolSearchToolCall('native-search', { maxResults })).toThrow('positive safe result maximum')
  })
})

describe('droidToolSearchToolCall', () => {
  it('keeps exact selection text and the native call ID with an optional maximum', () => {
    expect(droidToolSearchToolCall('search', 'select:Read,Execute', 100)).toEqual({
      id: 'call_search',
      name: 'ToolSearch',
      arguments: { query: 'select:Read,Execute', max_results: 100 },
    })
    expect(droidToolSearchToolCall('call_search', 'select:Read').arguments).toEqual({ query: 'select:Read' })
  })

  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('rejects an invalid native result maximum: %s', (maxResults) => {
    expect(() => droidToolSearchToolCall('search', 'execute', maxResults)).toThrow('positive safe result maximum')
  })
})

describe('droidScriptToolCall', () => {
  it('keeps raw source and a zero observation period with the exact native call ID', () => {
    const script = '\nreturn "출력" + (40 + 2);\n'
    expect(droidScriptToolCall('native-script', script, 0)).toEqual({ id: 'call_native-script', name: 'Script', arguments: { script, waitForMs: 0 } })
    expect(droidScriptToolCall('call_native-script', script).arguments).toEqual({ script })
  })

  it('accepts the exact UTF-8 size limit and rejects a multibyte overflow', () => {
    const script = 'x'.repeat(512 * 1024)
    expect(droidScriptToolCall('limit', script).arguments?.script).toBe(script)
    expect(() => droidScriptToolCall('limit', `${script}한`)).toThrow('512 KiB')
  })

  it.each(['', ' ', '\n'])('rejects empty source: %j', (source) => {
    expect(() => droidScriptToolCall('script', source)).toThrow('nonempty source')
  })

  it.each([-1, Number.NaN, Number.POSITIVE_INFINITY])('rejects an invalid observation period: %s', (waitForMs) => {
    expect(() => droidScriptToolCall('script', 'return 0;', waitForMs)).toThrow('nonnegative milliseconds')
  })
})

describe('droidWaitForScriptToolCall', () => {
  it('keeps a zero timeout and native script identity and supports explicit kill', () => {
    expect(droidWaitForScriptToolCall('wait', { toolCallId: 'script', timeoutMs: 0 })).toEqual({ id: 'call_wait', name: 'WaitForScript', arguments: { toolCallId: 'call_script', timeoutMs: 0 } })
    expect(droidWaitForScriptToolCall('wait', { toolCallId: 'call_script', kill: true }).arguments).toEqual({ toolCallId: 'call_script', kill: true })
  })

  it('rejects an absent script identity and incompatible control fields', () => {
    expect(() => droidWaitForScriptToolCall('wait', { toolCallId: '' })).toThrow('exact tool call ID')
    expect(() => droidWaitForScriptToolCall('wait', { toolCallId: 'script', kill: true, timeoutMs: 0 })).toThrow('combine kill and timeout')
  })

  it.each([-1, Number.NaN, Number.POSITIVE_INFINITY])('rejects an invalid timeout: %s', (timeoutMs) => {
    expect(() => droidWaitForScriptToolCall('wait', { toolCallId: 'script', timeoutMs })).toThrow('nonnegative milliseconds')
  })
})

describe('codebuddyReplToolCall', () => {
  it('keeps the direct native code source without a deferred wrapper', () => {
    expect(codebuddyReplToolCall('native-repl', '\nreturn 40 + 2;\n')).toEqual({ id: 'native-repl', name: 'REPL', arguments: { code: '\nreturn 40 + 2;\n' } })
  })
})

describe('codeExecutionToolCall', () => {
  it.each([
    { provider: AgentProvider.CODEX, name: 'exec', field: 'input' },
    { provider: AgentProvider.PI, name: 'codemode', field: 'code' },
    { provider: AgentProvider.CODEWHALE, name: 'execute_tools', field: 'code' },
    { provider: AgentProvider.CODEBUDDY, name: 'DeferExecuteTool', field: 'deferred' },
    { provider: AgentProvider.MIMO_CODE, name: 'exec', field: 'code' },
    { provider: AgentProvider.OH_MY_PI, name: 'eval', field: 'code' },
    { provider: AgentProvider.ZCODE, name: 'mcp__node_repl__js', field: 'code' },
    { provider: AgentProvider.QWEN_CODE, name: 'exec', field: 'source' },
    { provider: AgentProvider.GOOSE, name: 'execute_typescript', field: 'code' },
    { provider: AgentProvider.OPENCODE, name: 'execute', field: 'code' },
    { provider: AgentProvider.KILO, name: 'execute', field: 'code' },
    { provider: AgentProvider.DROID, name: 'Script', field: 'script' },
    { provider: AgentProvider.DIRAC, name: 'execute_command', field: 'script' },
  ])('preserves native source text for $provider', ({ provider, name, field }) => {
    const source = 'const value = "출력";\nreturn value;'
    const call = codeExecutionToolCall(provider, 'native-code', source)
    expect(call.id).toBe(provider === AgentProvider.DROID ? 'call_native-code' : 'native-code')
    expect(call.name).toBe(name)
    if (field === 'deferred')
      expect(call.arguments).toEqual({ toolName: 'REPL', params: { code: source } })
    else
      expect(field === 'input' ? call.input : call.arguments?.[field]).toBe(source)
  })

  it('refuses a provider without an audited native executor', () => {
    expect(() => codeExecutionToolCall(AgentProvider.CLAUDE_CODE, 'native-code', '')).toThrow('no audited native code executor')
  })

  it('selects the native Node script path without constructing a shell command', () => {
    expect(codeExecutionToolCall(AgentProvider.DIRAC, 'native-script', 'console.log(40 + 2);')).toEqual({
      id: 'native-script',
      name: 'execute_command',
      arguments: { script: 'console.log(40 + 2);', language: 'node' },
    })
  })
})

describe('native Droid Execute arguments', () => {
  it('supplies all required current native command fields with conservative risk', () => {
    const call = bashToolCall(AgentProvider.DROID, 'required-command', 'printf "native output"')
    expect(call.arguments).toEqual({ command: 'printf "native output"', summary: 'Run the scripted command', riskLevel: 'medium' })
  })

  it.each(['low', 'medium', 'high'] as const)('retains an explicitly selected native risk %s', (riskLevel) => {
    const command = '\n  printf "출력"\n'
    expect(droidExecuteToolCall('explicit', { command, summary: 'Print the native output', riskLevel }))
      .toEqual({ id: 'call_explicit', name: 'Execute', arguments: { command, summary: 'Print the native output', riskLevel } })
  })

  it.each([
    { id: '', command: 'echo value', summary: 'Print a value' },
    { id: 'call', command: ' ', summary: 'Print a value' },
    { id: 'call', command: 'echo value', summary: '' },
  ])('rejects a missing required native field %j', ({ id, command, summary }) => {
    expect(() => droidExecuteToolCall(id, { command, summary, riskLevel: 'low' })).toThrow(id === '' ? 'nonempty ID' : 'requires an ID')
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

describe('codewhaleToolSearchToolCall', () => {
  it.each(['image_probe show', '', 'a"quote\n$literal'])('preserves the native discovery query: %s', (query) => {
    expect(codewhaleToolSearchToolCall('native-discovery', query)).toEqual({ id: 'native-discovery', name: 'tool_search', arguments: { query } })
  })
})

describe('codewhaleReadMediaToolCall', () => {
  it.each(['/work/shot.png', '', '/work/a space/quote".png'])('preserves the native media path: %s', (path) => {
    expect(codewhaleReadMediaToolCall('native-media', path)).toEqual({ id: 'native-media', name: 'read_media', arguments: { path } })
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

describe('image-read tool calls', () => {
  it('uses Kimi Code ReadMediaFile for an image path', () => {
    expect(kimiReadMediaFileToolCall('kimi-image', '/work/shot.png')).toEqual({
      id: 'kimi-image',
      name: 'ReadMediaFile',
      arguments: { path: '/work/shot.png' },
    })
  })

  it('uses Goose and Reasonix native image tool schemas', () => {
    expect(gooseReadImageToolCall('goose-image', '/work/shot.png')).toEqual({
      id: 'goose-image',
      name: 'read_image',
      arguments: { source: '/work/shot.png' },
    })
    expect(reasonixViewImageToolCall('reasonix-image', '/work/shot.png')).toEqual({
      id: 'reasonix-image',
      name: 'view_image',
      arguments: { path: '/work/shot.png' },
    })
  })

  it('uses the installed ZCode Node schema and escapes image labels', () => {
    const label = 'image "); nodeRepl.write("forged'
    expect(zcodeNodeImageToolCall('zcode-image', 'AQID', label)).toEqual({
      id: 'zcode-image',
      name: 'mcp__node_repl__js',
      arguments: {
        code: `nodeRepl.write(${JSON.stringify(label)}); await nodeRepl.emitImage({ base64: "AQID", mimeType: 'image/png' })`,
        title: `Show ${label}`,
      },
    })
  })

  it('loads ZCode dynamic workflows before a named inline run', () => {
    expect(zcodeWorkflowSkillToolCall('load-skill')).toEqual({
      id: 'load-skill',
      name: 'Skill',
      arguments: { skill: 'dynamic-workflows' },
    })
    expect(zcodeCreateWorkflowToolCall('create-run', 'e2e-probe', 'return { conclusion: "done" }')).toEqual({
      id: 'create-run',
      name: 'CreateWorkflow',
      arguments: { name: 'e2e-probe', script: 'return { conclusion: "done" }' },
    })
  })

  it('reads the same native ZCode workflow with its exact run identity', () => {
    expect(zcodeGetWorkflowRunToolCall('read-native-run', 'workflow-run-42')).toEqual({ id: 'read-native-run', name: 'GetWorkflowRun', arguments: { run_id: 'workflow-run-42' } })
  })

  it.each(['', '   ', '\0', undefined, null, 0, false])('rejects an absent or malformed native workflow identity: %j', (value) => {
    expect(() => Reflect.apply(zcodeGetWorkflowRunToolCall, undefined, [value, 'workflow-run-42'])).toThrow('call ID and run ID')
    expect(() => Reflect.apply(zcodeGetWorkflowRunToolCall, undefined, ['read-native-run', value])).toThrow('call ID and run ID')
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

describe('Amp MCP tool calls', () => {
  it('uses the installed MCP tool name and input', () => {
    expect(mcpToolCall(AgentProvider.AMP, 'amp-echo', { server: 'echo_probe', tool: 'echo', input: { value: 'amp' } })).toEqual({
      id: 'amp-echo',
      name: 'mcp__echo_probe__echo',
      arguments: { value: 'amp' },
    })
  })
})

describe('kimiAgentSwarmToolCall', () => {
  it('uses the native swarm schema for item prompts', () => {
    expect(kimiAgentSwarmToolCall('swarm', 'Review modules', 'Review {{item}}.', ['module-a'])).toEqual({
      id: 'swarm',
      name: 'AgentSwarm',
      arguments: {
        description: 'Review modules',
        subagent_type: 'coder',
        prompt_template: 'Review {{item}}.',
        items: ['module-a'],
      },
    })
  })
})

describe('qwenWorkflowToolCall', () => {
  it('uses the native inline workflow schema', () => {
    expect(qwenWorkflowToolCall('run', 'return 1')).toEqual({
      id: 'run',
      name: 'workflow',
      arguments: { script: 'return 1' },
    })
  })
})

describe('codewhaleWorkflowToolCall', () => {
  it('uses a structured read-only plan with one child', () => {
    expect(codewhaleWorkflowToolCall('run', 'Probe the workflow', 'Reply with PONG.')).toEqual({
      id: 'run',
      name: 'workflow',
      arguments: {
        action: 'run',
        plan: {
          goal: 'Probe the workflow',
          risk: 'read_only',
          phases: [],
          children: [{ label: 'Probe child', prompt: 'Reply with PONG.', type: 'explore', file_scope: [] }],
          gates: [],
        },
      },
    })
  })

  it('preserves two distinct native read-only assignments', () => {
    const children = [{ label: 'First child', prompt: 'Read the first item.' }, { label: 'Second child', prompt: 'Read the second item.' }]
    const call = codewhaleWorkflowToolCall('run-two', 'Read both items.', 'Unused default.', children)
    expect(call.arguments).toHaveProperty('plan.children', children.map(child => ({ ...child, type: 'explore', file_scope: [] })))
    expect(call.arguments).toHaveProperty('plan.risk', 'read_only')
    expect(children).toEqual([{ label: 'First child', prompt: 'Read the first item.' }, { label: 'Second child', prompt: 'Read the second item.' }])
  })

  it('rejects empty native children', () => {
    expect(() => codewhaleWorkflowToolCall('run', 'Read.', 'Unused.', [])).toThrow('nonempty child assignment')
  })

  it.each([null, false, {}, 'child'])('rejects a malformed native children collection %j', (children) => {
    expect(() => Reflect.apply(codewhaleWorkflowToolCall, undefined, ['run', 'Read.', 'Unused.', children])).toThrow('children must be an array')
  })

  it.each([
    { children: [null] },
    { children: ['child'] },
    { children: [{ label: 7, prompt: 'Read.' }] },
    { children: [{ label: 'Child', prompt: 7 }] },
  ])('rejects a malformed native child $children', ({ children }) => {
    expect(() => Reflect.apply(codewhaleWorkflowToolCall, undefined, ['run', 'Read.', 'Unused.', children])).toThrow('nonempty child assignment')
  })

  it.each(['', ' ', '\n\t'])('rejects an empty native assignment %j', (prompt) => {
    expect(() => codewhaleWorkflowToolCall('run', 'Read.', prompt)).toThrow('nonempty child assignment')
    expect(() => codewhaleWorkflowToolCall('run', 'Read.', 'Unused.', [{ label: 'Child', prompt }])).toThrow('nonempty child assignment')
  })
})

describe('grokWorkflowToolCall', () => {
  it('uses a tagged Rhai script source', () => {
    expect(grokWorkflowToolCall('run', 'let meta = #{};')).toEqual({
      id: 'run',
      name: 'workflow',
      arguments: { source: { type: 'script', script: 'let meta = #{};' } },
    })
  })
})

describe('Cline teammate tool calls', () => {
  it('spawns and runs one teammate with the native schemas', () => {
    expect(clineSpawnTeammateToolCall('spawn', 'reviewer', 'Review the file.')).toEqual({
      id: 'spawn',
      name: 'team_spawn_teammate',
      arguments: { agentId: 'reviewer', rolePrompt: 'Review the file.' },
    })
    expect(clineRunTeammateTaskToolCall('run', 'reviewer', 'Find the issue.')).toEqual({
      id: 'run',
      name: 'team_run_task',
      arguments: { agentId: 'reviewer', task: 'Find the issue.', runMode: 'async' },
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
  { operation: 'backgroundBash', call: (p: AgentProvider) => backgroundBashToolCall(p, 'call-1', 'sleep 60') },
  { operation: 'updateTodos', call: (p: AgentProvider) => updateTodosToolCall(p, 'call-1', [{ step: 'First', status: 'pending' }]) },
  { operation: 'createGoal', call: (p: AgentProvider) => createGoalToolCall(p, 'call-1', 'Ship the feature.') },
  { operation: 'completeGoal', call: (p: AgentProvider) => completeGoalToolCall(p, 'call-1') },
  { operation: 'blockGoal', call: (p: AgentProvider) => blockGoalToolCall(p, 'call-1', 'Blocked here.') },
  { operation: 'mcpTool', call: (p: AgentProvider) => mcpToolCall(p, 'call-1', { server: 'form_probe', tool: 'echo', input: { text: 'hi' } }) },
] as const

describe('TOOL_VOCABULARY', () => {
  it('answers for every provider the proto enum declares', () => {
    expect(PROVIDERS).toHaveLength(29)
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

describe('bashToolCall', () => {
  it('carries the native id and a nonempty tool name for every provider', () => {
    for (const provider of PROVIDERS.filter(p => hasToolFor(p, 'bash'))) {
      const call = bashToolCall(provider, 'call-42', 'echo hi')
      expect(call.id, `provider ${provider}`).toBe(provider === AgentProvider.DROID ? 'call_call-42' : 'call-42')
      expect(call.name.length, `provider ${provider}`).toBeGreaterThan(0)
      // Exactly one payload form. A call with neither says nothing, and a call
      // with both leaves the protocol to pick, which differs per protocol.
      expect(call.arguments === undefined, `provider ${provider}`).toBe(call.input !== undefined)
    }
  })

  it('puts the command in the arguments where the provider takes JSON', () => {
    expect(bashToolCall(AgentProvider.CLAUDE_CODE, 'call-1', 'echo hi').arguments)
      .toMatchObject({ command: 'echo hi' })
  })

  it('puts JavaScript source in the input for Codex, not JSON arguments', () => {
    // Codex's `exec` is an OpenAI CUSTOM tool, so its payload is source text.
    // A builder that returned `arguments` would reach the CLI as an empty call.
    const call = bashToolCall(AgentProvider.CODEX, 'call-1', 'echo hi')
    expect(call.name).toBe('exec')
    expect(call.arguments).toBeUndefined()
    expect(call.input).toContain('exec_command')
    expect(call.input).toContain(JSON.stringify('echo hi'))
  })
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

describe('backgroundBashToolCall for Codewhale', () => {
  it('starts the job through task_shell_start, which is the one tool that takes it', () => {
    // `bash` refuses a `background` argument, so the flag form Claude uses would
    // fail the call and open no shell row.
    expect(backgroundBashToolCall(AgentProvider.CODEWHALE, 'call-1', 'sleep 60')).toEqual({ id: 'call-1', name: 'task_shell_start', arguments: { command: 'sleep 60' } })
  })
})

describe('spawnSubagentToolCall', () => {
  // `run` blocks until the child reports, so the parent's next scripted step reads
  // the report rather than a later notification turn.
  it('runs a MiMo Code subagent and waits for its report', () => {
    const call = spawnSubagentToolCall(AgentProvider.MIMO_CODE, 'call-1', { description: 'Probe it', prompt: 'Go.' })
    expect(call).toEqual({
      id: 'call-1',
      name: 'actor',
      arguments: { operation: { action: 'run', subagent_type: 'general', description: 'Probe it', prompt: 'Go.' } },
    })
  })

  it('names the collaboration namespace for Codex', () => {
    // A call that omits it comes back as `unsupported call: spawn_agent` in the
    // tool OUTPUT, so the turn continues and the registry simply stays empty.
    const call = spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', { description: 'Probe it', prompt: 'Go.' })
    expect(call.name).toBe('spawn_agent')
    expect(call.namespace).toBe('collaboration')
  })

  it('reduces a description to an identifier for the three tools that take a name', () => {
    const request = { description: 'Probe the Subagent Path!', prompt: 'Go.' }
    expect(spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', request).arguments)
      .toMatchObject({ task_name: 'probe_the_subagent_path' })
    // Copilot takes the identifier AND the description, which its schema
    // requires separately.
    expect(spawnSubagentToolCall(AgentProvider.GITHUB_COPILOT, 'call-1', request).arguments)
      .toMatchObject({ name: 'probe_the_subagent_path', description: 'Probe the Subagent Path!' })
    expect(spawnSubagentToolCall(AgentProvider.CODEWHALE, 'call-1', request).arguments)
      .toMatchObject({ name: 'probe_the_subagent_path' })
  })

  it('starts a Codewhale child through the one agent tool', () => {
    // `agent` holds every subagent action. A call without `action: start` asks
    // for a roster or a status, and no child runs.
    const call = spawnSubagentToolCall(AgentProvider.CODEWHALE, 'call-1', { description: 'Probe it', prompt: 'Go.' })
    expect(call.name).toBe('agent')
    expect(call.arguments).toEqual({ action: 'start', name: 'probe_it', type: 'explore', prompt: 'Go.', detached: false })
  })

  it('falls back to a usable name when the description reduces to nothing', () => {
    // Empty and punctuation-only both strip to an empty string. A provider that
    // refuses an empty name refuses the spawn, and the symptom appears far from
    // here as a registry row that never opened.
    for (const description of ['', '!!!', '  ', '---']) {
      expect(spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', { description, prompt: 'Go.' }).arguments)
        .toMatchObject({ task_name: 'scripted_subagent' })
    }
  })

  it('keeps no leading or trailing underscore on the identifier', () => {
    expect(spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', { description: '  Probe it.  ', prompt: 'Go.' }).arguments)
      .toMatchObject({ task_name: 'probe_it' })
  })
})

describe('askUserQuestionToolCall', () => {
  it('states multiSelect rather than omitting it, which Claude\'s schema requires', () => {
    const questions = [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] }]
    const call = askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'call-1', questions)
    expect(call.arguments?.questions).toEqual([{ ...questions[0], multiSelect: false }])
  })

  it('keeps an explicit multiSelect', () => {
    const questions = [{ question: 'Which?', header: 'Choice', multiSelect: true, options: [{ label: 'A', description: 'The first' }] }]
    const call = askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'call-1', questions)
    expect(call.arguments?.questions).toEqual([questions[0]])
  })

  // MiMo's schema spells a multi-select question `multiple` and takes no option
  // field beyond the label and the description.
  it('spells the MiMo Code question in MiMo\'s own fields', () => {
    const questions = [{ question: 'Which?', header: 'Choice', multiSelect: true, options: [{ label: 'A', description: 'The first', preview: '```\nA\n```' }] }]
    const call = askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'call-1', questions)
    expect(call.name).toBe('question')
    expect(call.arguments?.questions).toEqual([{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }], multiple: true }])
    const single = [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] }]
    expect(askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'call-1', single).arguments?.questions)
      .toMatchObject([{ multiple: false }])
  })
  it('gives each Codewhale question its own id and the runtime\'s field names', () => {
    // The answer repeats the question's id, so two questions must never share
    // one. An option's preview is not in the runtime's schema, so it stays out.
    const questions = [
      { question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first', preview: 'x' }] },
      { question: 'Which size?', header: 'Choice', multiSelect: true, options: [{ label: 'S', description: 'Small' }] },
    ]
    const call = askUserQuestionToolCall(AgentProvider.CODEWHALE, 'call-1', questions)
    expect(call.name).toBe('request_user_input')
    expect(call.arguments?.questions).toEqual([
      { id: 'question_1', header: 'Choice', question: 'Which?', options: [{ label: 'A', description: 'The first' }], allow_free_text: false, multi_select: false },
      { id: 'question_2', header: 'Choice', question: 'Which size?', options: [{ label: 'S', description: 'Small' }], allow_free_text: false, multi_select: true },
    ])
  })
  it('uses Codex question ids and omits fields outside its native schema', () => {
    const call = askUserQuestionToolCall(AgentProvider.CODEX, 'codex-question', [
      { question: 'Which color?', header: 'Color', options: [
        { label: 'Blue (Recommended)', description: 'Use blue.', preview: 'ignored' },
        { label: 'Red', description: 'Use red.' },
      ] },
    ])
    expect(call).toMatchObject({
      id: 'codex-question',
      name: 'request_user_input',
      arguments: { questions: [{
        id: 'question_1',
        header: 'Color',
        question: 'Which color?',
        options: [
          { label: 'Blue (Recommended)', description: 'Use blue.' },
          { label: 'Red', description: 'Use red.' },
        ],
      }] },
    })
  })

  it('uses Copilot ask_user with one question and plain choice labels', () => {
    const question = {
      question: 'Which color?',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Use blue.' },
        { label: 'Green', description: 'Use green.' },
      ],
    }
    expect(askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-question', [question]))
      .toEqual({ id: 'copilot-question', name: 'ask_user', arguments: { question: 'Which color?', choices: ['Blue', 'Green'] } })
    expect(() => askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, 'none', [])).toThrow('exactly one')
    expect(() => askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, 'two', [question, question])).toThrow('exactly one')
    expect(() => askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, 'multi', [{ ...question, multiSelect: true }])).toThrow('single-choice')
  })
})

describe('mimoInteractiveBashToolCall', () => {
  it('is the shell call with the interactive flag set', () => {
    const interactive = mimoInteractiveBashToolCall('call-1', 'read -p "Name? " n')
    const plain = bashToolCall(AgentProvider.MIMO_CODE, 'call-1', 'read -p "Name? " n')
    expect(interactive.name).toBe(plain.name)
    expect(interactive.arguments).toMatchObject({ command: 'read -p "Name? " n', interactive: true })
    expect(plain.arguments).not.toHaveProperty('interactive')
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

describe('mcpToolCall', () => {
  // Each agent gives a Model Context Protocol tool a name of its own, built from the
  // server and the tool.
  it('keeps native Pi arguments direct and preserves zero, false, and empty values', () => {
    expect(mcpToolCall(AgentProvider.PI, 'native-direct', { server: 'probe', tool: 'echo', input: { count: 0, enabled: false, text: '' } }))
      .toEqual({ id: 'native-direct', name: 'mcp__probe__echo', arguments: { count: 0, enabled: false, text: '' } })
  })

  it('uses installed Pi sanitation and the native hash suffix for long names', () => {
    expect(mcpToolCall(AgentProvider.PI, 'native-sanitize', { server: 'probe-one', tool: 'echo.출력', input: {} }).name).toBe('mcp__probe-one__echo___')
    expect(mcpToolCall(AgentProvider.PI, 'native-long', { server: 'probe', tool: 'x'.repeat(80), input: {} }).name)
      .toBe('mcp__probe__xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx_2012d930')
  })

  it('calls a Model Context Protocol tool in the agent\'s own shape', () => {
    const request = { server: 'form_probe', tool: 'echo', input: { text: 'hi' } }
    expect(mcpToolCall(AgentProvider.CLAUDE_CODE, 'claude-call', request)).toEqual({
      id: 'claude-call',
      name: 'mcp__form_probe__echo',
      arguments: { text: 'hi' },
    })
    expect(mcpToolCall(AgentProvider.CODEX, 'codex-call', request)).toEqual({
      id: 'codex-call',
      name: 'echo',
      namespace: 'mcp__form_probe',
      arguments: { text: 'hi' },
    })
    expect(mcpToolCall(AgentProvider.CURSOR, 'cursor-call', request)).toEqual({
      id: 'cursor-call',
      name: 'cursorMcp',
      arguments: { server: 'form_probe', tool: 'echo', input: { text: 'hi' } },
    })
    expect(mcpToolCall(AgentProvider.CODEBUDDY, 'codebuddy-call', request)).toEqual({
      id: 'codebuddy-call',
      name: 'mcp__form_probe__echo',
      arguments: { text: 'hi' },
    })
    expect(mcpToolCall(AgentProvider.GROK_BUILD, 'call-1', request)).toEqual({
      id: 'call-1',
      name: 'use_tool',
      arguments: { tool_name: 'form_probe__echo', tool_input: { text: 'hi' } },
    })
    expect(mcpToolCall(AgentProvider.PI, 'call-2', request)).toEqual({
      id: 'call-2',
      name: 'mcp__form_probe__echo',
      arguments: { text: 'hi' },
    })
    expect(mcpToolCall(AgentProvider.FAST_AGENT, 'call-3', request)).toEqual({
      id: 'call-3',
      name: 'form_probe__echo',
      arguments: { text: 'hi' },
    })
    expect(mcpToolCall(AgentProvider.GOOSE, 'goose-call', request)).toEqual({
      id: 'goose-call',
      name: 'form_probe__echo',
      arguments: { text: 'hi' },
    })
    expect(mcpToolCall(AgentProvider.CODEWHALE, 'codewhale-call', request)).toEqual({
      id: 'codewhale-call',
      name: 'mcp_form_probe_echo',
      arguments: { text: 'hi' },
    })
    expect(mcpToolCall(AgentProvider.QWEN_CODE, 'qwen-call', request)).toEqual({
      id: 'qwen-call',
      name: 'mcp__form_probe__echo',
      arguments: { text: 'hi' },
    })
    for (const provider of [AgentProvider.MIMO_CODE, AgentProvider.KILO, AgentProvider.OPENCODE]) {
      expect(mcpToolCall(provider, 'family-call', request)).toEqual({
        id: 'family-call',
        name: 'form_probe_echo',
        arguments: { text: 'hi' },
      })
    }
  })
})

describe('codebuddyWaitForMcpServersToolCall', () => {
  it('waits for the project server before the model calls its tool', () => {
    expect(codebuddyWaitForMcpServersToolCall('wait-call', ['form_probe']))
      .toEqual({ id: 'wait-call', name: 'WaitForMcpServers', arguments: { servers: ['form_probe'] } })
  })
})

describe('CodeBuddy Workflow tool calls', () => {
  it('discovers and executes the deferred Workflow tool', () => {
    expect(codebuddyFindWorkflowToolCall('find')).toEqual({ id: 'find', name: 'ToolSearch', arguments: { tool_names: ['Workflow'] } })
    expect(codebuddyWorkflowToolCall('run', 'return 1')).toEqual({
      id: 'run',
      name: 'DeferExecuteTool',
      arguments: { toolName: 'Workflow', params: { script: 'return 1' } },
    })
  })
})

describe('CodeBuddy task tool calls', () => {
  it('uses the native TaskCreate and TaskUpdate schemas', () => {
    expect(codebuddyTaskCreateToolCall('create', 'Inspect', 'Inspect the repository.')).toEqual({
      id: 'create',
      name: 'TaskCreate',
      arguments: { subject: 'Inspect', description: 'Inspect the repository.' },
    })
    expect(codebuddyTaskUpdateToolCall('update', '1', 'completed')).toEqual({
      id: 'update',
      name: 'TaskUpdate',
      arguments: { taskId: '1', status: 'completed' },
    })
  })
})

describe('blockGoalToolCall', () => {
  it('ends the Codewhale goal loop through the runtime\'s own goal tool', () => {
    expect(blockGoalToolCall(AgentProvider.CODEWHALE, 'call-1', 'Stops here.')).toEqual({
      id: 'call-1',
      name: 'update_goal',
      arguments: { status: 'blocked', blocker: 'Stops here.' },
    })
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

describe('updateTodosToolCall', () => {
  it('writes the whole Codewhale list with the runtime\'s field names', () => {
    const call = updateTodosToolCall(AgentProvider.CODEWHALE, 'call-1', [
      { step: 'First', status: 'completed' },
      { step: 'Second', status: 'in_progress' },
    ])
    expect(call.name).toBe('todo_write')
    expect(call.arguments).toEqual({ todos: [{ content: 'First', status: 'completed' }, { content: 'Second', status: 'in_progress' }] })
  })

  // Claude, ZCode and Reasonix state the whole list as `todos` with
  // `content`/`status`/`activeForm`. The sidebar draws the neutral statuses,
  // and `activeForm` is a display string the schema allows.
  it('writes the Claude list as TodoWrite todos', () => {
    expect(updateTodosToolCall(AgentProvider.CLAUDE_CODE, 'call-1', [
      { step: 'First', status: 'completed' },
      { step: 'Second', status: 'in_progress' },
    ])).toEqual({
      id: 'call-1',
      name: 'TodoWrite',
      arguments: {
        todos: [
          { content: 'First', status: 'completed', activeForm: 'First' },
          { content: 'Second', status: 'in_progress', activeForm: 'Working on: Second' },
        ],
      },
    })
  })

  it('writes the ZCode and Reasonix lists in their own tool names', () => {
    expect(updateTodosToolCall(AgentProvider.ZCODE, 'call-1', [{ step: 'One', status: 'pending' }]).name).toBe('TodoWrite')
    expect(updateTodosToolCall(AgentProvider.REASONIX, 'call-1', [{ step: 'One', status: 'pending' }]).name).toBe('todo_write')
  })

  // Goose and Copilot take a markdown checklist. Only `x` reads as completed;
  // every other marker stays pending. Goose's field is `content`; Copilot's is
  // `todos`.
  it('writes the Goose checklist in content, and the Copilot checklist in todos', () => {
    const steps = [
      { step: 'First', status: 'completed' as const },
      { step: 'Second', status: 'pending' as const },
      { step: 'Third', status: 'in_progress' as const },
    ]
    expect(updateTodosToolCall(AgentProvider.GOOSE, 'call-1', steps)).toEqual({
      id: 'call-1',
      name: 'todo__todo_write',
      arguments: { content: '- [x] First\n- [ ] Second\n- [ ] Third' },
    })
    expect(updateTodosToolCall(AgentProvider.GITHUB_COPILOT, 'call-1', steps)).toEqual({
      id: 'call-1',
      name: 'update_todo',
      arguments: { todos: '- [x] First\n- [ ] Second\n- [ ] Third' },
    })
  })

  // OpenCode and Kilo require a priority for each item. Cursor folds the
  // neutral statuses onto its own enum words.
  it('writes the OpenCode family list with priorities, and the Cursor list with enum statuses', () => {
    for (const provider of [AgentProvider.OPENCODE, AgentProvider.KILO]) {
      expect(updateTodosToolCall(provider, 'call-1', [{ step: 'One', status: 'pending' }])).toEqual({
        id: 'call-1',
        name: 'todowrite',
        arguments: { todos: [{ content: 'One', status: 'pending', priority: 'medium', activeForm: 'One' }] },
      })
    }
    expect(updateTodosToolCall(AgentProvider.CURSOR, 'call-1', [
      { step: 'First', status: 'completed' },
      { step: 'Second', status: 'in_progress' },
      { step: 'Third', status: 'pending' },
    ])).toEqual({
      id: 'call-1',
      name: 'updateTodos',
      arguments: {
        todos: [
          { content: 'First', status: 'TODO_STATUS_COMPLETED' },
          { content: 'Second', status: 'TODO_STATUS_IN_PROGRESS' },
          { content: 'Third', status: 'TODO_STATUS_PENDING' },
        ],
      },
    })
  })
})

describe('editToolCall', () => {
  it('carries both sides of the hunk for every provider that offers an edit tool', () => {
    for (const provider of PROVIDERS.filter(p => hasToolFor(p, 'edit'))) {
      const call = editToolCall(provider, 'call-1', { path: '/tmp/a.txt', before: 'ALPHA', after: 'BETA' })
      let payload = call.input ?? JSON.stringify(call.arguments)
      // Dirac's edit names the old line by an ANCHOR§CONTENT coordinate the
      // step captures out of the request, so its old text rides the capture
      // source rather than the call arguments.
      if (provider === AgentProvider.DIRAC)
        payload += JSON.stringify(diracEditAnchorCapture('ALPHA'))
      expect(payload, `provider ${provider} keeps the old text`).toContain('ALPHA')
      expect(payload, `provider ${provider} keeps the new text`).toContain('BETA')
      expect(payload, `provider ${provider} keeps the path`).toContain('/tmp/a.txt')
    }
  })
})

// Kimi Code's tool schemas set `additionalProperties: false`, so an extra field
// fails the call and a renamed one fails it too. These cases pin the shapes
// that the schemas in its model requests state.
describe('kimi code vocabulary', () => {
  const kimi = AgentProvider.KIMI_CODE

  it('calls a registered MCP tool with its native qualified name', () => {
    expect(mcpToolCall(kimi, 'kimi-mcp', { server: 'echo_probe', tool: 'echo', input: { value: 'kimi' } })).toEqual({
      id: 'kimi-mcp',
      name: 'mcp__echo_probe__echo',
      arguments: { value: 'kimi' },
    })
  })

  it('offers no plan-carrying exit, because its exit call raises the plan file', () => {
    expect(hasToolFor(kimi, 'exitPlanMode')).toBe(false)
    expect(exitPlanModeFromFileToolCall(kimi, 'call-1', [
      { label: 'Option A', description: 'Simple' },
      { label: 'Option B', description: 'Robust' },
    ])).toEqual({
      id: 'call-1',
      name: 'ExitPlanMode',
      arguments: { options: [{ label: 'Option A', description: 'Simple' }, { label: 'Option B', description: 'Robust' }] },
    })
  })

  it('offers the plan-file exit only for providers with native file approval', () => {
    expect(PROVIDERS.filter(p => hasToolFor(p, 'exitPlanModeFromFile'))).toEqual([kimi, AgentProvider.QODER, AgentProvider.GEMINI_CLI])
  })

  it('writes the question in snake case and drops the preview the schema refuses', () => {
    const call = askUserQuestionToolCall(kimi, 'call-1', [{
      question: 'Which?',
      header: 'Choice',
      options: [{ label: 'A', description: 'The first', preview: '```\nA\n```' }, { label: 'B', description: 'The second' }],
    }])
    expect(call.arguments).toEqual({
      questions: [{
        question: 'Which?',
        header: 'Choice',
        options: [{ label: 'A', description: 'The first' }, { label: 'B', description: 'The second' }],
        multi_select: false,
      }],
    })
  })

  it('maps a completed step onto its done status', () => {
    expect(updateTodosToolCall(kimi, 'call-1', [
      { step: 'First', status: 'completed' },
      { step: 'Second', status: 'in_progress' },
      { step: 'Third', status: 'pending' },
    ]).arguments).toEqual({
      todos: [
        { title: 'First', status: 'done' },
        { title: 'Second', status: 'in_progress' },
        { title: 'Third', status: 'pending' },
      ],
    })
  })

  it('states a description for a background command, which the schema then requires', () => {
    expect(backgroundBashToolCall(kimi, 'call-1', 'sleep 60').arguments)
      .toMatchObject({ command: 'sleep 60', run_in_background: true, description: expect.any(String) })
    expect(bashToolCall(kimi, 'call-1', 'echo hi').arguments).toEqual({ command: 'echo hi' })
  })

  it('creates a goal and marks it complete through the goal tools', () => {
    expect(createGoalToolCall(kimi, 'call-1', 'Ship the feature.')).toEqual({ id: 'call-1', name: 'CreateGoal', arguments: { objective: 'Ship the feature.' } })
    expect(completeGoalToolCall(kimi, 'call-2')).toEqual({ id: 'call-2', name: 'UpdateGoal', arguments: { status: 'complete' } })
  })

  it('addresses a file by path in the edit, write, and read tools', () => {
    expect(editToolCall(kimi, 'call-1', { path: '/tmp/a.txt', before: 'a', after: 'b' }).arguments)
      .toEqual({ path: '/tmp/a.txt', old_string: 'a', new_string: 'b' })
    expect(writeToolCall(kimi, 'call-1', { path: '/tmp/a.txt', content: 'x' }).arguments).toEqual({ path: '/tmp/a.txt', content: 'x' })
    expect(readToolCall(kimi, 'call-1', '/tmp/a.txt').arguments).toEqual({ path: '/tmp/a.txt' })
  })
})

describe('qoder cli vocabulary', () => {
  it('passes a native Workflow script without changing its source', () => {
    const script = 'export const meta = { name: "probe", description: "Run one child." };'
    expect(qoderWorkflowToolCall('qoder-workflow', script)).toEqual({
      id: 'qoder-workflow',
      name: 'Workflow',
      arguments: { script },
    })
  })

  it('reads the plan file on ExitPlanMode without a plan argument', () => {
    const qoder = AgentProvider.QODER
    expect(hasToolFor(qoder, 'exitPlanMode')).toBe(false)
    expect(exitPlanModeFromFileToolCall(qoder, 'exit-qoder', [])).toEqual({
      id: 'exit-qoder',
      name: 'ExitPlanMode',
      arguments: {},
    })
  })

  it('calls a registered MCP tool by its native qualified name', () => {
    expect(mcpToolCall(AgentProvider.QODER, 'qoder-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })).toEqual({
      id: 'qoder-mcp-form',
      name: 'mcp__form_probe__ask',
      arguments: {},
    })
  })
})

// These providers state a spawn's foreground or background choice on the native
// wire. An omitted flag lets a CLI release choose which path the test drives.
describe('spawnSubagentToolCall background flag', () => {
  it.each([
    [AgentProvider.CODEWHALE, 'detached'],
    [AgentProvider.GROK_BUILD, 'background'],
    [AgentProvider.QWEN_CODE, 'run_in_background'],
  ])('states the flag both ways for provider %s', (provider, flag) => {
    const request = { description: 'Probe the subagent path', prompt: 'Reply with PONG.' }
    expect(spawnSubagentToolCall(provider, 'call-1', request).arguments).toMatchObject({ [flag]: false })
    expect(spawnSubagentToolCall(provider, 'call-1', { ...request, background: true }).arguments).toMatchObject({ [flag]: true })
  })

  it('leaves the flag to a provider that takes none', () => {
    const call = spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'call-1', { description: 'd', prompt: 'p', background: true })
    expect(call.arguments).not.toHaveProperty('background')
    expect(call.arguments).not.toHaveProperty('run_in_background')
  })
})

describe('Droid Task tool call', () => {
  it('states a valid child type and both native await choices', () => {
    const request = { description: 'Inspect the parser', prompt: 'Count the files.' }
    expect(spawnSubagentToolCall(AgentProvider.DROID, 'droid-task', request)).toEqual({
      id: 'call_droid-task',
      name: 'Task',
      arguments: { subagent_type: 'explorer', description: 'Inspect the parser', prompt: 'Count the files.', await: true },
    })
    expect(spawnSubagentToolCall(AgentProvider.DROID, 'droid-task-bg', { ...request, background: true })).toEqual({
      id: 'call_droid-task-bg',
      name: 'Task',
      arguments: { subagent_type: 'explorer', description: 'Inspect the parser', prompt: 'Count the files.', await: false },
    })
  })
})

describe('the Droid native call identity', () => {
  const originalId = 'shell-79bf521f8c664bea9b0e0467a2b6bba4-0'
  const builders: { operation: string, build: (id: string) => MockModelToolCall }[] = [
    { operation: 'Execute', build: id => bashToolCall(AgentProvider.DROID, id, 'printf SHELL42') },
    { operation: 'Edit', build: id => editToolCall(AgentProvider.DROID, id, { path: '/private/file.txt', before: 'OLD42', after: 'NEW42' }) },
    { operation: 'Create', build: id => writeToolCall(AgentProvider.DROID, id, { path: '/private/file.txt', content: 'NEW42\n' }) },
    { operation: 'Read', build: id => readToolCall(AgentProvider.DROID, id, '/private/file.txt') },
    { operation: 'ExitSpecMode', build: id => exitPlanModeToolCall(AgentProvider.DROID, id, 'Run the actual plan.') },
    { operation: 'AskUser', build: id => askUserQuestionToolCall(AgentProvider.DROID, id, [{ question: 'Select a color.', header: 'Color', options: [{ label: 'Red', description: 'Use red.' }] }]) },
    { operation: 'Task', build: id => spawnSubagentToolCall(AgentProvider.DROID, id, { description: 'Native child', prompt: 'Return CHILD42.' }) },
    { operation: 'TodoWrite', build: id => updateTodosToolCall(AgentProvider.DROID, id, [{ step: 'Read the native file.', status: 'pending' }]) },
    { operation: 'MCP', build: id => mcpToolCall(AgentProvider.DROID, id, { server: 'form_probe', tool: 'ask', input: {} }) },
    { operation: 'ToolSearch', build: id => droidToolSearchToolCall(id, 'form_probe ask') },
  ]
  it.each(builders)('preserves the full original ID in the native $operation call', ({ build }) => {
    expect(build(originalId).id).toBe(`call_${originalId}`)
    expect(build(`call_${originalId}`).id).toBe(`call_${originalId}`)
  })
  it.each(builders)('refuses an absent identity before the native $operation call', ({ build }) => {
    expect(() => build('')).toThrow('nonempty ID')
  })
  it('keeps long call IDs distinct when their first 24 characters are equal', () => {
    const first = bashToolCall(AgentProvider.DROID, originalId, 'printf FIRST42')
    const second = bashToolCall(AgentProvider.DROID, `${originalId}-second`, 'printf SECOND42')
    expect(first.id).not.toBe(second.id)
    expect(first.id.startsWith('call_')).toBe(true)
    expect(second.id.startsWith('call_')).toBe(true)
    expect(first.arguments?.command).toBe('printf FIRST42')
    expect(second.arguments?.command).toBe('printf SECOND42')
  })
})

describe('Droid file tool calls', () => {
  it('uses the installed Read and Edit argument names', () => {
    const path = '/work/droid-note.txt'
    expect(readToolCall(AgentProvider.DROID, 'read-note', path)).toEqual({
      id: 'call_read-note',
      name: 'Read',
      arguments: { file_path: path },
    })
    expect(editToolCall(AgentProvider.DROID, 'edit-note', { path, before: 'before', after: 'after' })).toEqual({
      id: 'call_edit-note',
      name: 'Edit',
      arguments: { file_path: path, old_str: 'before', new_str: 'after' },
    })
  })
})

describe('Droid MCP tool call', () => {
  it('uses the native three-underscore server and tool name', () => {
    expect(droidToolSearchToolCall('search-1', 'form_probe ask')).toEqual({
      id: 'call_search-1',
      name: 'ToolSearch',
      arguments: { query: 'form_probe ask' },
    })
    expect(mcpToolCall(AgentProvider.DROID, 'form-1', { server: 'form_probe', tool: 'ask', input: {} })).toEqual({
      id: 'call_form-1',
      name: 'form_probe___ask',
      arguments: {},
    })
  })
})

describe('Cursor GenerateImage tool call', () => {
  it('keeps the native image path and bytes in its scripted result', () => {
    expect(cursorGenerateImageToolCall('image-1', 'A teal square', '/work/square.png', 'iVBORw0KGgo')).toEqual({
      id: 'image-1',
      name: 'generateImage',
      arguments: { description: 'A teal square', filePath: '/work/square.png', imageData: 'iVBORw0KGgo' },
    })
  })
})

describe('the Kiro tool vocabulary', () => {
  const kiro = AgentProvider.KIRO

  it('uses Kiro\'s own file tools and their argument names', () => {
    expect(editToolCall(kiro, 'c', { path: '/w/a', before: 'A', after: 'B' })).toEqual({ id: 'c', name: 'str_replace', arguments: { path: '/w/a', oldStr: 'A', newStr: 'B' } })
    expect(writeToolCall(kiro, 'c', { path: '/w/a', content: 'x' })).toEqual({ id: 'c', name: 'fs_write', arguments: { path: '/w/a', text: 'x' } })
    expect(readToolCall(kiro, 'c', '/w/a')).toEqual({ id: 'c', name: 'read_file', arguments: { path: '/w/a' } })
  })

  it('asks exactly one question, with titled options', () => {
    const question = { question: 'Which DB?', header: 'DB', options: [{ label: 'Postgres', description: 'pg' }] }
    expect(askUserQuestionToolCall(kiro, 'c', [question]).arguments).toEqual({ question: 'Which DB?', options: [{ title: 'Postgres', description: 'pg' }], reason: 'general-question' })
    expect(() => askUserQuestionToolCall(kiro, 'c', [question, question])).toThrow('exactly one question')
    expect(() => askUserQuestionToolCall(kiro, 'c', [])).toThrow('exactly one question')
  })

  it('refuses a multi-select question, which Kiro\'s user_input cannot ask', () => {
    const question = { question: 'Which DBs?', header: 'DB', options: [{ label: 'Postgres', description: 'pg' }], multiSelect: true }
    expect(() => askUserQuestionToolCall(kiro, 'c', [question])).toThrow('multi-select')
    expect(askUserQuestionToolCall(kiro, 'c', [{ ...question, multiSelect: false }]).name).toBe('user_input')
  })

  it('calls a Model Context Protocol tool by the name that Kiro gives it', () => {
    expect(mcpToolCall(kiro, 'kiro-mcp', { server: 'probe', tool: 'ask', input: {} })).toEqual({ id: 'kiro-mcp', name: 'mcp_probe_ask', arguments: {} })
    expect(mcpToolCall(kiro, 'c', { server: 'form_probe', tool: 'echo', input: { text: 'hi' } }).arguments).toEqual({ text: 'hi' })
  })

  it('offers no background command, which no Kiro spec runs', () => {
    expect(hasToolFor(kiro, 'backgroundBash')).toBe(false)
  })

  it('invokes a bundled agent with the description as its reason', () => {
    expect(spawnSubagentToolCall(kiro, 'c', { description: 'Find the file', prompt: 'Look.' }).arguments).toEqual({ name: 'context-gatherer', prompt: 'Look.', explanation: 'Find the file' })
  })

  it('creates the to-do list from the steps, and completes tasks by their ids', () => {
    expect(updateTodosToolCall(kiro, 'c', [{ step: 'One', status: 'pending' }]).arguments).toMatchObject({ command: 'create', tasks: [{ task_description: 'One' }] })
    expect(kiroCompleteTodosToolCall('c', ['1', '2']).arguments).toMatchObject({ command: 'complete', completed_task_ids: ['1', '2'] })
  })

  it('leaves the plan mode through its own switch, which is no plan approval', () => {
    expect(hasToolFor(kiro, 'exitPlanMode')).toBe(false)
    expect(kiroSwitchToExecutionToolCall('c', '1. Do it')).toEqual({ id: 'c', name: 'switch_to_execution', arguments: { plan: '1. Do it' } })
  })

  it('completes a goal step through send_message', () => {
    expect(completeGoalToolCall(kiro, 'c')).toEqual({ id: 'c', name: 'send_message', arguments: { message: 'The goal is verified.', severity: 'success' } })
  })

  it('blocks a goal through an error that a step sends', () => {
    expect(blockGoalToolCall(kiro, 'c', 'The repository is read-only.')).toEqual({ id: 'c', name: 'send_message', arguments: { message: 'The repository is read-only.', severity: 'error' } })
  })
})

describe('the Grok Build tool vocabulary', () => {
  // Grok reads its plan from the file the model wrote in plan mode, and its
  // `exit_plan_mode` schema declares no argument at all.
  it('sends no plan text with exit_plan_mode', () => {
    expect(exitPlanModeToolCall(AgentProvider.GROK_BUILD, 'call-1', 'The plan.').arguments).toEqual({})
  })

  it('spells the multi-select flag the way its schema does', () => {
    const call = askUserQuestionToolCall(AgentProvider.GROK_BUILD, 'call-1', [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'a' }], multiSelect: true }])
    expect(call.arguments).toEqual({ questions: [{ question: 'Which?', options: [{ label: 'A', description: 'a' }], multi_select: true }] })
  })

  it('replaces the whole to-do list, so the scripted steps are the list', () => {
    const call = updateTodosToolCall(AgentProvider.GROK_BUILD, 'call-1', [{ step: 'First', status: 'pending' }, { step: 'Second', status: 'completed' }])
    expect(call.arguments).toEqual({ merge: false, todos: [{ id: '1', content: 'First', status: 'pending' }, { id: '2', content: 'Second', status: 'completed' }] })
  })

  it('states the description its shell tool requires', () => {
    expect(bashToolCall(AgentProvider.GROK_BUILD, 'call-1', 'ls').arguments).toEqual({ command: 'ls', description: 'Run the scripted command' })
    expect(backgroundBashToolCall(AgentProvider.GROK_BUILD, 'call-1', 'sleep 9').arguments).toMatchObject({ background: true })
  })
})

describe('the Qwen Code tool vocabulary', () => {
  it('uses its own file tools with an absolute path argument', () => {
    expect(readToolCall(AgentProvider.QWEN_CODE, 'call-1', '/p/a.txt')).toMatchObject({ name: 'read_file', arguments: { file_path: '/p/a.txt' } })
    expect(editToolCall(AgentProvider.QWEN_CODE, 'call-1', { path: '/p/a.txt', before: 'a', after: 'b' })).toMatchObject({ name: 'edit', arguments: { file_path: '/p/a.txt', old_string: 'a', new_string: 'b' } })
    expect(writeToolCall(AgentProvider.QWEN_CODE, 'call-1', { path: '/p/a.txt', content: 'x' })).toMatchObject({ name: 'write_file' })
  })

  it('carries the plan in exit_plan_mode and backgrounds a command with its own flag', () => {
    expect(exitPlanModeToolCall(AgentProvider.QWEN_CODE, 'call-1', 'The plan.').arguments).toEqual({ plan: 'The plan.' })
    expect(backgroundBashToolCall(AgentProvider.QWEN_CODE, 'call-1', 'sleep 9').arguments).toMatchObject({ is_background: true })
  })
})

// omp 18.2.11's own tool schemas. Only the specs 133 to 139 script these shapes, and
// CI runs no E2E, so these cases are the check that runs on every change.
describe('the Oh My Pi tool vocabulary', () => {
  it('yields a subagent\'s report as `data`', () => {
    expect(ohMyPiYieldToolCall('call-1', 'Done.')).toEqual({ id: 'call-1', name: 'yield', arguments: { data: 'Done.' } })
  })

  const omp = AgentProvider.OH_MY_PI

  it('uses the minted MCP tool name', () => {
    expect(mcpToolCall(omp, 'omp-mcp', { server: 'echo_probe', tool: 'echo', input: { value: 'omp' } })).toEqual({
      id: 'omp-mcp',
      name: 'mcp__echo_probe_echo',
      arguments: { value: 'omp' },
    })
  })

  it('addresses a file by path in the read, write and replace-mode edit tools', () => {
    expect(bashToolCall(omp, 'call-1', 'ls')).toEqual({ id: 'call-1', name: 'bash', arguments: { command: 'ls' } })
    expect(readToolCall(omp, 'call-1', 'notes.txt')).toEqual({ id: 'call-1', name: 'read', arguments: { path: 'notes.txt' } })
    expect(writeToolCall(omp, 'call-1', { path: 'a.txt', content: 'x\n' })).toEqual({ id: 'call-1', name: 'write', arguments: { path: 'a.txt', content: 'x\n' } })
    // The E2E profile sets `edit.mode: replace`. The default hashline edit addresses
    // lines by a hash of the file, which a scripted turn cannot compute.
    expect(editToolCall(omp, 'call-1', { path: 'a.ts', before: 'a', after: 'b' })).toEqual({ id: 'call-1', name: 'edit', arguments: { path: 'a.ts', old_string: 'a', new_string: 'b' } })
  })

  it('gives each question of an ask call its own id, and states multi for a multi-select alone', () => {
    // The question bridge matches each answer to its question by this id.
    const call = askUserQuestionToolCall(omp, 'call-1', [
      { question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] },
      { question: 'Which sizes?', header: 'Sizes', multiSelect: true, options: [{ label: 'S', description: 'Small', preview: '```\nS\n```' }] },
    ])
    expect(call).toEqual({
      id: 'call-1',
      name: 'ask',
      arguments: {
        questions: [
          { id: 'q1', question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] },
          { id: 'q2', question: 'Which sizes?', header: 'Sizes', options: [{ label: 'S', description: 'Small', preview: '```\nS\n```' }], multi: true },
        ],
      },
    })
  })

  it('spawns a subagent through a task batch, whose task name becomes the subagent\'s id', () => {
    // Spec 136 finds the registry row by this id. omp takes no background flag in
    // the call: its `async.enabled` setting decides, and the E2E profile turns it off.
    expect(spawnSubagentToolCall(omp, 'call-1', { description: 'Run the fruit task', prompt: 'List three fruits.', background: true })).toEqual({
      id: 'call-1',
      name: 'task',
      arguments: { context: 'Run the fruit task', tasks: [{ name: 'run_the_fruit_task', agent: 'task', task: 'List three fruits.' }] },
    })
  })

  it('opens a to-do list with init, in one phase, and leaves each status to omp', () => {
    expect(updateTodosToolCall(omp, 'call-1', [{ step: 'First', status: 'completed' }, { step: 'Second', status: 'pending' }])).toEqual({
      id: 'call-1',
      name: 'todo',
      arguments: { op: 'init', list: [{ phase: 'Plan', items: ['First', 'Second'] }] },
    })
  })

  it('offers no plan-mode, goal or background shell tool, which omp\'s RPC mode does not reach', () => {
    for (const operation of ['enterPlanMode', 'exitPlanMode', 'exitPlanModeFromFile', 'backgroundBash', 'createGoal', 'completeGoal', 'blockGoal'] as const)
      expect(hasToolFor(omp, operation), operation).toBe(false)
  })
})

describe('the Cline tool vocabulary', () => {
  const cline = AgentProvider.CLINE

  it('uses the SDK MCP server and tool separator', () => {
    expect(mcpToolCall(cline, 'cline-mcp', { server: 'echo_probe', tool: 'echo', input: { value: 'cline' } })).toEqual({
      id: 'cline-mcp',
      name: 'echo_probe__echo',
      arguments: { value: 'cline' },
    })
  })

  it('uses Cline\'s own tools and their argument names', () => {
    expect(bashToolCall(cline, 'c', 'echo hi')).toEqual({ id: 'c', name: 'run_commands', arguments: { commands: ['echo hi'] } })
    expect(editToolCall(cline, 'c', { path: '/w/a', before: 'A', after: 'B' })).toEqual({ id: 'c', name: 'editor', arguments: { path: '/w/a', old_text: 'A', new_text: 'B' } })
    // A write is the same editor call with no old text, which creates the file.
    expect(writeToolCall(cline, 'c', { path: '/w/a', content: 'x' })).toEqual({ id: 'c', name: 'editor', arguments: { path: '/w/a', new_text: 'x' } })
    expect(readToolCall(cline, 'c', '/w/a')).toEqual({ id: 'c', name: 'read_files', arguments: { files: [{ path: '/w/a' }] } })
  })

  it('leaves plan mode through the plan tool, which carries no plan', () => {
    expect(hasToolFor(cline, 'enterPlanMode')).toBe(false)
    expect(exitPlanModeToolCall(cline, 'c', 'The plan.')).toEqual({ id: 'c', name: 'switch_to_act_mode', arguments: {} })
  })

  it('asks exactly one question with 2 to 5 bare options', () => {
    const option = (label: string) => ({ label, description: `The ${label}` })
    const question = { question: 'Which DB?', header: 'DB', options: [option('Postgres'), option('SQLite')] }
    expect(askUserQuestionToolCall(cline, 'c', [question]).arguments).toEqual({ question: 'Which DB?', options: ['Postgres', 'SQLite'] })
    expect(() => askUserQuestionToolCall(cline, 'c', [question, question])).toThrow('exactly one question')
    expect(() => askUserQuestionToolCall(cline, 'c', [])).toThrow('exactly one question')
    expect(() => askUserQuestionToolCall(cline, 'c', [{ ...question, multiSelect: true }])).toThrow('no multi-select')
    expect(() => askUserQuestionToolCall(cline, 'c', [{ ...question, options: [option('Postgres')] }])).toThrow('2 to 5 options')
    expect(() => askUserQuestionToolCall(cline, 'c', [{ ...question, options: ['A', 'B', 'C', 'D', 'E', 'F'].map(option) }])).toThrow('2 to 5 options')
  })

  it('leads the subagent task with its description, which titles the registry row', () => {
    const call = spawnSubagentToolCall(cline, 'c', { description: 'Probe the subagent path', prompt: 'Reply with PONG.', background: true })
    expect(call.name).toBe('spawn_agent')
    expect(call.arguments).toEqual({ systemPrompt: expect.any(String), task: 'Probe the subagent path\n\nReply with PONG.' })
  })
})

// The Amp specs alone script these shapes, and CI runs no E2E, so these cases are
// the check that runs on every change.
describe('the Amp tool vocabulary', () => {
  const amp = AgentProvider.AMP

  it('runs a command through shell_command, and backgrounds it with a one-second wait', () => {
    expect(bashToolCall(amp, 'c', 'ls')).toEqual({ id: 'c', name: 'shell_command', arguments: { command: 'ls' } })
    expect(backgroundBashToolCall(amp, 'c', 'sleep 60')).toEqual({ id: 'c', name: 'shell_command', arguments: { command: 'sleep 60', timeout_ms: 1_000 } })
  })

  it('edits and writes through one apply_patch text, and reads by path', () => {
    expect(editToolCall(amp, 'c', { path: '/w/a.ts', before: 'old', after: 'new' })).toEqual({
      id: 'c',
      name: 'apply_patch',
      arguments: { patchText: '*** Begin Patch\n*** Update File: /w/a.ts\n@@\n-old\n+new\n*** End Patch' },
    })
    expect(writeToolCall(amp, 'c', { path: '/w/b.ts', content: 'x\ny' })).toEqual({
      id: 'c',
      name: 'apply_patch',
      arguments: { patchText: '*** Begin Patch\n*** Add File: /w/b.ts\n+x\n+y\n*** End Patch' },
    })
    expect(readToolCall(amp, 'c', '/w/a.ts')).toEqual({ id: 'c', name: 'Read', arguments: { path: '/w/a.ts' } })
  })

  it('spawns a subagent through Task, which Amp runs on its server', () => {
    expect(spawnSubagentToolCall(amp, 'c', { description: 'Probe it', prompt: 'Go.', background: true }))
      .toEqual({ id: 'c', name: 'Task', arguments: { description: 'Probe it', prompt: 'Go.' } })
  })

  it('offers no plan mode, question, to-do, or goal tool', () => {
    for (const operation of ['enterPlanMode', 'exitPlanMode', 'exitPlanModeFromFile', 'askUserQuestion', 'updateTodos', 'createGoal', 'completeGoal', 'blockGoal'] as const)
      expect(hasToolFor(amp, operation), operation).toBe(false)
    expect(hasToolFor(amp, 'mcpTool')).toBe(true)
  })
})

/**
 * The patch text of an apply_patch call: Amp states it as an argument, and Codex
 * states it as the JSON string literal that its `exec` source passes.
 * A patch marks each hunk line. An unmarked line is not part of the hunk,
 * so apply_patch refuses the patch or applies another change.
 */
function patchTextOf(call: MockModelToolCall): string {
  const argument = call.arguments?.patchText
  if (typeof argument === 'string')
    return argument
  const literal = /tools\.apply_patch\(("(?:[^"\\]|\\.)*")\)/.exec(call.input ?? '')?.[1]
  if (literal === undefined)
    throw new Error(`The call ${call.name} states no apply_patch text`)
  return JSON.parse(literal) as string
}

describe('the Junie tool vocabulary', () => {
  const junie = AgentProvider.JUNIE

  it('runs a command through bash and reads through open_entire_file', () => {
    expect(bashToolCall(junie, 'c', 'ls')).toEqual({ id: 'c', name: 'bash', arguments: { command: 'ls' } })
    expect(readToolCall(junie, 'c', '/w/a.ts')).toEqual({ id: 'c', name: 'open_entire_file', arguments: { path: '/w/a.ts' } })
    expect(writeToolCall(junie, 'c', { path: '/w/b.ts', content: 'x' })).toEqual({
      id: 'c',
      name: 'create',
      arguments: { filename: '/w/b.ts', content: 'x' },
    })
  })

  it('edits through search_replace with the search and replace blocks', () => {
    expect(editToolCall(junie, 'c', { path: '/w/a.ts', before: 'old', after: 'new' })).toEqual({
      id: 'c',
      name: 'search_replace',
      arguments: { file_path: '/w/a.ts', search: 'old', replace: 'new' },
    })
  })

  it('names every ask_user question and spawns a subagent by task', () => {
    expect(askUserQuestionToolCall(junie, 'c', [{ question: 'Which?', header: 'Pick', options: [{ label: 'A', description: 'The first' }] }]))
      .toEqual({
        id: 'c',
        name: 'ask_user',
        arguments: {
          questions: [{ name: 'Pick', question: 'Which?', options: [{ title: 'A', description: 'The first' }], allowMultiple: false }],
        },
      })
    expect(spawnSubagentToolCall(junie, 'c', { description: 'Probe it', prompt: 'Go.' })).toEqual({
      id: 'c',
      name: 'spawn_subagent',
      arguments: { agent: 'junie-cli-docs', name: 'Probe it', task: 'Go.' },
    })
    expect(spawnSubagentToolCall(junie, 'custom', {
      description: 'Read the test file',
      prompt: 'Read note.txt.',
      agentType: 'leapmux-e2e-child',
    })).toEqual({
      id: 'custom',
      name: 'spawn_subagent',
      arguments: { agent: 'leapmux-e2e-child', name: 'Read the test file', task: 'Read note.txt.' },
    })
  })

  it('submits a plan through the installed submit tool', () => {
    expect(junieSubmitPlanToolCall(
      'p',
      'probe-plan',
      [{ name: 'Requirements', content: 'Inspect the repository.' }],
      [{ name: 'Inspect the repository', description: 'Read the relevant files.' }],
    )).toEqual({
      id: 'p',
      name: 'submit',
      arguments: {
        name: 'probe-plan',
        proposal: [{ name: 'Requirements', content: 'Inspect the repository.' }],
        delivery_plan: [{ name: 'Inspect the repository', description: 'Read the relevant files.' }],
      },
    })
  })

  it('submits the bundled documentation child answer with solution_summary', () => {
    expect(junieSubagentSubmitToolCall('child', 'Junie keeps sessions in its home.')).toEqual({
      id: 'child',
      name: 'submit',
      arguments: { solution_summary: 'Junie keeps sessions in its home.' },
    })
  })

  it('uses the native MCP server and tool spelling', () => {
    expect(mcpToolCall(junie, 'form-call', { server: 'form_probe', tool: 'ask', input: {} })).toEqual({
      id: 'form-call',
      name: 'mcp_form_probe_ask',
      arguments: {},
    })
  })
})

describe('the Dirac tool vocabulary', () => {
  const dirac = AgentProvider.DIRAC

  it('returns the native compaction context through condense', () => {
    expect(diracCondenseToolCall('summary-1', 'Keep the branch state.')).toEqual({
      id: 'summary-1',
      name: 'condense',
      arguments: { context: 'Keep the branch state.' },
    })
  })

  it('sends the schema fields alone, never the rawInput tool stamp', () => {
    // Dirac stamps `tool` into the rawInput it REPORTS; the model that sends it
    // gets "Unsupported response parameter: tool".
    expect(bashToolCall(dirac, 'c', 'ls')).toEqual({ id: 'c', name: 'execute_command', arguments: { commands: ['ls'] } })
    expect(readToolCall(dirac, 'c', '/w/a.ts')).toEqual({
      id: 'c',
      name: 'read_file',
      arguments: { paths: ['/w/a.ts'], include_anchors: true },
    })
    expect(writeToolCall(dirac, 'c', { path: '/w/b.ts', content: 'x' })).toEqual({
      id: 'c',
      name: 'write_to_file',
      arguments: { path: '/w/b.ts', content: 'x' },
    })
  })

  it('edits through edit_file by ANCHOR§CONTENT coordinate', () => {
    expect(editToolCall(dirac, 'c', { path: '/w/a.ts', before: 'old', after: 'new' })).toEqual({
      id: 'c',
      name: 'edit_file',
      arguments: {
        files: [{
          path: '/w/a.ts',
          edits: [{ edit_type: 'replace', anchor: '{{editAnchor}}', end_anchor: '{{editAnchor}}', text: 'new' }],
        }],
      },
    })
    expect(diracEditAnchorCapture('old')).toEqual({ editAnchor: '([A-Z][a-zA-Z]*§old)' })
  })

  it('ends a turn through respond without the rawInput tool stamp', () => {
    expect(diracRespondToolCall('c', 'complete', 'Done.')).toEqual({
      id: 'c',
      name: 'respond',
      arguments: { operation: 'complete', text: 'Done.' },
    })
  })

  it('spawns a child with the native use_subagents array', () => {
    expect(spawnSubagentToolCall(dirac, 'c', { description: 'Count files', prompt: 'Count the files.' })).toEqual({
      id: 'c',
      name: 'use_subagents',
      arguments: { subagents: [{ task_title: 'Count files', prompt: 'Count the files.' }] },
    })
  })
})

describe('editToolCall patch text', () => {
  it.each([AgentProvider.AMP, AgentProvider.CODEX])('marks each line of a multi-line hunk for provider %s', (provider) => {
    const call = editToolCall(provider, 'c', { path: 'a.ts', before: 'one\ntwo', after: 'three\nfour' })
    expect(patchTextOf(call)).toBe('*** Begin Patch\n*** Update File: a.ts\n@@\n-one\n-two\n+three\n+four\n*** End Patch')
  })

  it.each([AgentProvider.AMP, AgentProvider.CODEX])('marks the one empty line of an empty side for provider %s', (provider) => {
    const call = editToolCall(provider, 'c', { path: 'a.ts', before: 'gone', after: '' })
    expect(patchTextOf(call)).toBe('*** Begin Patch\n*** Update File: a.ts\n@@\n-gone\n+\n*** End Patch')
  })

  it.each([AgentProvider.AMP, AgentProvider.CODEX])('writes the same added-file patch for provider %s', (provider) => {
    const call = writeToolCall(provider, 'c', { path: 'b.ts', content: 'x\ny' })
    expect(patchTextOf(call)).toBe('*** Begin Patch\n*** Add File: b.ts\n+x\n+y\n*** End Patch')
  })
})

// The MiMo Code specs alone script these shapes. Every input field is snake_case.
describe('the MiMo Code tool vocabulary', () => {
  const mimo = AgentProvider.MIMO_CODE

  it('addresses a file by file_path, and states the description that its shell tool requires', () => {
    expect(bashToolCall(mimo, 'c', 'ls')).toEqual({ id: 'c', name: 'bash', arguments: { command: 'ls', description: 'Run the scripted command' } })
    expect(editToolCall(mimo, 'c', { path: 'a.ts', before: 'a', after: 'b' })).toEqual({ id: 'c', name: 'edit', arguments: { file_path: 'a.ts', old_string: 'a', new_string: 'b' } })
    expect(writeToolCall(mimo, 'c', { path: 'a.ts', content: 'x' })).toEqual({ id: 'c', name: 'write', arguments: { file_path: 'a.ts', content: 'x' } })
    expect(readToolCall(mimo, 'c', 'a.ts')).toEqual({ id: 'c', name: 'read', arguments: { file_path: 'a.ts' } })
  })

  it('leaves plan mode through plan_exit, which carries no plan', () => {
    expect(exitPlanModeToolCall(mimo, 'c', 'The plan.')).toEqual({ id: 'c', name: 'plan_exit', arguments: {} })
    expect(hasToolFor(mimo, 'enterPlanMode')).toBe(false)
  })

  it('offers no whole-list to-do update, because its task tool acts on one item', () => {
    expect(hasToolFor(mimo, 'updateTodos')).toBe(false)
  })

  it('calls the local MCP tool under its server prefix', () => {
    expect(mcpToolCall(mimo, 'ask-1', { server: 'form_probe', tool: 'ask', input: {} }))
      .toEqual({ id: 'ask-1', name: 'form_probe_ask', arguments: {} })
  })
})

describe('mimoWorkflowToolCall', () => {
  it('runs the script through the workflow tool', () => {
    expect(mimoWorkflowToolCall('c', 'await agent("Go.")')).toEqual({ id: 'c', name: 'workflow', arguments: { operation: 'run', script: 'await agent("Go.")' } })
  })
})

describe('the Codewhale tool vocabulary', () => {
  const codewhale = AgentProvider.CODEWHALE

  it('addresses a file by path, and edits through a list of replacements', () => {
    expect(bashToolCall(codewhale, 'c', 'ls')).toEqual({ id: 'c', name: 'bash', arguments: { command: 'ls' } })
    expect(editToolCall(codewhale, 'c', { path: 'a.ts', before: 'a', after: 'b' })).toEqual({ id: 'c', name: 'edit', arguments: { path: 'a.ts', edits: [{ oldText: 'a', newText: 'b' }] } })
    expect(writeToolCall(codewhale, 'c', { path: 'a.ts', content: 'x' })).toEqual({ id: 'c', name: 'write', arguments: { path: 'a.ts', content: 'x' } })
    expect(readToolCall(codewhale, 'c', 'a.ts')).toEqual({ id: 'c', name: 'read', arguments: { path: 'a.ts' } })
  })

  it('offers no plan-mode tool and no goal creation, which are thread settings', () => {
    for (const operation of ['enterPlanMode', 'exitPlanMode', 'createGoal', 'completeGoal'] as const)
      expect(hasToolFor(codewhale, operation), operation).toBe(false)
  })
})

describe('the Fast Agent tool vocabulary', () => {
  const fastAgent = AgentProvider.FAST_AGENT

  it('builds the native human-input schema without adding an ACP question route', () => {
    const answers = ['first', 'second']
    const call = fastAgentHumanInputToolCall('native-input', 'Choose one answer.', answers)
    answers.push('later')
    expect(call).toEqual({
      id: 'native-input',
      name: '__human_input',
      arguments: { message: 'Choose one answer.', schema: { type: 'object', properties: { answer: { type: 'string', enum: ['first', 'second'] } }, required: ['answer'] } },
    })
    expect(hasToolFor(fastAgent, 'askUserQuestion')).toBe(false)
  })

  it('preserves a valid empty message and empty string choice', () => {
    expect(fastAgentHumanInputToolCall('native-empty', '', ['']).arguments).toMatchObject({ message: '', schema: { properties: { answer: { enum: [''] } } } })
  })

  it('refuses a human-input schema without an answer choice', () => {
    expect(() => fastAgentHumanInputToolCall('native-input', 'Choose.', [])).toThrow('answer choice')
  })

  it('runs a command through execute, and reads and writes through the text-file tools', () => {
    // The live `-x` runtime answers `bash` with "Tool 'bash' is not available.
    // Available tools: attach_media, edit_file, execute, read_text_file,
    // write_text_file." -- so the names below are the whole contract.
    expect(bashToolCall(fastAgent, 'c', 'ls')).toEqual({ id: 'c', name: 'execute', arguments: { command: 'ls' } })
    expect(readToolCall(fastAgent, 'c', '/w/a.ts')).toEqual({ id: 'c', name: 'read_text_file', arguments: { path: '/w/a.ts' } })
    expect(writeToolCall(fastAgent, 'c', { path: '/w/b.ts', content: 'x\ny' })).toEqual({
      id: 'c',
      name: 'write_text_file',
      arguments: { path: '/w/b.ts', content: 'x\ny' },
    })
  })

  it('edits through edit_file with the old and new text', () => {
    expect(editToolCall(fastAgent, 'c', { path: '/w/a.ts', before: 'old', after: 'new' })).toEqual({
      id: 'c',
      name: 'edit_file',
      arguments: { path: '/w/a.ts', old_string: 'old', new_string: 'new' },
    })
  })

  it('spawns a child with the native message argument', () => {
    expect(spawnSubagentToolCall(fastAgent, 'c', { description: 'Count files', prompt: 'Count the files.' })).toEqual({
      id: 'c',
      name: 'subagent',
      arguments: { message: 'Count the files.', label: 'Count files' },
    })
  })

  it('offers no background shell, plan-mode, question, to-do, or goal tool', () => {
    // `execute`'s schema is command/args/env/cwd with additionalProperties
    // false, and no separate background tool is offered.
    for (const operation of ['backgroundBash', 'enterPlanMode', 'exitPlanMode', 'exitPlanModeFromFile', 'askUserQuestion', 'updateTodos', 'createGoal', 'completeGoal', 'blockGoal'] as const)
      expect(hasToolFor(fastAgent, operation), operation).toBe(false)
    expect(hasToolFor(fastAgent, 'mcpTool')).toBe(true)
  })
})

describe('the Letta Code task vocabulary', () => {
  it('opens an image with the native ViewImage tool', () => {
    expect(lettaViewImageToolCall('image-1', '/work/shot.png')).toEqual({
      id: 'image-1',
      name: 'ViewImage',
      arguments: { path: '/work/shot.png' },
    })
  })

  it('offers no one-call whole-list update for its TaskCreate and TaskUpdate tools', () => {
    expect(hasToolFor(AgentProvider.LETTA, 'updateTodos')).toBe(false)
  })

  it('uses the installed TaskCreate, TaskUpdate, and TaskList argument fields', () => {
    expect(lettaTaskCreateToolCall('create-1', 'Inspect', 'Read the files.')).toEqual({
      id: 'create-1',
      name: 'TaskCreate',
      arguments: { subject: 'Inspect', description: 'Read the files.' },
    })
    expect(lettaTaskUpdateToolCall('update-1', 'task_1', 'completed')).toEqual({
      id: 'update-1',
      name: 'TaskUpdate',
      arguments: { taskId: 'task_1', status: 'completed' },
    })
    expect(lettaTaskListToolCall('list-1')).toEqual({
      id: 'list-1',
      name: 'TaskList',
      arguments: {},
    })
  })
})

describe('new native executor and child completion calls', () => {
  it('keeps every native Gemini task status and copies the checklist input', () => {
    const todos: Parameters<typeof geminiTodoSnapshotToolCall>[1] = [
      { description: ' Pending task.\n실제 내용 🧪 ', status: 'pending' },
      { description: 'Current task.', status: 'in_progress' },
      { description: 'Finished task.', status: 'completed' },
      { description: 'Removed task.', status: 'cancelled' },
      { description: 'Blocked task.', status: 'blocked' },
    ]
    const call = geminiTodoSnapshotToolCall('native-checklist', todos)
    expect(call.name).toBe('write_todos')
    expect(call.arguments).toEqual({ todos })
    expect(call.arguments?.todos).not.toBe(todos)
    expect(geminiTodoSnapshotToolCall('empty-checklist', [])).toEqual({ id: 'empty-checklist', name: 'write_todos', arguments: { todos: [] } })
  })

  it('provides the installed Gemini Replace instruction and preserves the exact replacement text', () => {
    const path = '/work/file with spaces.ts'
    const before = '\uFEFFold text\r\n실제 내용 🧪'
    const after = 'new text\r\n실제 결과 🧪'
    const call = editToolCall(AgentProvider.GEMINI_CLI, 'native-replace', { path, before, after })
    expect(call).toEqual({
      id: 'native-replace',
      name: 'replace',
      arguments: {
        file_path: path,
        old_string: before,
        new_string: after,
        instruction: 'Apply the requested replacement exactly.',
      },
    })
    expect(call.arguments).not.toHaveProperty('expected_replacements')
  })

  it('keeps the native Deepseek image path without shell conversion', () => {
    const path = '/work/a file/실제 그림 🧪.png'
    expect(deepseekHarnessReadImageToolCall('native-image', path)).toEqual({
      id: 'native-image',
      name: 'read_image',
      arguments: { file_path: path },
    })
  })

  it('requests the native wider sandbox with the exact reason and command', () => {
    const command = 'printf \'native result42\\n\''
    const justification = '  Write the probe file.\n실제 이유 🧪  '
    expect(deepseekHarnessEscalatedBashToolCall('native-permission', command, justification)).toEqual({
      id: 'native-permission',
      name: 'bash',
      arguments: { command, description: 'Run the scripted command.', sandbox_permissions: 'danger-full-access', justification },
    })
  })

  it('preserves the Deepseek workflow source and native metadata', () => {
    const source = 'return await Promise.resolve(40 + 2)'
    expect(codeExecutionToolCall(AgentProvider.DEEPSEEK_HARNESS, 'native-exec', source)).toEqual({
      id: 'native-exec',
      name: 'workflow',
      arguments: { script: source, meta: { name: 'native-code', description: 'Run the scripted native source.' } },
    })
  })

  it('keeps native DeepSeek tool-bound code separate from the workflow payload', () => {
    const source = '  const value = await tools.mcp__results__inspect({count: 0, enabled: false, text: "結果"});\nreturn value;  '
    expect(deepseekHarnessRunCodeToolCall('native-code', source)).toEqual({
      id: 'native-code',
      name: 'run_code',
      arguments: { description: 'Read the native MCP value.', code: source },
    })
  })

  it.each(['', ' ', '\t\n'])('rejects an absent DeepSeek code source %j', (source) => {
    expect(() => deepseekHarnessRunCodeToolCall('native-code', source)).toThrow('nonempty call ID and source')
  })

  it.each(['', ' ', '\t\n'])('rejects an absent DeepSeek code call ID %j', (id) => {
    expect(() => deepseekHarnessRunCodeToolCall(id, 'return 42')).toThrow('nonempty call ID and source')
  })

  it('preserves Gemini child response bytes and an explicit empty result', () => {
    const response = '  Native response.\n실제 내용 🧪  '
    expect(geminiCompleteTaskToolCall('native-complete', response)).toEqual({ id: 'native-complete', name: 'complete_task', arguments: { result: { response } } })
    expect(geminiCompleteTaskToolCall('native-empty', '').arguments).toEqual({ result: { response: '' } })
  })
})
