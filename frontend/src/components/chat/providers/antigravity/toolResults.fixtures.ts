import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { ANTIGRAVITY_STEP_TYPE, ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { parsedFrame } from '~/test-support/mimoFixtures'

const TYPES = new Map<string, string>([
  [ANTIGRAVITY_TOOL.ReadFile, ANTIGRAVITY_STEP_TYPE.ViewFile],
  [ANTIGRAVITY_TOOL.ViewFile, ANTIGRAVITY_STEP_TYPE.ViewFile],
  [ANTIGRAVITY_TOOL.WriteFile, ANTIGRAVITY_STEP_TYPE.CodeAction],
  [ANTIGRAVITY_TOOL.EditFile, ANTIGRAVITY_STEP_TYPE.CodeAction],
  [ANTIGRAVITY_TOOL.RunCommand, ANTIGRAVITY_STEP_TYPE.RunCommand],
  [ANTIGRAVITY_TOOL.InvokeSubagent, ANTIGRAVITY_STEP_TYPE.InvokeSubagent],
  [ANTIGRAVITY_TOOL.AskQuestion, ANTIGRAVITY_STEP_TYPE.AskQuestion],
  [ANTIGRAVITY_TOOL.CallMcpTool, ANTIGRAVITY_STEP_TYPE.McpTool],
  [ANTIGRAVITY_TOOL.ListResources, ANTIGRAVITY_STEP_TYPE.McpTool],
  [ANTIGRAVITY_TOOL.ReadResource, ANTIGRAVITY_STEP_TYPE.McpTool],
])

function step(tool: string, status: string, args: Record<string, unknown>, extra: Record<string, unknown>): Record<string, unknown> {
  return {
    type: TYPES.get(tool) ?? ANTIGRAVITY_STEP_TYPE.Generic,
    status,
    metadata: {
      toolCall: {
        id: `call-${tool}`,
        name: tool,
        argumentsJson: JSON.stringify(args),
      },
    },
    ...extra,
  }
}

function done(tool: string, args: Record<string, unknown>, extra: Record<string, unknown>): ToolResultFixture {
  return {
    payload: step(tool, 'CORTEX_STEP_STATUS_DONE', args, extra),
    options: {
      spanType: 'antigravity_step',
      request: parsedFrame(step(tool, 'CORTEX_STEP_STATUS_RUNNING', args, extra)),
    },
  }
}

function generic(result: string, fullOutputUri = '') {
  return {
    generic: {
      result: {
        result,
        ...(fullOutputUri
          ? { fullOutputUri }
          : {}),
      },
    },
  }
}

const READ = {
  viewFile: {
    absolutePathUri: 'file:///p/a.ts',
    content: 'Showing lines 1-2 of 2\n1: alpha\n2: beta',
    numLines: 2,
    numBytes: 16,
  },
}
const EDIT = {
  codeAction: {
    actionSpec: {
      command: {
        isEdit: true,
        file: { absoluteUri: 'file:///p/a.ts' },
      },
    },
    actionResult: {
      edit: {
        diff: {
          unifiedDiff: {
            lines: [
              { text: 'alpha', type: 'UNIFIED_DIFF_LINE_TYPE_DELETE' },
              { text: 'omega', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
            ],
          },
        },
      },
    },
  },
}
const MCP = {
  mcpTool: {
    serverName: 'probe',
    toolCall: { id: 'call-mcp', name: 'inspect', argumentsJson: '{}' },
    resultString: 'NATIVE_MCP_RESULT',
  },
}

const ANTIGRAVITY_FIXTURES = {
  [ANTIGRAVITY_TOOL.ReadFile]: done(ANTIGRAVITY_TOOL.ReadFile, { Path: '/p/a.ts' }, READ),
  [ANTIGRAVITY_TOOL.ViewFile]: done(ANTIGRAVITY_TOOL.ViewFile, { Path: '/p/a.ts' }, READ),
  [ANTIGRAVITY_TOOL.WriteFile]: done(ANTIGRAVITY_TOOL.WriteFile, { Path: '/p/new.ts', Content: 'fresh\n', Overwrite: true }, {}),
  [ANTIGRAVITY_TOOL.EditFile]: done(ANTIGRAVITY_TOOL.EditFile, { Path: '/p/a.ts', Target: 'alpha', Replacement: 'proposed' }, EDIT),
  [ANTIGRAVITY_TOOL.RunCommand]: done(ANTIGRAVITY_TOOL.RunCommand, { CommandLine: 'echo probe', WaitMsBeforeAsync: 1000, toolSummary: 'probe' }, {
    runCommand: {
      commandLine: 'echo probe',
      cwd: '/p',
      exitCode: 0,
      combinedOutput: { full: 'probe\n' },
    },
  }),
  [ANTIGRAVITY_TOOL.SearchWeb]: done(ANTIGRAVITY_TOOL.SearchWeb, { query: 'LeapMux', domain: 'leapmux.dev' }, generic('Native search result')),
  [ANTIGRAVITY_TOOL.InvokeSubagent]: done(ANTIGRAVITY_TOOL.InvokeSubagent, {
    TypeName: 'research',
    Role: 'Researcher',
    Prompt: 'Inspect the source',
    Model: 'inherit',
  }, {
    invokeSubagent: {
      subagents: [
        { typeName: 'research', role: 'Researcher' },
      ],
      results: [
        { conversationId: 'child-cascade' },
      ],
    },
  }),
  [ANTIGRAVITY_TOOL.ManageSubagents]: done(ANTIGRAVITY_TOOL.ManageSubagents, { operation: 'list' }, generic('Subagents listed')),
  [ANTIGRAVITY_TOOL.ManageTask]: done(ANTIGRAVITY_TOOL.ManageTask, { operation: 'status', task_id: 'task-1' }, generic('Task running')),
  [ANTIGRAVITY_TOOL.SendMessage]: done(ANTIGRAVITY_TOOL.SendMessage, { Recipient: 'child-cascade', Message: 'Continue' }, generic('Message sent')),
  [ANTIGRAVITY_TOOL.Schedule]: done(ANTIGRAVITY_TOOL.Schedule, { schedule: '* * * * *', prompt: 'Check' }, generic('Schedule created')),
  [ANTIGRAVITY_TOOL.AskQuestion]: done(ANTIGRAVITY_TOOL.AskQuestion, {
    questions: [
      {
        question: 'Which?',
        options: [
          { id: 'a', text: 'A' },
        ],
        is_multi_select: false,
      },
    ],
  }, generic('A')),
  [ANTIGRAVITY_TOOL.CallMcpTool]: done(ANTIGRAVITY_TOOL.CallMcpTool, { ServerName: 'probe', ToolName: 'inspect', Arguments: {} }, MCP),
  [ANTIGRAVITY_TOOL.ListResources]: done(ANTIGRAVITY_TOOL.ListResources, { ServerName: 'probe' }, MCP),
  [ANTIGRAVITY_TOOL.ReadResource]: done(ANTIGRAVITY_TOOL.ReadResource, { ServerName: 'probe', Uri: 'file:///p/a.ts' }, MCP),
  [ANTIGRAVITY_TOOL.DefineSubagent]: done(ANTIGRAVITY_TOOL.DefineSubagent, { Name: 'researcher', Description: 'Research', SystemPrompt: 'Inspect' }, generic('Subagent defined')),
}

function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  const fixture = (ANTIGRAVITY_FIXTURES as Record<string, ToolResultFixture | undefined>)[name]
  const args = fixture?.payload.metadata as Record<string, unknown> | undefined
  const toolCall = args?.toolCall as Record<string, unknown> | undefined
  const input = toolCall?.argumentsJson
  return {
    payload: step(name, 'CORTEX_STEP_STATUS_ERROR', input
      ? JSON.parse(String(input)) as Record<string, unknown>
      : {}, generic('Native failure')),
    ...(fixture?.options !== undefined
      ? { options: fixture.options }
      : {}),
    kind,
    name,
    status,
  }
}

export const ANTIGRAVITY_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.ANTIGRAVITY_CLI,
  fixtures: ANTIGRAVITY_FIXTURES,
  failures: [
    failed('execute', ANTIGRAVITY_TOOL.RunCommand),
    failed('read', ANTIGRAVITY_TOOL.ReadFile),
    failed('write', ANTIGRAVITY_TOOL.WriteFile),
    {
      ...failed('write', ANTIGRAVITY_TOOL.WriteFile, 'declined'),
      payload: step(ANTIGRAVITY_TOOL.WriteFile, 'CORTEX_STEP_STATUS_ERROR', { Path: '/p/new.ts', Content: 'fresh\n' }, {
        error: { userErrorMessage: 'Encountered error in tool execution: user denied permission for write_file(/p/new.ts)' },
      }),
    },
    failed('edit', ANTIGRAVITY_TOOL.EditFile),
    failed('web_search', ANTIGRAVITY_TOOL.SearchWeb),
    failed('agent', ANTIGRAVITY_TOOL.InvokeSubagent),
    failed('agents', ANTIGRAVITY_TOOL.ManageSubagents),
    failed('task', ANTIGRAVITY_TOOL.ManageTask),
    failed('message', ANTIGRAVITY_TOOL.SendMessage),
    failed('trigger', ANTIGRAVITY_TOOL.Schedule),
    failed('question', ANTIGRAVITY_TOOL.AskQuestion),
    failed('mcp', ANTIGRAVITY_TOOL.CallMcpTool),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    [ANTIGRAVITY_TOOL.SearchWeb]: 'The web-search result is native text, not a typed link list.',
    [ANTIGRAVITY_TOOL.ManageSubagents]: 'The management result is native text with no neutral agent records.',
    [ANTIGRAVITY_TOOL.ManageTask]: 'The task result is native text rather than a typed task outcome.',
    [ANTIGRAVITY_TOOL.SendMessage]: 'The delivery result is native text.',
    [ANTIGRAVITY_TOOL.Schedule]: 'The schedule result is native text.',
    [ANTIGRAVITY_TOOL.AskQuestion]: 'The native answer is text rather than a neutral answer list.',
    [ANTIGRAVITY_TOOL.DefineSubagent]: 'The definition result is native text rather than a child execution.',
  },
}

export const ANTIGRAVITY_GENERIC_TOOLS = {}
