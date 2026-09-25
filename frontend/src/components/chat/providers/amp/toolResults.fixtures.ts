import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { AMP_SHELL_TOOL, AMP_SUBAGENT_TOOL } from '~/generated/contracts/amp-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { AMP_TOOL_NAME } from './toolNames'

/** The id every fixture's call carries. Amp spells a call id `TU-` and 22 base62 characters. */
export const AMP_FIXTURE_CALL_ID = 'TU-034UC14fL0WVIuQhmDl0qN'

/** The assistant row that states one call, as the worker cuts it out of Amp's message. */
export function ampToolUseRow(name: string, input: Record<string, unknown>, id = AMP_FIXTURE_CALL_ID): Record<string, unknown> {
  return {
    type: 'assistant',
    message: {
      type: 'message',
      role: 'assistant',
      content: [{ type: 'tool_use', id, name, input }],
      stop_reason: 'tool_use',
      usage: { input_tokens: 0, cache_creation_input_tokens: 100, cache_read_input_tokens: 200, output_tokens: 7 },
    },
    parent_tool_use_id: null,
    session_id: 'T-01a0d1c3-e51a-756b-9279-34c1cd441c35',
  }
}

/** The user row that answers one call. */
export function ampToolResultRow(content: string, isError = false, id = AMP_FIXTURE_CALL_ID): Record<string, unknown> {
  return {
    type: 'user',
    message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: id, content, is_error: isError }] },
    parent_tool_use_id: null,
    session_id: 'T-01a0d1c3-e51a-756b-9279-34c1cd441c35',
  }
}

/** A successful result row, paired with the row that states its call. */
function answered(name: string, content: string, input: Record<string, unknown>): ToolResultFixture {
  return {
    payload: ampToolResultRow(content),
    options: {
      request: {
        wrapper: null,
        topLevel: null,
        parentObject: ampToolUseRow(name, input),
        rawText: '',
        supplementalContent: undefined,
        messageMetadata: undefined,
      },
    },
  }
}

const PATCH = '*** Begin Patch\n*** Update File: /work/a.ts\n@@\n-old\n+new\n*** End Patch'
const UNIFIED = 'Index: /work/a.ts\n===================================================================\n--- /work/a.ts\n+++ /work/a.ts\n@@ -1,1 +1,1 @@\n-old\n+new\n'

/**
 * One successful result for every tool the kind table holds.
 *
 * `shell_command` and `Task` are the results of a probe of the real CLI, with the
 * paths shortened. The others follow the result records in Amp's own tool code:
 * `apply_patch` states `{summary, files}`, `edit_file` states `{diff, lineRange}`, and
 * `Read` states `{absolutePath, content}` with numbered lines.
 */
const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [AMP_SHELL_TOOL.ShellCommand]: answered(AMP_SHELL_TOOL.ShellCommand, '{"output":"README.md\\n","exitCode":0}', { command: 'ls', workdir: '/work' }),
  [AMP_TOOL_NAME.AsyncShellCommand]: answered(AMP_TOOL_NAME.AsyncShellCommand, '{"output":"started\\n","running":true,"pid":4242}', { command: 'npm run dev', workdir: '/work' }),
  [AMP_TOOL_NAME.Bash]: answered(AMP_TOOL_NAME.Bash, '{"output":"probe\\n","exitCode":0}', { cmd: 'echo probe' }),
  [AMP_SHELL_TOOL.ShellCommandStatus]: answered(AMP_SHELL_TOOL.ShellCommandStatus, '{"output":"done\\n","exitCode":0,"running":false,"pid":4242}', { pid: 4242, timeout_ms: 10000 }),
  [AMP_SHELL_TOOL.ShellCommandKill]: answered(AMP_SHELL_TOOL.ShellCommandKill, '{"output":"","exitCode":143,"running":false,"pid":4242}', { pid: 4242 }),
  [AMP_TOOL_NAME.ApplyPatch]: answered(
    AMP_TOOL_NAME.ApplyPatch,
    JSON.stringify({ summary: 'update: /work/a.ts (+1/-1)', files: [{ uri: 'file:///work/a.ts', type: 'update', additions: 1, deletions: 1, diff: UNIFIED }] }),
    { patchText: PATCH },
  ),
  [AMP_TOOL_NAME.EditFile]: answered(
    AMP_TOOL_NAME.EditFile,
    JSON.stringify({ diff: `\`\`\`diff\n${UNIFIED}\`\`\``, lineRange: [1, 1] }),
    { path: '/work/a.ts', old_str: 'old', new_str: 'new' },
  ),
  [AMP_TOOL_NAME.CreateFile]: answered(AMP_TOOL_NAME.CreateFile, 'Successfully created file /work/new.txt', { path: '/work/new.txt', content: 'fresh line\n' }),
  [AMP_TOOL_NAME.DeleteFile]: answered(AMP_TOOL_NAME.DeleteFile, 'Deleted /work/old.txt', { path: '/work/old.txt' }),
  [AMP_TOOL_NAME.Read]: answered(AMP_TOOL_NAME.Read, JSON.stringify({ absolutePath: '/work/notes.txt', content: '1: alpha one\n2: beta two' }), { path: '/work/notes.txt' }),
  [AMP_TOOL_NAME.ViewMedia]: answered(
    AMP_TOOL_NAME.ViewMedia,
    JSON.stringify({ absolutePath: '/work/shot.png', content: 'iVBORw0KGgo=', isImage: true, imageInfo: { mimeType: 'image/png', size: 8 } }),
    { path: '/work/shot.png' },
  ),
  [AMP_TOOL_NAME.Grep]: answered(AMP_TOOL_NAME.Grep, JSON.stringify(['/work/a.ts:3:const alpha = 1']), { pattern: 'alpha', path: '/work' }),
  [AMP_TOOL_NAME.Glob]: answered(AMP_TOOL_NAME.Glob, JSON.stringify(['/work/a.ts', '/work/b.ts']), { filePattern: '**/*.ts' }),
  [AMP_TOOL_NAME.GlobAlias]: answered(AMP_TOOL_NAME.GlobAlias, JSON.stringify(['/work/a.ts']), { filePattern: '*.ts' }),
  [AMP_TOOL_NAME.WebSearch]: answered(AMP_TOOL_NAME.WebSearch, JSON.stringify([{ title: 'Doc', url: 'https://example.com/doc' }]), { objective: 'leapmux docs' }),
  [AMP_TOOL_NAME.ReadWebPage]: answered(AMP_TOOL_NAME.ReadWebPage, '# Example\n\nThe page.', { url: 'https://example.com' }),
  [AMP_TOOL_NAME.Painter]: answered(AMP_TOOL_NAME.Painter, 'Generated one image.', { prompt: 'A blue paper airplane' }),
  [AMP_TOOL_NAME.Skill]: answered(AMP_TOOL_NAME.Skill, 'Loaded the skill.', { name: 'release' }),
  [AMP_TOOL_NAME.Sleep]: answered(AMP_TOOL_NAME.Sleep, 'Slept for 5 seconds.', { duration_ms: 5000 }),
  [AMP_SUBAGENT_TOOL.Task]: answered(AMP_SUBAGENT_TOOL.Task, 'pong', { description: 'Return pong', prompt: 'Reply with exactly the word pong.' }),
  [AMP_SUBAGENT_TOOL.Oracle]: answered(AMP_SUBAGENT_TOOL.Oracle, 'Lock the map before the read.', { task: 'Review the lock order', context: 'The deadlock shows in CI.' }),
  [AMP_SUBAGENT_TOOL.Librarian]: answered(AMP_SUBAGENT_TOOL.Librarian, 'quartz traps timers with Trap().', { query: 'How does quartz trap timers?' }),
  [AMP_SUBAGENT_TOOL.Finder]: answered(AMP_SUBAGENT_TOOL.Finder, 'backend/amp/permission.go', { query: 'where the bridge closes' }),
}

/**
 * The sentence every failed fixture carries. Synthetic on purpose: the guard asks about
 * the ladder -- the outcome word, the brand, the kind and the request -- and never about
 * Amp's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED result of the call one successful fixture already states. */
function failed(kind: ToolKind, name: string, content = ERROR_TEXT, status: ToolFailureFixture['status'] = 'failed', isError = true): ToolFailureFixture {
  const fixture = FIXTURES[name]
  return {
    payload: ampToolResultRow(content, isError),
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const AMP_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.AMP,
  fixtures: FIXTURES,
  failures: [
    failed('execute', AMP_SHELL_TOOL.ShellCommand),
    // A permission rule that rejected the call reaches the stream with `is_error: false`.
    failed('execute', AMP_SHELL_TOOL.ShellCommand, 'Tool rejected by plugin: Matches built-in permissions rule 75: ask shell_command', 'declined', false),
    // The LeapMux helper refused the call with the reader's reason.
    failed('execute', AMP_SHELL_TOOL.ShellCommand, 'Plugin error: Use the clean target instead.\n', 'declined'),
    failed('execute', AMP_SHELL_TOOL.ShellCommand, 'Tool execution cancelled: User cancelled', 'cancelled'),
    failed('task', AMP_SHELL_TOOL.ShellCommandStatus),
    failed('edit', AMP_TOOL_NAME.ApplyPatch),
    failed('edit', AMP_TOOL_NAME.EditFile),
    failed('write', AMP_TOOL_NAME.CreateFile),
    failed('delete', AMP_TOOL_NAME.DeleteFile),
    failed('read', AMP_TOOL_NAME.Read),
    failed('grep', AMP_TOOL_NAME.Grep),
    failed('glob', AMP_TOOL_NAME.Glob),
    failed('web_search', AMP_TOOL_NAME.WebSearch),
    failed('fetch', AMP_TOOL_NAME.ReadWebPage),
    failed('image', AMP_TOOL_NAME.Painter),
    failed('skill', AMP_TOOL_NAME.Skill),
    failed('wait', AMP_TOOL_NAME.Sleep),
    failed('agent', AMP_SUBAGENT_TOOL.Task),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    [AMP_TOOL_NAME.Painter]: 'Amp describes the picture it made in words, and the result states no field the image model holds.',
  },
}
