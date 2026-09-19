import type { ToolKind } from '~/components/chat/ir/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'

const CALL = 'kilo-1'

/** A finished Kilo update whose paired request states the tool and the arguments. */
function kiloUpdate(rawInput: Record<string, unknown>, result: Record<string, unknown>, title: string): ToolResultFixture {
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', kind: 'other', ...result },
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, status: 'pending', kind: 'other', title, rawInput }) as ParsedMessageContent,
    },
  }
}

function text(value: string) {
  return [{ type: 'content', content: { type: 'text', text: value } }]
}

const CHART_CONFIG = JSON.stringify({ type: 'bar', data: { labels: ['A'], datasets: [{ label: 'Hits', data: [3] }] } })

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  agent_manager_models: kiloUpdate({}, { content: text('Available models:\n- kilo-coder') }, 'agent_manager_models'),
  background_process: kiloUpdate({ command: 'npm run watch' }, { kind: 'execute', content: text('watching for changes') }, 'background_process'),
  board_post: kiloUpdate({ content: 'Ship the chart kind.' }, { content: text('Saved to the board.') }, 'board_post'),
  board_read: kiloUpdate({}, { content: text('The board is empty.') }, 'board_read'),
  kilo_memory_recall: kiloUpdate({}, { content: text('The reader prefers tabs over spaces.') }, 'kilo_memory_recall'),
  kilo_memory_save: kiloUpdate({ note: 'The reader prefers tabs.' }, { content: text('Saved.') }, 'kilo_memory_save'),
  browser_open: kiloUpdate({ url: 'https://example.com' }, { content: text('# Example page') }, 'browser_open'),
  notebook_edit: kiloUpdate({ path: '/p/nb.ipynb', old_string: 'x', new_string: 'y' }, { kind: 'edit', content: [{ type: 'diff', path: '/p/nb.ipynb', oldText: 'x', newText: 'y' }] }, 'notebook_edit'),
  notebook_execute: kiloUpdate({ code: '1 + 1' }, { kind: 'execute', content: text('2') }, 'notebook_execute'),
  notebook_read: kiloUpdate({ path: '/p/nb.ipynb' }, { kind: 'read', rawOutput: { metadata: { display: { type: 'file', path: '/p/nb.ipynb', text: 'cell one', lineStart: 1, totalLines: 1 } } } }, 'notebook_read'),
  open_plan: kiloUpdate({ path: '/p/plan.md' }, { kind: 'read', rawOutput: { metadata: { display: { type: 'file', path: '/p/plan.md', text: '# Plan', lineStart: 1, totalLines: 1 } } } }, 'open_plan'),
  plan_exit: kiloUpdate({ mode: 'exit' }, { content: text('Back in build mode.') }, 'plan_exit'),
  repo_overview: kiloUpdate({}, { content: text('A frontend and a worker, joined by gRPC.') }, 'repo_overview'),
  semantic_search: kiloUpdate({ query: 'chart kind' }, { kind: 'search', content: text('src/chart.ts states the chart kind.') }, 'semantic_search'),
  notify_user: kiloUpdate({ message: 'The build finished.' }, { content: text('The build finished.') }, 'notify_user'),
  send_file: kiloUpdate({ path: '/p/report.pdf' }, { content: text('File sent.') }, 'send_file'),
  chart: kiloUpdate({ title: 'Weekly hits', spec: CHART_CONFIG }, { content: text(CHART_CONFIG), rawOutput: { metadata: { title: 'Weekly hits' } } }, 'chart'),
  generate_image: kiloUpdate({ prompt: 'a red square' }, { content: [{ type: 'content', content: { type: 'image', data: 'aGk=', mimeType: 'image/png' } }] }, 'generate_image'),
  goal_report: kiloUpdate({}, { content: text('Goal complete: every tool named.') }, 'goal_report'),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. Kilo's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * The FAILED frame of the call one successful fixture already states.
 *
 * It keeps the frame's identity -- the call id and the wire kind, which describe the TOOL and never
 * the outcome -- and replaces the answer with the reason. Everything a successful call
 * left behind is gone: a call that failed computed no `rawOutput`, no diff and no
 * display record.
 *
 * The request half comes from the successful fixture rather than from a second copy of
 * the request. The two frames then describe ONE call, which is what lets the ladder
 * assert that a failure keeps the kind, the tool and the request of its success.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // Every failure pairs by name with a fixture above, so the reads are guarded for the type alone.
  const fixture = FIXTURES[name]
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, kind: fixture?.payload.kind, status: 'failed', content: text(ERROR_TEXT) },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const KILO_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.KILO,
  fixtures: FIXTURES,
  failures: [
    failed('agents', 'agent_manager_models'),
    failed('execute', 'background_process'),
    failed('memory', 'board_post'),
    failed('fetch', 'browser_open'),
    failed('edit', 'notebook_edit'),
    failed('read', 'notebook_read'),
    failed('switch_mode', 'plan_exit'),
    failed('list', 'repo_overview'),
    failed('search', 'semantic_search'),
    failed('message', 'notify_user'),
    failed('chart', 'chart'),
    failed('image', 'generate_image'),
    failed('report', 'goal_report'),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    repo_overview: 'An overview is prose about the tree, not the directory listing the list kind draws.',
    semantic_search: 'Ranked prose about the code, not the file-line list the search body draws.',
  },
}
