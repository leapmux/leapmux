/**
 * The kinds Claude reads differently from the shared table, in one place.
 *
 * This leaf module imports the row type alone. It keeps Claude-specific request
 * extraction in the provider layer and keeps the model validator provider-neutral.
 */

import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { ClaudeRowContext, ClaudeToolRow } from './toolCommon'
import { clipFirstLine } from '~/lib/clipFirstLine'
import { pickBoolean, pickNumber, pickString } from '~/lib/jsonPick'
import { parseMcpToolName } from '../../../model/mcpToolCall'
import { MESSAGE_PREVIEW_LIMIT } from '../../../model/tools/message'
import { DEFAULT_TOOL_REQUESTS, toolRequestFor } from '../../defaultToolRequests'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeFileEditChanges } from './fileEdit'
import { claudeQuestions } from './question'
import { claudeTriggerRequest } from './remoteTrigger'
import { claudeTodoRequest } from './todo'

/**
 * Everything one Claude request reads beyond the arguments the call sent.
 *
 * Three fields, and each one answers a kind whose arguments cannot. The tool NAME
 * carries a field of its own for five kinds, because Claude states no `action` and no
 * `language` argument. The paired result and the row's surroundings answer the `Task*`
 * half of the todo kind alone, whose row draws the task its ANSWER states.
 *
 * There is no `finished` fact here, and Claude needs none. It states a result in a
 * separate `user` message, so an absent result row means that no result exists --
 * unlike the providers whose last frame carries partial output, where the two states
 * read the same and a builder must be told which one it has.
 */
export interface ClaudeToolFacts {
  /** The tool name, after `canonicalClaudeToolName` folded its aliases. */
  toolName: string
  /** The paired RESULT row, or undefined while the call runs. */
  result: ClaudeToolRow | undefined
  /** What the row reads beyond its own bytes: the paired payload and the task snapshot. */
  context: ClaudeRowContext
}

/**
 * The kinds Claude reads differently, and the fact each one reads.
 *
 * This object IS Claude's deviation list. Every other kind takes
 * {@link DEFAULT_TOOL_REQUESTS}, so a reader answers "what does Claude read differently
 * from every other provider?" from these keys alone.
 *
 * PARTIAL, and never a copy of the shared table. No type can refuse an entry that
 * shadows a shared entry identically, because a function taking the arguments alone
 * satisfies a slot that supplies the arguments and the facts --
 * `toolRequests.test.ts` pins the key set for that reason.
 *
 * EVERY entry declares its own return type, and the annotation is load-bearing.
 * `ToolRequestOverrides` supplies a contextual signature, which is not an annotated
 * position: TypeScript infers the arrow's return type from the literal it returns, so
 * the object loses its freshness before any property is checked and a key no renderer
 * reads rides into the model. `toolTableEntriesAreAnnotated.test.ts` keeps every entry in
 * this form.
 */
export const CLAUDE_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<ClaudeToolFacts> = {
  // The subagent TYPE, which Claude spells `subagent_type`. The shared entry declares no
  // such field, and it supplies the description and the prompt that ride beside it.
  agent: (input): ToolRequestByKind['agent'] => {
    const agentType = pickString(input, 'subagent_type')
    return {
      ...DEFAULT_TOOL_REQUESTS.agent(input),
      ...(agentType ? { agentType } : {}),
    }
  },

  // The TEAM that `TeamCreate` and `TeamDelete` act on, which is not a roster filter at
  // all: the shared entry declares no team, so both calls drew the header "List agents"
  // and the team's own name appeared nowhere on the row. The two filters are TRIMMED
  // here, which the shared entry does not do, because a filter of spaces is no filter.
  agents: (input): ToolRequestByKind['agents'] => {
    const team = pickString(input, 'team_name').trim()
    if (team)
      return { team: { name: team } }
    const channel = pickString(input, 'channel').trim()
    const query = pickString(input, 'q').trim()
    return {
      ...(channel ? { channel } : {}),
      ...(query ? { query } : {}),
    }
  },

  // The change the ARGUMENTS ask for: one entry per substitution, one for a write. The
  // shared entry states an empty list, because no other provider's arguments describe a
  // diff.
  edit: (input, facts): ToolRequestByKind['edit'] => ({
    changes: claudeFileEditChanges(input, facts.toolName, facts.result),
    ...(input.replace_all === true ? { replaceAll: true } : {}),
  }),
  write: (input, facts): ToolRequestByKind['write'] => ({
    changes: claudeFileEditChanges(input, facts.toolName, facts.result),
    ...(input.replace_all === true ? { replaceAll: true } : {}),
  }),

  // The LANGUAGE, which the tool NAME states: `PowerShell` runs its command through a
  // different shell, and the row draws that word. The shared entry sees the arguments
  // alone, and it supplies the command and the description but states no language.
  execute: (input, facts): ToolRequestByKind['execute'] => {
    const shared = DEFAULT_TOOL_REQUESTS.execute(input)
    return facts.toolName === CLAUDE_TOOL_NAMES.POWERSHELL
      ? { ...shared, language: 'powershell' }
      : shared
  },

  // The SERVER whose resources a listing asks for, which is not a file path. The shared
  // entry reads a path and answers `.` for a call that carries none, which states a
  // directory nobody listed.
  list: (input): ToolRequestByKind['list'] => ({ path: pickString(input, 'server') || 'resources' }),

  // The SERVER and the TOOL, which Claude spells inside the tool NAME. The shared entry
  // reads two arguments no Claude call carries, so both fields were empty.
  mcp: (input, facts): ToolRequestByKind['mcp'] => {
    const identity = parseMcpToolName(facts.toolName)
    return { server: identity?.server ?? '', tool: identity?.tool ?? facts.toolName, args: input }
  },

  // The one-line SUMMARY the model writes for a STRUCTURED message, which is the only
  // one-line form such a message has. The shared entry reads `text` and then `message`,
  // and neither answers a record, so a structured message left the row with no words at
  // all. The addressee is TRIMMED here for the reason the agents entry gives.
  message: (input): ToolRequestByKind['message'] => {
    const message = input.message
    const to = pickString(input, 'to').trim()
    const summary = pickString(input, 'summary')
    return {
      ...(to ? { to } : {}),
      text: typeof message === 'string' ? message : clipFirstLine(summary, MESSAGE_PREVIEW_LIMIT),
      ...(summary ? { summary } : {}),
    }
  },

  // The parsed QUESTIONS. The shared entry states an empty list, because the shape of a
  // question is each provider's own.
  question: (input): ToolRequestByKind['question'] => ({ questions: claudeQuestions(input) }),

  // The free-text ARGUMENT STRING a `Skill` call takes beside the name. The shared entry
  // prettifies the whole arguments record as JSON, which draws the skill's own name back
  // to the reader as one of its arguments.
  skill: (input): ToolRequestByKind['skill'] => {
    const name = pickString(input, 'skill')
    const args = pickString(input, 'args')
    return {
      ...(name ? { name } : {}),
      ...(args ? { args } : {}),
    }
  },

  // The mode the TOOL NAME states. The shared entry reads a `mode` argument that no
  // Claude call carries, so every switch stated no mode at all.
  switch_mode: (input, facts): ToolRequestByKind['switch_mode'] => {
    // `mode` is where the session IS after the switch, which is what separates entering
    // a worktree from leaving one: both used to state `worktree`, so the two rows drew
    // the identical title.
    const target = pickString(input, 'name')
    if (facts.toolName === CLAUDE_TOOL_NAMES.ENTER_WORKTREE)
      return { mode: 'worktree', ...(target ? { target } : {}) }
    if (facts.toolName === CLAUDE_TOOL_NAMES.EXIT_WORKTREE)
      return { mode: 'default', ...(target ? { target } : {}) }
    // The two plan brackets word their own row TITLE instead. `switchModeRenderer` titles
    // the row from `request.mode` FIRST and reads that title only when the request states
    // no mode, so they must state none.
    return {}
  },

  // The ACTION, which the TOOL NAME states, and the timeout and the blocking flag a
  // Claude task call sends beside the id. The shared entry answers `other` for every
  // call and declares neither of those two. `shell_id` is Claude's second spelling of
  // the id, from the tools that answer to a `Bash`-era alias.
  task: (input, facts): ToolRequestByKind['task'] => {
    const taskId = pickString(input, 'task_id') || pickString(input, 'shell_id')
    const timeoutMs = pickNumber(input, 'timeout', undefined)
    const block = pickBoolean(input, 'block', undefined)
    return {
      action: facts.toolName === CLAUDE_TOOL_NAMES.TASK_OUTPUT ? 'output' : 'stop',
      // Each optional half rides only when the call sent it.
      ...(taskId ? { taskId } : {}),
      ...(timeoutMs !== undefined ? { timeoutMs } : {}),
      ...(block !== undefined ? { block } : {}),
    }
  },

  // The list `TodoWrite` asks to save, or the SINGLE task a `Task*` call acts on. The
  // shared entry states an empty list for the reason the question entry gives.
  todo: (input, facts): ToolRequestByKind['todo'] => claudeTodoRequest(facts.toolName, input, facts.result, facts.context),

  // The ACTION, which Claude states in an `action` ARGUMENT, and the label one level down
  // under `body`. The shared entry supplies the id and the schedule.
  trigger: (input): ToolRequestByKind['trigger'] => claudeTriggerRequest(input),

  // How long the call waited, which `Sleep` spells `durationMs` -- the one camelCase
  // argument in a vocabulary that is snake_case everywhere else. The shared entry answers
  // undefined for every call, because no other provider states a duration in its
  // arguments.
  wait: (input): ToolRequestByKind['wait'] => {
    const durationMs = pickNumber(input, 'durationMs', undefined)
    return { ...(durationMs !== undefined ? { durationMs } : {}) }
  },
}

/** One kind's declared request: Claude's own reading, or the shared table's. */
export function claudeRequestFor<K extends ToolKind>(
  kind: K,
  input: Record<string, unknown>,
  facts: ClaudeToolFacts,
): ToolRequestByKind[K] {
  return toolRequestFor(kind, input, facts, CLAUDE_TOOL_REQUEST_OVERRIDES)
}
