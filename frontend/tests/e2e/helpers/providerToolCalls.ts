import type { MockModelToolCall } from './mockModelScript'
import { createHash } from 'node:crypto'
import { isAbsolute } from 'node:path'
// Use a relative import.
// The note in ../agentSettings.ts explains standalone native probes.
import { AMP_TOOL_NAME } from '../../../src/components/chat/providers/amp/toolNames'
import { CLINE_TOOL_NAME } from '../../../src/components/chat/providers/cline/toolNames'
import { MUSE_TOOL, MUSE_TOOL_NAMESPACE } from '../../../src/components/chat/providers/muse/toolNames'
import { AMP_SHELL_TOOL, AMP_SUBAGENT_TOOL } from '../../../src/generated/contracts/amp-protocol'
import { CLINE_TOOL } from '../../../src/generated/contracts/cline-protocol'
import { CODEWHALE_TOOL } from '../../../src/generated/contracts/codewhale-protocol'
import { COMMAND_CODE_TOOL } from '../../../src/generated/contracts/commandcode-protocol'
import { COPILOT_TOOL } from '../../../src/generated/contracts/copilot-protocol'
import { DEEPSEEK_HARNESS_TOOL } from '../../../src/generated/contracts/deepseek-harness-protocol'
import { GEMINI_TOOL } from '../../../src/generated/contracts/gemini-protocol'
import { KIMI_TOOL } from '../../../src/generated/contracts/kimi-protocol'
import { LETTA_TOOL } from '../../../src/generated/contracts/letta-protocol'
import { MIMO_ACTOR_ACTION, MIMO_TOOL } from '../../../src/generated/contracts/mimo-protocol'
import { PI_TOOL } from '../../../src/generated/contracts/pi-protocol'
import { QWEN_TOOL } from '../../../src/generated/contracts/qwen-protocol'
import { ZCODE_TOOL } from '../../../src/generated/contracts/zcode-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { escapeRegExp } from '../../../src/lib/regexp'
import {
  CURSOR_CREATE_PLAN_TOOL,
  CURSOR_EXECUTION_TOOLS,
  CURSOR_GENERATE_IMAGE_TOOL,
  CURSOR_MCP_TOOL,
  CURSOR_QUESTION_TOOL,
  CURSOR_REQUEST_CONTEXT_TOOL,
  CURSOR_TASK_TOOL,
  CURSOR_TODO_STATUS_WORDS,
  CURSOR_UPDATE_TODOS_TOOL,
  CURSOR_WEB_FETCH_TOOL,
} from './cursorSurface'
import { quotePosixShellArgument } from './shellArguments'

/**
 * The mock returns the tool calls that each test scripts.
 * Each builder uses its provider's native name and arguments.
 * Native source and captured requests define these builders.
 * A provider that rejects a call fails its native spec because the intended tool result does not arrive.
 * This table supplies one builder vocabulary for the end-to-end (E2E) helpers.
 * The satisfies clause requires an entry for each provider.
 */

export interface EditRequest {
  path: string
  before: string
  after: string
}

export interface WriteRequest {
  path: string
  content: string
}

/** The native file that the Gemini plan scenarios write before approval. */
export const GEMINI_E2E_PLAN_FILENAME = 'leapmux-e2e-plan.md'

/** The installed Claude child tool that delivers its completed report to its caller. */
export const CLAUDE_SUBAGENT_HANDBACK_TOOL = 'SubagentHandback'

/**
 * The agent that the Kiro spawn call starts: the context gatherer, which Kiro bundles and its default mode offers.
 * The registry row and the report of the child state this name, and the request of the child states it as its mode.
 */
export const KIRO_CHILD_AGENT = 'context-gatherer'

/** Build the native Claude report call without changing any report text. */
export function claudeSubagentHandbackToolCall(id: string, message: string): MockModelToolCall {
  return { id, name: CLAUDE_SUBAGENT_HANDBACK_TOOL, arguments: { message } }
}

/** The required native catalog shape from Claude Code 2.1.284. */
export function claudeSubagentHandbackToolDefinition() {
  return {
    name: CLAUDE_SUBAGENT_HANDBACK_TOOL,
    input_schema: {
      type: 'object',
      properties: { message: { type: 'string' } },
      required: ['message'],
      additionalProperties: false,
    },
  }
}

/** A subagent to spawn. */
export interface SubagentRequest {
  /** A short label, 3 to 5 words, which the registry row shows. */
  description: string
  /** The task the child performs. Mark it so the child's turns reach the script. */
  prompt: string
  /** The Junie custom-agent ID; omitted to select its bundled docs child. */
  agentType?: string
  /** Cursor's remote Task report, when the scenario does not request a native child execution. */
  report?: string
  /** Execute an actual Cursor child locally with this native model selection. */
  nativeExecution?: { modelId: string }
  /**
   * Run the child in the background: the spawn call returns before the child ends, and a later notice reports it.
   *
   * A builder whose native spawn call carries a background choice states it on every call, so a changed native
   * default cannot change the test. An absent value takes the default of that builder, which its comment states:
   * the foreground for most of them, and the background where the existing specs of the provider rely on it.
   * A builder refuses a value that its native spawn cannot honor: Letta Code always runs a child in the background,
   * Oh My Pi lets its profile decide, and Cursor's task call carries no background choice. Other provider builders
   * ignore this field.
   */
  background?: boolean
  /** Hold Cursor's native task completion in the mock service after its start event. */
  completionGate?: string
  /** Cursor's actual nested child delta, supplied by the isolated remote service. */
  taskProgress?: string
}

/**
 * A to-do status that the sidebar draws as a state of its own. Each native tool accepts its own subset, and each
 * builder refuses a status that its tool does not accept, so a test cannot send a word that the native tool refuses.
 */
export type TodoStatus = 'pending' | 'in_progress' | 'completed' | 'cancelled' | 'deleted' | 'abandoned' | 'blocked'

/** The three statuses that every native to-do tool of this table accepts. */
const BASE_TODO_STATUSES = ['pending', 'in_progress', 'completed'] as const satisfies readonly TodoStatus[]

/** One step of a to-do list, in the shape every provider that keeps one shares. */
export interface TodoStep {
  step: string
  status: TodoStatus
}

/**
 * The status, when `tool` accepts it. A tool that does not accept the status refuses the whole call, and the
 * test then fails at the call rather than at a sidebar that the native tool never fed.
 */
function acceptedTodoStatus<const Accepted extends readonly TodoStatus[]>(tool: string, accepted: Accepted, status: TodoStatus): Accepted[number] {
  if (!(accepted as readonly TodoStatus[]).includes(status))
    throw new Error(`The native ${tool} to-do tool accepts no status ${status}; it accepts ${accepted.join(', ')}.`)
  return status as Accepted[number]
}

/** The reason of a blocked goal or task, for a native tool that refuses a blocked status with no reason. */
function requireReason(tool: string, reason: string): string {
  if (reason.trim() === '')
    throw new Error(`The native ${tool} call requires a nonempty reason for its blocked status.`)
  return reason
}

/** The status of a task in the Task family of Claude Code, CodeBuddy Code, Letta Code and Command Code. */
export type TaskFamilyStatus = 'pending' | 'in_progress' | 'completed' | 'deleted'

/** Pi's todo extension changes one task per call and returns the whole list. */
export type PiTodoRequest
  = | { action: 'create', subject: string, description?: string, activeForm?: string }
    | { action: 'update', id: number, status: 'pending' | 'in_progress' | 'completed' | 'deleted' }
    | { action: 'clear' }

/** One choice offered by a question. */
export interface QuestionOption {
  label: string
  description: string
  /**
   * The control surface renders this Markdown in the option's own region.
   * A code block receives syntax highlighting.
   * Other content keeps its whitespace, including diagram alignment.
   * Only native question paths with preview support use this field.
   * The builders preserve the full option.
   */
  preview?: string
}

/** One question, in the shape every provider that asks one shares. */
export interface QuestionRequest {
  question: string
  /**
   * A short header label. Claude limits it to 12 characters. Pi limits it to 16.
   */
  header: string
  options: QuestionOption[]
  multiSelect?: boolean
  /**
   * The question invites a typed answer beside its options. Only Codewhale's native question states this
   * (`allow_free_text`), and the other builders ignore it: their native question takes a typed answer, or none, with
   * no flag of its own.
   */
  freeText?: boolean
}

/** One approach a plan offers. The approval surface lists each one as a choice. */
export interface PlanApproachRequest {
  label: string
  description: string
}

/**
 * A null member means this table supplies no builder for that operation.
 * A provider can expose the operation through another native path.
 * requireBuilder rejects an unavailable builder before the request starts.
 */
interface ProviderToolVocabulary {
  bash: ((id: string, command: string) => MockModelToolCall) | null
  edit: ((id: string, request: EditRequest) => MockModelToolCall) | null
  write: ((id: string, request: WriteRequest) => MockModelToolCall) | null
  read: ((id: string, path: string) => MockModelToolCall) | null
  /** Enter plan mode. The provider auto-approves this one. */
  enterPlanMode: ((id: string) => MockModelToolCall) | null
  /** Leave plan mode, which raises the plan for approval. */
  exitPlanMode: ((id: string, plan: string) => MockModelToolCall) | null
  /**
   * Leave plan mode through a provider that reads its plan file.
   * The model writes the plan before the exit call.
   * Kimi Code also offers approaches as choices.
   * Qoder CLI accepts no exit arguments.
   * Each provider identifies its plan file in the native request.
   */
  exitPlanModeFromFile: ((id: string, approaches: PlanApproachRequest[]) => MockModelToolCall) | null
  /** Ask the user to choose, which raises a control request. */
  askUserQuestion: ((id: string, questions: QuestionRequest[]) => MockModelToolCall) | null
  /** Spawn a subagent, which opens a registry row and a child transcript. */
  spawnSubagent: ((id: string, request: SubagentRequest) => MockModelToolCall) | null
  /**
   * Spawn several subagents in ONE native call, which runs them at the same time.
   * A null value means the native spawn tool starts one child for each call; a test then sends one spawn call for each
   * child in one model step.
   */
  spawnSubagentBatch: ((id: string, requests: readonly SubagentRequest[]) => MockModelToolCall) | null
  /**
   * Run a native background command and open its shell row.
   * The shell row has no child agent.
   * The row preserves the native task status.
   * A null value means this table supplies no background command builder.
   */
  backgroundBash: ((id: string, command: string) => MockModelToolCall) | null
  /**
   * Write a native bulk to-do list for the sidebar.
   * A null value means the provider has no bulk list builder here or the tests use another task
   * path.
   * Providers with incremental task operations use their specific task builders.
   */
  updateTodos: ((id: string, steps: TodoStep[]) => MockModelToolCall) | null
  /**
   * Start a native session goal through a model tool.
   * The provider can request approval.
   * A null value means the table supplies no goal-start tool builder.
   */
  createGoal: ((id: string, objective: string) => MockModelToolCall) | null
  /**
   * Mark a native goal completed and stop its goal loop.
   * A null value means the table supplies no completion tool builder.
   */
  completeGoal: ((id: string) => MockModelToolCall) | null
  /**
   * Mark a native goal blocked and stop its goal loop.
   * A null value means the table supplies no blocked-goal tool builder.
   */
  blockGoal: ((id: string, reason: string) => MockModelToolCall) | null
  /**
   * Call a native Model Context Protocol (MCP) tool with the provider's exact shape.
   * A null value means this table supplies no MCP builder.
   */
  mcpTool: ((id: string, request: McpToolRequest) => MockModelToolCall) | null
  /**
   * Run a script through the native code executor of the provider.
   * A null value means the provider has no code executor that the suite audited.
   */
  codeExecution: ((id: string, source: string) => MockModelToolCall) | null
  /**
   * The names of the native tools that run a workflow of several subagents, as the model catalog of the provider
   * lists them. For CodeBuddy, the name is the tool that its deferred wrapper takes. A null value means the suite
   * knows no workflow tool of the provider. {@link WORKFLOW_TOOL_NAMES} is the union of these names.
   */
  workflowTools: readonly string[] | null
}

/**
 * One native Model Context Protocol tool call.
 * The fields identify these parts:
 *
 * - The server.
 * - The tool.
 * - The input.
 */
export interface McpToolRequest {
  server: string
  tool: string
  input: Record<string, unknown>
}

function cursorQuestionToolCall(id: string, questions: QuestionRequest[]): MockModelToolCall {
  if (questions.length === 0)
    throw new Error('Cursor needs at least one question')
  return {
    id,
    name: CURSOR_QUESTION_TOOL,
    arguments: {
      title: questions[0]!.header,
      questions: questions.map((question, questionIndex) => ({
        id: `question-${questionIndex + 1}`,
        prompt: question.question,
        allowMultiple: question.multiSelect ?? false,
        options: question.options.map((option, optionIndex) => ({
          id: `option-${questionIndex + 1}-${optionIndex + 1}`,
          label: option.label,
        })),
      })),
    },
  }
}

/** One item of a Cursor to-do call: the ID that a merge addresses, its text, and its status. */
export interface CursorTodoRequest {
  id: string
  content: string
  status: TodoStatus
}

/** The statuses of Cursor's TodoStatus enum (cursor-agent 2026.09.28: `TODO_STATUS_PENDING` to `TODO_STATUS_CANCELLED`). */
const CURSOR_TODO_STATUSES = Object.keys(CURSOR_TODO_STATUS_WORDS) as readonly (keyof typeof CURSOR_TODO_STATUS_WORDS)[]

/** One item in Cursor's own field names and status words. */
function cursorTodoItem({ id, content, status }: CursorTodoRequest): Record<string, unknown> {
  if (!id)
    throw new Error('A native Cursor to-do item requires a nonempty ID.')
  return { id, content, status: CURSOR_TODO_STATUS_WORDS[acceptedTodoStatus('Cursor updateTodos', CURSOR_TODO_STATUSES, status)] }
}

/**
 * Merge items into Cursor's to-do list: `merge: true` changes each item whose ID the list holds, adds each other item,
 * and keeps every item that the call does not state. The surface writes the flag into UpdateTodosArgs, and the CLI
 * states it again in its `cursor/update_todos` frame. `updateTodosToolCall` numbers its items from 1, so a merge after
 * it addresses them as '1', '2', and so on.
 */
export function cursorMergeTodosToolCall(id: string, items: readonly CursorTodoRequest[]): MockModelToolCall {
  if (items.length === 0)
    throw new Error('A native Cursor to-do merge requires at least one item: Cursor sends no frame for an empty merge.')
  return { id, name: CURSOR_UPDATE_TODOS_TOOL, arguments: { todos: items.map(cursorTodoItem), merge: true } }
}

/**
 * Convert a description to an identifier that the provider accepts.
 * These native fields require an identifier beside the prompt:
 *
 * - Codex task_name.
 * - Copilot name.
 * - Codewhale name, which identifies the child session.
 * - Oh My Pi task name, which becomes the subagent ID.
 *
 * Remove spaces and punctuation from the description.
 */
function identifierFrom(description: string): string {
  const identifier = description.toLowerCase().replaceAll(/[^a-z0-9]+/g, '_').replaceAll(/^_+|_+$/g, '')
  // Codex and Copilot require a nonempty name.
  // An empty description or punctuation alone produces no identifier, which causes the native spawn to fail.
  // The stable fallback keeps the call valid and makes a failed test easy to find.
  return identifier === '' ? 'scripted_subagent' : identifier
}

/**
 * The Markdown checklist that Goose and Copilot read as a whole to-do list. Only an `x` marker reads as completed, and
 * every other marker reads as pending, so the checklist states no other status.
 */
function markdownChecklist(tool: string, steps: readonly TodoStep[]): string {
  return steps.map(step => `- [${acceptedTodoStatus(tool, BASE_TODO_STATUSES, step.status) === 'completed' ? 'x' : ' '}] ${step.step}`).join('\n')
}

/**
 * The statuses of the `todowrite` schema of the OpenCode family. OpenCode 1.18.34 and Kilo 7.8.3 both describe the
 * field as "Current status of the task: pending, in_progress, completed, cancelled".
 */
const OPENCODE_FAMILY_TODO_STATUSES = [...BASE_TODO_STATUSES, 'cancelled'] as const satisfies readonly TodoStatus[]

/**
 * The statuses that Qoder CLI's `WriteTodos` and Gemini CLI's `write_todos` accept. Qoder CLI 1.1.65 states the enum
 * `pending, in_progress, completed, cancelled, blocked` in its schema, and the tool documentation of Gemini CLI 0.62.0
 * states the same five.
 */
const CANCELLED_AND_BLOCKED_TODO_STATUSES = [...BASE_TODO_STATUSES, 'cancelled', 'blocked'] as const satisfies readonly TodoStatus[]
const MUSE_TODO_STATUSES = [...BASE_TODO_STATUSES, 'cancelled'] as const satisfies readonly TodoStatus[]

/** The one phase that `updateTodosToolCall` gives a whole Oh My Pi list. */
const OH_MY_PI_TODO_PHASE = 'Plan'

/**
 * Spawn Oh My Pi children through one `task` call (omp 18.6.0).
 *
 * - Each task runs through the bundled task agent, and its name becomes the subagent ID, so it requires an
 *   identifier. omp refuses two names that differ only in case.
 * - `context` is the one background text of the batch, which omp requires nonempty: the descriptions, one for each
 *   line.
 * - The schema also lists `solutionSpace` for each task. The tool validates leniently and its runtime treats the field
 *   as optional, so the builder keeps each child's prompt to its scripted task.
 * - The call carries no background choice. omp runs each child of the call as a background job while its
 *   `async.enabled` setting is on, and in the call while it is off, as in the E2E profile.
 */
function ohMyPiTaskToolCall(id: string, requests: readonly SubagentRequest[]): MockModelToolCall {
  if (requests.length === 0)
    throw new Error('The native Oh My Pi task call requires at least one task.')
  if (requests.some(request => request.background !== undefined))
    throw new Error('The native Oh My Pi task call carries no background choice; the async.enabled setting of its profile decides.')
  const names = requests.map(request => identifierFrom(request.description))
  if (new Set(names.map(name => name.toLowerCase())).size !== names.length)
    throw new Error(`The native Oh My Pi task names must differ: ${names.join(', ')}.`)
  return {
    id,
    name: 'task',
    arguments: {
      context: requests.map(request => request.description).join('\n'),
      tasks: requests.map((request, index) => ({ name: names[index], agent: 'task', task: request.prompt })),
    },
  }
}

/**
 * Spawn Dirac children through one `use_subagents` call (dirac-cli 0.5.17). The tool starts every child of the call at
 * once and returns when all of them end. Its schema sets no item limit, and an empty list fails the call ("Missing
 * required parameter: subagents"). Each title states the row, in 5 words or 80 characters at most.
 */
function diracSubagentsToolCall(id: string, requests: readonly SubagentRequest[]): MockModelToolCall {
  if (requests.length === 0)
    throw new Error('The native Dirac use_subagents call requires at least one subagent.')
  return {
    id,
    name: 'use_subagents',
    arguments: { subagents: requests.map(({ description, prompt }) => ({ task_title: description, prompt })) },
  }
}

/** OpenCode and Kilo share the `todowrite` schema of their protocol family. */
function openCodeFamilyTodoCall(id: string, steps: TodoStep[]): MockModelToolCall {
  return {
    id,
    name: 'todowrite',
    arguments: {
      todos: steps.map(({ step, status }) => ({
        content: step,
        status: acceptedTodoStatus('OpenCode family todowrite', OPENCODE_FAMILY_TODO_STATUSES, status),
        priority: 'medium',
        activeForm: status === 'in_progress' ? `Working on: ${step}` : step,
      })),
    },
  }
}

/** OpenCode and Kilo share the `question` tool's prompt schema. */
function openCodeFamilyQuestionCall(id: string, questions: QuestionRequest[]): MockModelToolCall {
  return {
    id,
    name: 'question',
    arguments: {
      questions: questions.map(({ question, header, options, multiSelect }) => ({
        question,
        header,
        options: options.map(({ label, description }) => ({ label, description })),
        multiple: multiSelect ?? false,
      })),
    },
  }
}

/**
 * The Codex router requires this namespace for collaboration tools.
 * codex-rs/core/src/tools/router.rs checks the exact string for these calls:
 *
 * - spawn_agent.
 * - send_message.
 * - followup_task.
 *
 * The default functions namespace carries exec without a namespace field.
 */
const CODEX_COLLABORATION_NAMESPACE = 'collaboration'

/** Codex clamps low integer timeouts in its native wait-agent handler. */
export function codexWaitAgentToolCall(id: string, timeoutMs?: number): MockModelToolCall {
  if (timeoutMs !== undefined && !Number.isSafeInteger(timeoutMs))
    throw new Error('The native Codex wait timeout must be a safe integer.')
  return {
    id,
    name: 'wait_agent',
    namespace: CODEX_COLLABORATION_NAMESPACE,
    arguments: timeoutMs === undefined ? {} : { timeout_ms: timeoutMs },
  }
}

/**
 * Codex calls shell tools through exec, an OpenAI custom tool.
 * Its input is JavaScript source.
 * The tools global supplies the nested calls.
 * The text function adds output items to the transcript.
 */
export function codexExecToolCall(id: string, source: string): MockModelToolCall {
  return { id, name: 'exec', input: source }
}

/**
 * Prefix each text line with its apply_patch hunk marker.
 */
function patchLines(mark: '+' | '-', text: string): string {
  return text.split('\n').map(line => `${mark}${line}`).join('\n')
}

/**
 * An apply_patch text that replaces `before` with `after` in one hunk.
 *
 * Codex, Amp, and Copilot read the same patch format. Each line of a side carries its
 * mark, because an unmarked line is not part of the hunk.
 */
function updateFilePatch({ path, before, after }: EditRequest): string {
  return `*** Begin Patch\n*** Update File: ${path}\n@@\n${patchLines('-', before)}\n${patchLines('+', after)}\n*** End Patch`
}

/** An apply_patch text that creates the file at `path` with `content`. */
function addFilePatch({ path, content }: WriteRequest): string {
  const lines = content === '' ? [] : content.split('\n')
  if (content.endsWith('\n'))
    lines.pop()
  const added = lines.map(line => `+${line}`).join('\n')
  return `*** Begin Patch\n*** Add File: ${path}\n${added ? `${added}\n` : ''}*** End Patch`
}

/**
 * The largest `timeout_ms` that Amp's shell_command takes. Amp's tool description states
 * "valid values are 0 through 60000 milliseconds" and a default of 10000.
 */
export const AMP_SHELL_WAIT_LIMIT_MS = 60_000

/**
 * Read a command that Amp's shell_command moved to the background, by the PID of its result. Amp waits up to
 * `timeoutMs` for the command to end, streams its new output meanwhile, and answers with the same record as
 * shell_command: `running`, `pid`, the output since the last read, and the `exitCode` once the command ended. Amp
 * 0.0.1791074829 states a wait from 0 to 60000 milliseconds and a default of 10000.
 */
export function ampShellCommandStatusToolCall(id: string, pid: number, timeoutMs?: number): MockModelToolCall {
  if (!Number.isSafeInteger(pid) || pid <= 0)
    throw new RangeError('The native Amp shell_command_status call requires the positive PID of a command.')
  if (timeoutMs !== undefined && (!Number.isSafeInteger(timeoutMs) || timeoutMs < 0 || timeoutMs > AMP_SHELL_WAIT_LIMIT_MS))
    throw new RangeError(`The native Amp shell_command_status wait must be an integer from 0 to ${AMP_SHELL_WAIT_LIMIT_MS} milliseconds.`)
  return { id, name: AMP_SHELL_TOOL.ShellCommandStatus, arguments: { pid, ...(timeoutMs === undefined ? {} : { timeout_ms: timeoutMs }) } }
}

function codexCommandCall(id: string, request: { cmd: string, sandbox_permissions?: string, justification?: string }): MockModelToolCall {
  return codexExecToolCall(id, `const result = await tools.exec_command(${JSON.stringify(request)})\ntext(JSON.stringify(result))`)
}

function codexApplyPatch(id: string, patch: string): MockModelToolCall {
  // Report the result through text.
  // Without text, the exec cell returns empty output for both successful and refused patches.
  return codexExecToolCall(
    id,
    `const result = await tools.apply_patch(${JSON.stringify(patch)})\ntext(typeof result === 'string' ? result : JSON.stringify(result))`,
  )
}

/**
 * Mark the Codex goal through update_goal, which takes one status word and no other field (codex-rs `ext/goal`,
 * 0.160.0). The E2E model `gpt-5.6-luna` runs in `code_mode_only`, which offers update_goal only inside exec, as it
 * offers update_plan. The result reports through text, so a refused status shows in the output of the cell.
 */
function codexUpdateGoalToolCall(id: string, status: 'complete' | 'blocked'): MockModelToolCall {
  return codexExecToolCall(id, `const result = await tools.update_goal(${JSON.stringify({ status })})\ntext(JSON.stringify(result))`)
}

const TOOL_VOCABULARY = {
  [AgentProvider.CLAUDE_CODE]: {
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'Edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'Write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: 'EnterPlanMode', arguments: {} }),
    exitPlanMode: (id, plan) => ({ id, name: 'ExitPlanMode', arguments: { plan } }),
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => ({ id, name: 'AskUserQuestion', arguments: { questions: questions.map(withMultiSelect) } }),
    // Claude Code 2.1.289 in stream-JSON mode offers `run_in_background`, and a child runs in the background when the
    // call omits it ("Agents run in the background by default"). The E2E children report through a completion
    // notification ("Async agent launched successfully."), so the default here is that native default.
    spawnSubagent: (id, { description, prompt, background }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose', run_in_background: background ?? true } }),
    spawnSubagentBatch: null,
    // The native Bash tool uses run_in_background to detach the command.
    // The CLI description states: "You can use the `run_in_background` parameter to run the command in the background."
    backgroundBash: (id, command) => ({
      id,
      name: 'Bash',
      arguments: { command, description: 'Run the scripted command in the background', run_in_background: true },
    }),
    // TodoWrite replaces the whole list on every call, and it accepts pending, in_progress and completed.
    //
    // Claude Code 2.1.289 offers no to-do tool for the E2E model (`sonnet` resolves to claude-sonnet-5-5, outside the
    // list of models that get one). With `CLAUDE_CODE_ENABLE_TODO_TOOLS=1` it offers the Task family
    // (`claudeTaskCreateToolCall`), and with `CLAUDE_CODE_ENABLE_TASKS=false` also it offers TodoWrite instead. A
    // native probe of the model request of each setting showed this.
    updateTodos: (id, steps) => ({
      id,
      name: 'TodoWrite',
      arguments: {
        todos: steps.map(step => ({
          content: step.step,
          status: acceptedTodoStatus('Claude Code TodoWrite', BASE_TODO_STATUSES, step.status),
          activeForm: step.status === 'in_progress' ? `Working on: ${step.step}` : step.step,
        })),
      },
    }),
    createGoal: null,
    // No model tool ends a Claude Code goal. A separate evaluator request decides it after each turn: it asks whether
    // the stopping condition holds and reads `{"ok": boolean, "reason": string, "impossible"?: boolean}` (2.1.289).
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: ['Workflow'],
  },
  [AgentProvider.CODEWHALE]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    // The edit tool accepts a replacement list.
    // Each replacement matches the original file.
    // The path resolves against the workspace.
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, edits: [{ oldText: before, newText: after }] } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { path } }),
    // Plan mode is a thread setting, and no tool enters or leaves it.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // The runtime defers request_user_input.
    // The first call returns its schema and asks no question.
    // Script the second call to raise the question.
    // Each question requires an ID that the answer repeats.
    // The native multi_select field controls multiple choices.
    // The native allow_free_text field states that the question invites a typed answer. Codewhale 0.10.0 offers the
    // typed "Other" answer on every question all the same: the field is a hint to the model, and no gate.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'request_user_input',
      arguments: {
        questions: questions.map((question, index) => ({
          id: `question_${index + 1}`,
          header: question.header,
          question: question.question,
          options: question.options.map(({ label, description }) => ({ label, description })),
          allow_free_text: question.freeText ?? false,
          multi_select: question.multiSelect ?? false,
        })),
      },
    }),
    // The agent tool handles each subagent action.
    // Its start action returns the child ID before the child completes.
    // The name field identifies the child session and requires an identifier.
    // The explore role permits reads only.
    spawnSubagent: (id, { description, prompt, background }) => ({
      id,
      name: 'agent',
      arguments: { action: 'start', name: identifierFrom(description), type: 'explore', prompt, detached: background ?? false },
    }),
    spawnSubagentBatch: null,
    // The bash tool refuses a background argument.
    // Use the deferred task_shell_start tool for a background command.
    // The first call loads its schema.
    // Script the second call to start the command.
    backgroundBash: (id, command) => ({ id, name: 'task_shell_start', arguments: { command } }),
    // `todo_write` replaces the whole list, and its rows say `content` where the
    // shared shape says `step`. The schema of the 0.10.1 source also states `cancelled`; no probe of the installed
    // 0.10.0 confirmed it, so the builder takes the three statuses alone.
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_write',
      arguments: { todos: steps.map(step => ({ content: step.step, status: acceptedTodoStatus('Codewhale todo_write', BASE_TODO_STATUSES, step.status) })) },
    }),
    // A goal is a thread setting that LeapMux writes, so the model creates none.
    createGoal: null,
    // The native update_goal tool ends the goal loop.
    // The complete status requires a verification receipt that this scripted turn cannot supply.
    // The test uses blocked to end the loop.
    completeGoal: null,
    blockGoal: (id, reason) => ({ id, name: 'update_goal', arguments: { status: 'blocked', blocker: reason } }),
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp_${server}_${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: CODEWHALE_TOOL.ExecuteTools, arguments: { code: source } }),
    workflowTools: [CODEWHALE_TOOL.Workflow],
  },
  [AgentProvider.CODEX]: {
    bash: (id, command) => codexCommandCall(id, { cmd: command }),
    edit: (id, request) => codexApplyPatch(id, updateFilePatch(request)),
    write: (id, request) => codexApplyPatch(id, addFilePatch(request)),
    read: (id, path) => codexCommandCall(id, { cmd: `cat ${quotePosixShellArgument(path)}` }),
    // Codex drives plan mode through a session mode, not a tool.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => ({
      id,
      name: 'request_user_input',
      arguments: {
        questions: questions.map((question, index) => ({
          id: `question_${index + 1}`,
          header: question.header,
          question: question.question,
          options: question.options.map(({ label, description }) => ({ label, description })),
        })),
      },
    }),
    // Send a function call with JSON arguments and the collaboration namespace.
    // An exec call to tools.spawn_agent creates no child and reports no result.
    // A bare spawn_agent function call returns "unsupported call: spawn_agent" in the tool output.
    // Both paths let the model continue without a child registry row.
    // The native task_name schema accepts lowercase letters, digits, and underscores.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'spawn_agent',
      namespace: CODEX_COLLABORATION_NAMESPACE,
      arguments: { task_name: identifierFrom(description), message: prompt },
    }),
    spawnSubagentBatch: null,
    // Codex calls update_plan through exec to update the to-do list.
    // The native argument type lives in codex-rs/protocol/src/plan_tool.rs.
    // The session mode controls plan mode.
    backgroundBash: null,
    updateTodos: (id, steps) => codexExecToolCall(
      id,
      `await tools.update_plan({ plan: ${JSON.stringify(steps.map(step => ({ step: step.step, status: acceptedTodoStatus('Codex update_plan', BASE_TODO_STATUSES, step.status) })))} })`,
    ),
    // LeapMux sets a Codex goal through the side band (`thread/goal/set`), not through the model's create_goal.
    createGoal: null,
    completeGoal: id => codexUpdateGoalToolCall(id, 'complete'),
    // update_goal states no reason, so the builder drops the one that its caller holds.
    blockGoal: id => codexUpdateGoalToolCall(id, 'blocked'),
    mcpTool: (id, { server, tool, input }) => ({
      id,
      name: tool,
      namespace: `mcp__${server}`,
      arguments: input,
    }),
    codeExecution: (id, source) => codexExecToolCall(id, source),
    workflowTools: null,
  },
  [AgentProvider.GITHUB_COPILOT]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command, description: 'Run the scripted command' } }),
    // Copilot changes files through its freeform apply_patch tool, which reads the patch grammar of Codex and Amp.
    edit: (id, request) => copilotApplyPatchToolCall(id, updateFilePatch(request)),
    write: (id, request) => copilotApplyPatchToolCall(id, addFilePatch(request)),
    read: (id, path) => ({ id, name: 'view', arguments: { path } }),
    // Copilot drives plan mode through its session-mode option group.
    enterPlanMode: null,
    exitPlanMode: (id, plan) => ({
      id,
      name: 'exit_plan_mode',
      arguments: {
        summary: plan,
        actions: ['autopilot', 'interactive', 'exit_only'],
        recommendedAction: 'interactive',
      },
    }),
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => {
      const question = questions[0]
      if (questions.length !== 1 || !question || question.multiSelect)
        throw new Error('Copilot ask_user accepts exactly one single-choice question')
      return {
        id,
        name: 'ask_user',
        arguments: { question: question.question, choices: question.options.map(option => option.label) },
      }
    },
    // The native task tool requires four fields:
    //
    // - agent_type.
    // - name.
    // - description.
    // - prompt.
    //
    // The native agent_type enum includes explore.
    // The name field becomes the child ID and requires an identifier.
    // The CLI reports "Invalid input: \"description\": Required" when description is absent.
    // Omit mode to keep the child in the foreground.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'task',
      arguments: { agent_type: 'explore', name: identifierFrom(description), description, prompt },
    }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    // Copilot update_todo accepts a Markdown checklist in todos.
    // A case-insensitive x marker means completed.
    // Every other marker means pending.
    updateTodos: (id, steps) => ({
      id,
      name: 'update_todo',
      arguments: {
        todos: markdownChecklist('Copilot update_todo', steps),
      },
    }),
    createGoal: null,
    // The session goal is the autopilot objective, and the model ends its task through task_complete, whose one field
    // is a summary (Copilot CLI 1.0.87). An independent reviewer then decides the outcome: completed, continue, or
    // blocked. The model can state no blocked goal.
    completeGoal: id => ({ id, name: COPILOT_TOOL.TaskComplete, arguments: { summary: 'The scripted goal is complete.' } }),
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `${server}-${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: null,
  },
  [AgentProvider.CURSOR]: {
    // Cursor receives protobuf events through its Connect Run stream.
    // The mock uses provider-owned codecs for each supported call.
    bash: (id, command) => ({ id, name: CURSOR_EXECUTION_TOOLS.bash, arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: CURSOR_EXECUTION_TOOLS.edit, arguments: { path, before, after } }),
    write: (id, { path, content }) => ({ id, name: CURSOR_EXECUTION_TOOLS.write, arguments: { path, content } }),
    read: (id, path) => ({ id, name: CURSOR_EXECUTION_TOOLS.read, arguments: { path } }),
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: cursorQuestionToolCall,
    // The Run surface executes a native child when nativeExecution exists.
    // The remote Task report path remains available for service-only scenarios.
    // Cursor's TaskArgs carries no background choice (cursor-agent 2026.09.28): the service states it in the result,
    // TaskSuccess.is_background, which the mock wire (./cursorWire.ts) does not write. The builder therefore refuses a
    // background choice.
    spawnSubagent: (id, { description, prompt, report, completionGate, taskProgress, nativeExecution, background }) => {
      if (background !== undefined)
        throw new Error('Cursor\'s task call carries no background choice; its service states it in TaskSuccess.is_background.')
      return {
        id,
        name: CURSOR_TASK_TOOL,
        arguments: { description, prompt, report: report ?? '' },
        ...(completionGate !== undefined ? { completionGate } : {}),
        ...(taskProgress !== undefined ? { taskProgress } : {}),
        ...(nativeExecution !== undefined ? { nativeExecution } : {}),
      }
    },
    spawnSubagentBatch: null,
    backgroundBash: null,
    // Cursor's `updateTodos` carries `todos` as a list. Its own status words
    // (`TODO_STATUS_IN_PROGRESS`) fold onto the neutral ones the sidebar draws.
    // `merge: false` replaces the list, and each item states the ID that a later merge
    // (`cursorMergeTodosToolCall`) addresses: its position, from 1.
    updateTodos: (id, steps) => ({
      id,
      name: CURSOR_UPDATE_TODOS_TOOL,
      arguments: {
        todos: steps.map((step, index) => cursorTodoItem({ id: String(index + 1), content: step.step, status: step.status })),
        merge: false,
      },
    }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({
      id,
      name: CURSOR_MCP_TOOL,
      arguments: { server, tool, input },
    }),
    codeExecution: null,
    workflowTools: null,
  },
  [AgentProvider.GOOSE]: {
    bash: (id, command) => ({ id, name: 'shell', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, before, after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: null,
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'delegate', arguments: { instructions: prompt, description } }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    // The Goose todo extension accepts a Markdown checklist, such as - [x] done.
    // Its extractor reads the completed and pending states from that checklist.
    updateTodos: (id, steps) => ({
      id,
      name: 'todo__todo_write',
      arguments: {
        content: markdownChecklist('Goose todo_write', steps),
      },
    }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `${server}__${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'execute_typescript', arguments: { code: source } }),
    workflowTools: null,
  },
  [AgentProvider.KIMI_CODE]: {
    // Kimi Code supplies each tool's JSON Schema in its model request.
    // The schemas set additionalProperties=false.
    // An extra argument causes the native call to fail.
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'Edit', arguments: { path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'Write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'Read', arguments: { path } }),
    enterPlanMode: id => ({ id, name: 'EnterPlanMode', arguments: {} }),
    // Kimi's `ExitPlanMode` states no plan. It raises the plan file, so the
    // operation above cannot carry the plan that its caller passes.
    exitPlanMode: null,
    // `options` takes 1 to 3 approaches, and Kimi offers them as choices only
    // for 2 or more.
    exitPlanModeFromFile: (id, approaches) => ({
      id,
      name: 'ExitPlanMode',
      arguments: { options: approaches.map(({ label, description }) => ({ label, description })) },
    }),
    // Use the native multi_select field.
    // An option accepts only its label and description.
    // The schema refuses the preview field that Claude accepts.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'AskUserQuestion',
      arguments: {
        questions: questions.map(({ question, header, options, multiSelect }) => ({
          question,
          header,
          options: options.map(({ label, description }) => ({ label, description })),
          multi_select: multiSelect ?? false,
        })),
      },
    }),
    // `coder` is the default type, stated so a change of default cannot change
    // the child. `explore` would prefix the prompt with a git context.
    // Kimi Code 2.1.1 takes `run_in_background`, and a child runs in the foreground when the call omits it. A background
    // child needs the TaskList, TaskOutput and TaskStop tools active; without them Kimi refuses the call with
    // BACKGROUND_AGENT_UNAVAILABLE.
    spawnSubagent: (id, { description, prompt, background }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'coder', run_in_background: background ?? false } }),
    spawnSubagentBatch: null,
    // The schema requires `description` when `run_in_background` is true.
    backgroundBash: (id, command) => ({
      id,
      name: 'Bash',
      arguments: { command, description: 'Run the scripted command in the background', run_in_background: true },
    }),
    // The status vocabulary is `pending`, `in_progress` and `done`.
    updateTodos: (id, steps) => ({
      id,
      name: 'TodoList',
      arguments: {
        todos: steps.map((step) => {
          const status = acceptedTodoStatus('Kimi Code TodoList', BASE_TODO_STATUSES, step.status)
          return { title: step.step, status: status === 'completed' ? 'done' : status }
        }),
      },
    }),
    // Outside Never Ask, the server raises a goal-start approval for this call.
    createGoal: (id, objective) => ({ id, name: 'CreateGoal', arguments: { objective } }),
    // `status` takes `active`, `complete` or `blocked`.
    completeGoal: id => ({ id, name: 'UpdateGoal', arguments: { status: 'complete' } }),
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: [KIMI_TOOL.AgentSwarm],
  },
  [AgentProvider.KILO]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { filePath: path, oldString: before, newString: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { filePath: path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { filePath: path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: openCodeFamilyQuestionCall,
    // The native task schema requires subagent_type beside description and prompt.
    // task.ts reads that field to resolve the child and reports "Unknown agent type" when it identifies no agent.
    // The general agent is built in through agent/agent.ts.
    // Without that field, the child does not spawn and its transcript stays empty.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'task',
      arguments: { description, prompt, subagent_type: 'general' },
    }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    updateTodos: openCodeFamilyTodoCall,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `${server}_${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'execute', arguments: { code: source } }),
    workflowTools: null,
  },
  // MiMo Code 0.1.15 supplies snake_case input fields in its native schemas.
  // Two tool names differ from OpenCode:
  //
  // - task updates the to-do list.
  // - actor starts a subagent.
  [AgentProvider.MIMO_CODE]: {
    // `description` is REQUIRED by the schema, and MiMo refuses a call without it.
    bash: (id, command) => ({ id, name: 'bash', arguments: { command, description: 'Run the scripted command' } }),
    // MiMo refuses an edit of a file that the session did not read first, so a spec
    // scripts a read of the file before the edit.
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { file_path: path } }),
    // Plan mode is a primary agent that a prompt runs on, not a tool.
    enterPlanMode: null,
    // `plan_exit` takes no argument. The plan is a file that the plan agent writes
    // at a path the session picks, so the call cannot carry the plan text.
    exitPlanMode: id => ({ id, name: 'plan_exit', arguments: {} }),
    exitPlanModeFromFile: null,
    // The schema spells a multi-select question `multiple`, and an option takes
    // `label` and `description` alone.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'question',
      arguments: {
        questions: questions.map(({ question, header, options, multiSelect }) => ({
          question,
          header,
          options: options.map(({ label, description }) => ({ label, description })),
          multiple: multiSelect ?? false,
        })),
      },
    }),
    // The run action waits until the subagent reports.
    // The parent's next ordered model turn receives that report.
    // The spawn action, MiMo's own default, returns before the child completes.
    // Its later `<actor-notification>` reaches the parent as a user message, which requires a matching script rule.
    // The builder takes run unless the caller asks for the background.
    spawnSubagent: (id, { description, prompt, background }) => ({
      id,
      name: 'actor',
      arguments: { operation: { action: background === true ? 'spawn' : 'run', subagent_type: 'general', description, prompt } },
    }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    // The to-do tool acts on ONE item for each call, so it cannot write a list in
    // one call. `mimoTaskToolCall` below states each operation.
    updateTodos: null,
    // MiMo's goal is a session setting that the user writes, not a model tool.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `${server}_${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'exec', arguments: { code: source } }),
    workflowTools: [MIMO_TOOL.Workflow],
  },
  [AgentProvider.OPENCODE]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { filePath: path, oldString: before, newString: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { filePath: path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { filePath: path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: openCodeFamilyQuestionCall,
    // The native tool/task.ts schema requires subagent_type as a Schema.String beside description and prompt.
    // The general agent is built in through agent/agent.ts.
    // Without that field, a registry row can appear but its child runs no turn.
    // Kilo shares this task schema.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'task',
      arguments: { description, prompt, subagent_type: 'general' },
    }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    updateTodos: openCodeFamilyTodoCall,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `${server}_${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'execute', arguments: { code: source } }),
    workflowTools: null,
  },
  [AgentProvider.PI]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, edits: [{ oldText: before, newText: after }] } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { path } }),
    askUserQuestion: (id, questions) => ({ id, name: 'ask_user_question', arguments: { questions: questions.map(withMultiSelect) } }),
    // The session mode selects plan mode.
    // No model tool enters it.
    // The plan_mode_complete tool leaves plan mode and carries the plan text.
    // The native implementation is pi-plan-mode/src/plan-mode.ts; pi-plan-mode/test/saved-plan.test.ts checks saved plans.
    // The extension refuses the call outside plan mode.
    // Open the agent in plan mode before that scripted call.
    enterPlanMode: null,
    exitPlanMode: (id, plan) => ({ id, name: 'plan_mode_complete', arguments: { plan } }),
    exitPlanModeFromFile: null,
    // The `Agent` tool of @tintinweb/pi-subagents 0.19.0 runs a child in the background when the call omits
    // `run_in_background` (`backgroundByDefault`), and with `false` the call blocks and returns the child's output.
    spawnSubagent: (id, { description, prompt, background }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose', run_in_background: background ?? true } }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    updateTodos: null,
    // pi-goal-x 0.31.9 ends a goal through update_goal. It offers the tool only while a goal is active, paused or
    // budget-limited. `complete` runs its completion audit first, and `blocked` requires the reason at run time.
    createGoal: (id, objective) => ({ id, name: 'create_goal', arguments: { objective } }),
    completeGoal: id => ({ id, name: 'update_goal', arguments: { status: 'complete' } }),
    blockGoal: (id, reason) => ({ id, name: 'update_goal', arguments: { status: 'blocked', reason: requireReason('Pi update_goal', reason) } }),
    mcpTool: (id, { server, tool, input }) => ({ id, name: piNativeMcpToolName(server, tool), arguments: input }),
    codeExecution: (id, source) => piCodemodeToolCall(id, source),
    workflowTools: [PI_TOOL.SubagentWorkflow],
  },
  [AgentProvider.GROK_BUILD]: {
    // Read off the tool schemas in Grok Build 1.0.41's own model request:
    // `description` is REQUIRED beside `command`.
    bash: (id, command) => ({ id, name: 'run_terminal_command', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'search_replace', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'read_file', arguments: { target_file: path } }),
    enterPlanMode: id => ({ id, name: 'enter_plan_mode', arguments: {} }),
    // The native exit_plan_mode tool accepts no arguments.
    // Grok reads the Markdown plan file when the model calls it.
    // The plan argument from the shared helper does not become the approval text.
    exitPlanMode: id => ({ id, name: 'exit_plan_mode', arguments: {} }),
    exitPlanModeFromFile: null,
    // Grok spells the flag `multi_select` in the schema the model sees, and
    // offers no header chip.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'ask_user_question',
      arguments: { questions: questions.map(({ question, options, multiSelect }) => ({ question, options, multi_select: multiSelect ?? false })) },
    }),
    spawnSubagent: (id, { description, prompt, background }) => ({ id, name: 'spawn_subagent', arguments: { prompt, description, background: background ?? false } }),
    spawnSubagentBatch: null,
    backgroundBash: (id, command) => ({ id, name: 'run_terminal_command', arguments: { command, description: 'Run the scripted command in the background', background: true } }),
    // `merge: false` replaces the list, so the scripted steps are the whole list.
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_write',
      arguments: { merge: false, todos: steps.map((step, index) => ({ id: String(index + 1), content: step.step, status: acceptedTodoStatus('Grok Build todo_write', BASE_TODO_STATUSES, step.status) })) },
    }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: 'use_tool', arguments: { tool_name: `${server}__${tool}`, tool_input: input } }),
    codeExecution: null,
    workflowTools: ['workflow'],
  },
  [AgentProvider.QWEN_CODE]: {
    // Read off the tool schemas in Qwen Code 0.24.4's own model request.
    bash: (id, command) => ({ id, name: 'run_shell_command', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write_file', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'read_file', arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: 'enter_plan_mode', arguments: {} }),
    exitPlanMode: (id, plan) => ({ id, name: 'exit_plan_mode', arguments: { plan } }),
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => ({ id, name: 'ask_user_question', arguments: { questions: questions.map(withMultiSelect) } }),
    // Qwen 0.24 defaults to a background agent run.
    // Set run_in_background explicitly to select the intended path.
    spawnSubagent: (id, { description, prompt, background }) => ({
      id,
      name: 'agent',
      arguments: { description, prompt, subagent_type: 'general-purpose', run_in_background: background ?? false },
    }),
    spawnSubagentBatch: null,
    backgroundBash: (id, command) => ({ id, name: 'run_shell_command', arguments: { command, description: 'Run the scripted command in the background', is_background: true } }),
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_write',
      arguments: { todos: steps.map((step, index) => ({ id: String(index + 1), content: step.step, status: acceptedTodoStatus('Qwen Code todo_write', BASE_TODO_STATUSES, step.status) })) },
    }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'exec', arguments: { source } }),
    workflowTools: [QWEN_TOOL.Workflow],
  },
  // Kiro's own tool names and argument shapes, read off the requests of its v3
  // engine (`v3_*` probes): the file tools take `path` and `text`, and the
  // replacement takes `oldStr` and `newStr`.
  [AgentProvider.KIRO]: {
    bash: (id, command) => ({ id, name: 'execute_bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'str_replace', arguments: { path, oldStr: before, newStr: after } }),
    write: (id, { path, content }) => ({ id, name: 'fs_write', arguments: { path, text: content } }),
    read: (id, path) => ({ id, name: 'read_file', arguments: { path } }),
    // Kiro enters plan mode through its mode, not through a tool. Its plan mode
    // leaves through `switch_to_execution`, which raises no approval: see
    // `kiroSwitchToExecutionToolCall`.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // The native user_input tool asks one question.
    // Only a spec mode offers that tool.
    // Each option carries a title.
    // The tool has no header and no flag for multiple selections.
    // The builder omits the header and refuses a multiSelect request.
    askUserQuestion: (id, questions) => {
      const [question, ...rest] = questions
      if (!question || rest.length > 0)
        throw new Error('Kiro\'s user_input asks exactly one question')
      if (question.multiSelect)
        throw new Error('Kiro\'s user_input has no multi-select question')
      return {
        id,
        name: 'user_input',
        arguments: { question: question.question, options: question.options.map(({ label, description }) => ({ title: label, description })), reason: 'general-question' },
      }
    },
    // `name` states an agent that Kiro bundles, and `explanation` is the reason the
    // row states.
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'invoke_sub_agent', arguments: { name: KIRO_CHILD_AGENT, prompt, explanation: description } }),
    spawnSubagentBatch: null,
    // Kiro's Control Process tool starts a background process.
    // No native probe identified its model-facing name.
    // This table supplies no builder because no verified call shape exists here.
    backgroundBash: null,
    // The create command starts each task in the open state.
    // Kiro has no state for a task in progress.
    // A later complete command marks the task completed.
    // The create builder does not use the shared statuses.
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_list',
      arguments: { command: 'create', tasks: steps.map(({ step }) => ({ task_description: step })), task_list_description: 'The scripted task list' },
    }),
    // A goal is Kiro's `/goal` command, which the user sends; no model tool starts one.
    createGoal: null,
    // A step of the goal workflow reports success through `send_message`, which ends
    // the goal's loop.
    completeGoal: id => ({ id, name: 'send_message', arguments: { message: 'The goal is verified.', severity: 'success' } }),
    // An error from send_message fails the goal workflow step.
    // That error also fails the round and the workflow run.
    // Kiro reports the failed run and blocks the goal.
    blockGoal: (id, reason) => ({ id, name: 'send_message', arguments: { message: reason, severity: 'error' } }),
    // Kiro offers each tool of a server to the model as `mcp_<server>_<tool>`, with
    // the tool's own arguments.
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp_${server}_${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: null,
  },
  // Oh My Pi supplies these tool schemas in tools/*.ts.
  // Native probes use omp 18.2.11.
  // The isolated E2E profile selects edit.mode=replace.
  // That edit mode accepts the old text and replacement text.
  // The default hashline mode requires a hash from a prior native file read.
  // The fixed scripted edit cannot compute that hash.
  [AgentProvider.OH_MY_PI]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { path } }),
    // omp's plan mode is a terminal feature; RPC reaches no plan-mode tool.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // `ask` takes an id for each question and `multi` for a multi-select.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'ask',
      arguments: {
        questions: questions.map((question, index) => ({
          id: `q${index + 1}`,
          question: question.question,
          header: question.header,
          options: question.options,
          ...(question.multiSelect ? { multi: true } : {}),
        })),
      },
    }),
    // The task tool accepts a task list, and one call with several tasks runs them at the same time.
    spawnSubagent: (id, request) => ohMyPiTaskToolCall(id, [request]),
    spawnSubagentBatch: ohMyPiTaskToolCall,
    // The `async` flag of bash backgrounds the command (omp 18.6.0), and a later `async-result` message reports its end.
    // omp offers the flag only while its `async.enabled` setting is on, and the E2E profile turns that setting off:
    // the call needs a profile that turns it on.
    backgroundBash: (id, command) => ({ id, name: 'bash', arguments: { command, async: true } }),
    // The todo init command creates a list with phases.
    // Oh My Pi sets the first task to in_progress.
    // The init schema accepts no status field, so the builder cannot select the initial statuses.
    // `ohMyPiTodoToolCall` states each later change of status.
    updateTodos: (id, steps) => ({ id, name: 'todo', arguments: { op: 'init', list: [{ phase: OH_MY_PI_TODO_PHASE, items: steps.map(step => step.step) }] } }),
    // omp starts goal mode from its terminal UI only; RPC offers no goal tool.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}_${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'eval', arguments: { language: 'js', code: source } }),
    workflowTools: null,
  },
  [AgentProvider.REASONIX]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit_file', arguments: { path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write_file', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read_file', arguments: { path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: null,
    // The native read_only_task schema lives in internal/agent/task.go.
    // It requires prompt.
    // Its description is the 3-to-7-word label that the dispatch line shows.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'read_only_task',
      arguments: { prompt, description },
    }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    // `todo_write` states the whole list as `todos`.
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_write',
      arguments: {
        todos: steps.map(({ step, status }) => ({
          content: step,
          status: acceptedTodoStatus('Reasonix todo_write', BASE_TODO_STATUSES, status),
          activeForm: status === 'in_progress' ? `Working on: ${step}` : step,
        })),
      },
    }),
    // A goal is the objective of a Goal-mode turn, so the model creates none. update_goal of Reasonix 1.38.7 takes
    // `continue`, `complete` or `blocked`, and it refuses `blocked` with no reason (the captured catalog in
    // ../reasonix/toolCatalog.fixtures.ts).
    createGoal: null,
    completeGoal: id => ({ id, name: 'update_goal', arguments: { status: 'complete' } }),
    blockGoal: (id, reason) => ({ id, name: 'update_goal', arguments: { status: 'blocked', reason: requireReason('Reasonix update_goal', reason) } }),
    mcpTool: (id, { server, tool, input }) => ({ id, name: 'use_capability', arguments: { action: 'call', capability_id: `mcp-tool:${server}/${tool}`, arguments: input } }),
    codeExecution: null,
    workflowTools: null,
  },
  [AgentProvider.ZCODE]: {
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'Edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'Write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: 'EnterPlanMode', arguments: {} }),
    exitPlanMode: (id, plan) => ({ id, name: 'ExitPlanMode', arguments: { plan } }),
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => ({ id, name: 'AskUserQuestion', arguments: { questions: questions.map(withMultiSelect) } }),
    // ZCode 3.14.4's Agent takes `run_in_background`, and a child runs in the foreground when the call omits it.
    spawnSubagent: (id, { description, prompt, background }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose', run_in_background: background ?? false } }),
    spawnSubagentBatch: null,
    // The SAME Bash tool, with the `run_in_background` flag of its runtime schema. ZCode refuses the flag in an
    // off-peak turn ("Idle-time tasks do not support background commands").
    backgroundBash: (id, command) => ({
      id,
      name: 'Bash',
      arguments: { command, description: 'Run the scripted command in the background', run_in_background: true },
    }),
    // ZCode 3.14.4's `TodoWrite` states the whole list as `todos`. Each item REQUIRES `priority` beside `content` and
    // `status`, and the tool parses the call with that schema before it stores the list. The schema has no
    // `activeForm`, so the tool would drop one.
    updateTodos: (id, steps) => ({
      id,
      name: 'TodoWrite',
      arguments: {
        todos: steps.map(({ step, status }) => ({
          content: step,
          status: acceptedTodoStatus('ZCode TodoWrite', BASE_TODO_STATUSES, status),
          priority: 'medium',
        })),
      },
    }),
    // No model tool ends a ZCode goal. After each goal turn a separate verification request decides it, and it reads
    // `{"passed": boolean, "reason": string, "nextAction": string}` (3.14.4).
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'mcp__node_repl__js', arguments: { code: source, title: 'Run the native script' } }),
    workflowTools: [ZCODE_TOOL.CreateWorkflow],
  },
  // Amp's tool code and native probes define these calls.
  // The mock Amp service leases each non-subagent tool to the CLI executor.
  // Those calls execute in the real agent directory.
  // The LeapMux permission helper handles their approvals.
  [AgentProvider.AMP]: {
    // Without timeout_ms, Amp waits 10 seconds. It then returns `running: true` and a PID,
    // and the turn continues while the command runs on. A command that a scenario holds
    // must stay in its call until the scenario releases it, so the call asks for Amp's
    // largest wait. A quick command still returns when it exits.
    bash: (id, command) => ({ id, name: AMP_SHELL_TOOL.ShellCommand, arguments: { command, timeout_ms: AMP_SHELL_WAIT_LIMIT_MS } }),
    // Amp's agent modes edit through one `*** Begin Patch` text, the format that
    // Codex reads also. The path is absolute, as Amp's own edits state it.
    edit: (id, request) => ({ id, name: AMP_TOOL_NAME.ApplyPatch, arguments: { patchText: updateFilePatch(request) } }),
    write: (id, request) => ({ id, name: AMP_TOOL_NAME.ApplyPatch, arguments: { patchText: addFilePatch(request) } }),
    read: (id, path) => ({ id, name: AMP_TOOL_NAME.Read, arguments: { path } }),
    // Amp has no plan mode, and the worker disables its question tool: a question
    // in stream-JSON mode ends the session.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: null,
    // `Task` runs on Amp's server. The mock's Amp surface answers the child's turn
    // from the scenario its prompt marks.
    spawnSubagent: (id, { description, prompt }) => ({ id, name: AMP_SUBAGENT_TOOL.Task, arguments: { description, prompt } }),
    spawnSubagentBatch: null,
    // Amp moves the command to the background after timeout_ms.
    // `ampShellCommandStatusToolCall` reads a backgrounded command again by its PID.
    // A zero wait can return before Amp records the spawned process.
    // Use one second so the native result identifies the process.
    backgroundBash: (id, command) => ({ id, name: AMP_SHELL_TOOL.ShellCommand, arguments: { command, timeout_ms: 1_000 } }),
    // Amp's agent modes have no to-do tool, and Amp has no session goal.
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: null,
  },
  // Cline's own tool names and argument shapes, read off the tool schemas in the
  // model requests of Cline 3.0.64's hub. Each schema sets `additionalProperties:
  // false`, so an extra field fails the call.
  [AgentProvider.CLINE]: {
    bash: (id, command) => ({ id, name: CLINE_TOOL.RunCommands, arguments: { commands: [command] } }),
    // One `editor` tool replaces text, inserts it, or creates a missing file. The
    // path is absolute, as the schema asks.
    edit: (id, { path, before, after }) => ({ id, name: CLINE_TOOL_NAME.Editor, arguments: { path, old_text: before, new_text: after } }),
    write: (id, { path, content }) => ({ id, name: CLINE_TOOL_NAME.Editor, arguments: { path, new_text: content } }),
    read: (id, path) => ({ id, name: CLINE_TOOL_NAME.ReadFiles, arguments: { files: [{ path }] } }),
    // Plan mode is a session mode, and no tool enters it.
    enterPlanMode: null,
    // The native switch_to_act_mode tool accepts no arguments.
    // Its approval displays the model answer that precedes the call.
    // The shared helper's plan argument does not become that answer.
    // The Worker offers this tool only in Plan mode.
    exitPlanMode: id => ({ id, name: CLINE_TOOL.SwitchToActMode, arguments: {} }),
    exitPlanModeFromFile: null,
    // The native ask_question tool asks one question with 2 to 5 options.
    // Each option is a label.
    // The tool has no header or multiSelect field.
    // The builder refuses a request that the schema cannot represent.
    askUserQuestion: (id, questions) => {
      const [question, ...rest] = questions
      if (!question || rest.length > 0)
        throw new Error('Cline\'s ask_question asks exactly one question')
      if (question.multiSelect)
        throw new Error('Cline\'s ask_question has no multi-select question')
      if (question.options.length < 2 || question.options.length > 5)
        throw new Error('Cline\'s ask_question takes 2 to 5 options')
      return { id, name: CLINE_TOOL.AskQuestion, arguments: { question: question.question, options: question.options.map(({ label }) => label) } }
    },
    // The native spawn_agent tool accepts a system prompt and task.
    // It accepts no separate label.
    // LeapMux uses the task's first line as the row title.
    // Place the description before the marked prompt in the task.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: CLINE_TOOL.SpawnAgent,
      arguments: { systemPrompt: 'You are a subagent. Do the task, then report the result.', task: `${description}\n\n${prompt}` },
    }),
    spawnSubagentBatch: null,
    // Cline's commands run in the foreground of their call.
    backgroundBash: null,
    // Cline has no to-do list and no session goal.
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: [CLINE_TOOL_NAME.TeamRunTask],
  },
  // These providers expose their own native tool vocabularies.
  // Each implemented entry follows its installed tool schema.
  [AgentProvider.CODEBUDDY]: {
    // CodeBuddy uses Claude Code 2.1.220 tool-call shapes.
    // Each call uses an Anthropic tool_use block.
    // Its permission reply differs: CodeBuddy uses allowed where Claude uses behavior.
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'Edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'Write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: 'EnterPlanMode', arguments: {} }),
    exitPlanMode: (id, plan) => ({ id, name: 'ExitPlanMode', arguments: { plan } }),
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => ({ id, name: 'AskUserQuestion', arguments: { questions: questions.map(withMultiSelect) } }),
    // CodeBuddy Code 2.160.0's Agent takes `run_in_background` unless CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS is set,
    // which the E2E environment does not set. A child runs in the foreground when the call omits the flag.
    spawnSubagent: (id, { description, prompt, background }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose', run_in_background: background ?? false } }),
    spawnSubagentBatch: null,
    backgroundBash: (id, command) => ({
      id,
      name: 'Bash',
      arguments: { command, description: 'Run the scripted command in the background', run_in_background: true },
    }),
    // `TodoWrite` replaces the whole list on every call. Its items take
    // `content`, a required `activeForm` and `status`; the CLI reads the
    // `newTodos` half as the list to save. `codebuddyTaskUpdateToolCall` states
    // the `deleted` status of the Task family.
    updateTodos: (id, steps) => ({
      id,
      name: 'TodoWrite',
      arguments: {
        oldTodos: [],
        newTodos: steps.map(({ step, status }) => ({ content: step, activeForm: step, status: acceptedTodoStatus('CodeBuddy Code TodoWrite', BASE_TODO_STATUSES, status) })),
      },
    }),
    // `CreateGoal` takes the goal as `condition`, not `objective`.
    createGoal: (id, objective) => ({ id, name: 'CreateGoal', arguments: { condition: objective } }),
    // `UpdateGoal` REPLACES the condition; there is no complete or block verb.
    // `/goal clear` is the user's route and has no tool form.
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: 'DeferExecuteTool', arguments: { toolName: 'REPL', params: { code: source } } }),
    workflowTools: ['Workflow'],
  },
  [AgentProvider.JUNIE]: {
    // Junie's tools are OpenAI function calls. `bash` takes the command; the
    // tool description states that each call is wrapped in `bash -c`.
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    // `search_replace` is Junie's single-hunk edit: `search` is the block to
    // find, `replace` its replacement.
    edit: (id, { path, before, after }) => ({ id, name: 'search_replace', arguments: { file_path: path, search: before, replace: after } }),
    // `create` writes a new file; the path key is `filename`.
    write: (id, { path, content }) => ({ id, name: 'create', arguments: { filename: path, content } }),
    // `open_entire_file` reads a file in full.
    read: (id, path) => ({ id, name: 'open_entire_file', arguments: { path } }),
    // Plan mode is a config option (`mode: plan`), not a tool: the `plan`
    // slash command is a `setConfigOption` action.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // Junie sends ask_user through Agent Client Protocol (ACP) as a permission request.
    // Each question requires name and question.
    // Each option requires a short title and a description sentence.
    // The native choices become the control options.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'ask_user',
      arguments: {
        questions: questions.map(question => ({
          name: question.header,
          question: question.question,
          options: question.options.map(({ label, description }) => ({ title: label, description })),
          allowMultiple: question.multiSelect ?? false,
        })),
      },
    }),
    // The spawn_subagent tool waits for the child result.
    // Its required agent field identifies the child kind.
    // Its name field labels fresh built-in agents.
    // Custom agents ignore name and use their definition's native title.
    // Its required task field supplies the work.
    spawnSubagent: (id, { description, prompt, agentType }) => ({
      id,
      name: 'spawn_subagent',
      arguments: { agent: agentType ?? 'junie-cli-docs', name: description, task: prompt },
    }),
    spawnSubagentBatch: null,
    // The SAME `bash` tool, with the `background` flag the tool description
    // documents for long-running processes.
    backgroundBash: (id, command) => ({ id, name: 'bash', arguments: { command, background: true } }),
    // No to-do tool: the plan entries serve that role.
    updateTodos: null,
    // Goals are a CLI flag (`--goal`), not a tool.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    // Junie offers each MCP tool with the `mcp_` prefix.
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp_${server}_${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: null,
  },
  [AgentProvider.LETTA]: {
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, request) => ({
      id,
      name: 'Edit',
      arguments: { file_path: request.path, old_string: request.before, new_string: request.after },
    }),
    write: (id, request) => ({
      id,
      name: 'Write',
      arguments: { file_path: request.path, content: request.content },
    }),
    read: (id, path) => ({ id, name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // Letta Code 0.34.2 requires a header and, for each option, a label and a description of its own. The schema states
    // no preview, so the preview stays out.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'AskUserQuestion',
      arguments: {
        questions: questions.map(q => ({
          question: q.question,
          header: q.header,
          options: q.options.map(o => ({ label: o.label, description: o.description })),
          multiSelect: q.multiSelect ?? false,
        })),
      },
    }),
    // The model calls Letta's Task tool `Agent`. Its schema states no background choice, because each child runs in the
    // background: "Agents always run in the background" (0.34.2). The builder refuses a foreground request.
    spawnSubagent: (id, { description, prompt, background }) => {
      if (background === false)
        throw new Error('Letta Code always runs a child in the background; its Agent tool takes no foreground choice.')
      return { id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose' } }
    },
    spawnSubagentBatch: null,
    backgroundBash: null,
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
    codeExecution: null,
    workflowTools: [LETTA_TOOL.Workflow],
  },
  [AgentProvider.DIRAC]: {
    // Dirac's native registry supplies these tool schemas.
    // Each schema sets additionalProperties=false.
    // Dirac adds rawInput.tool when it reports a call.
    // Do not send that field as a model argument.
    // If the model sends that field, Dirac returns "Unsupported response parameter: tool".
    //
    // The execute_command tool runs each command in a process of Dirac's own,
    // not in an ACP client terminal: the CLI forces
    // `vscodeTerminalExecutionMode: "backgroundExec"`. Each commands entry
    // supplies one command.
    bash: (id, command) => ({ id, name: 'execute_command', arguments: { commands: [command] } }),
    // The edit_file tool addresses each edit with an ANCHOR§CONTENT coordinate.
    // A prior anchored native read supplies that coordinate.
    // Its random ID belongs to the conversation.
    // The diracEditAnchorCapture helper reads the coordinate from the model request.
    // The before text identifies the target line.
    edit: (id, { path, after }) => ({
      id,
      name: 'edit_file',
      arguments: {
        files: [{
          path,
          edits: [{ edit_type: 'replace', anchor: '{{editAnchor}}', end_anchor: '{{editAnchor}}', text: after }],
        }],
      },
    }),
    // `write_to_file` creates or overwrites a file.
    write: (id, { path, content }) => ({ id, name: 'write_to_file', arguments: { path, content } }),
    // `read_file` reads files, and states `include_anchors` so the result
    // carries the ANCHOR§CONTENT coordinates a later `edit_file` needs.
    read: (id, path) => ({ id, name: 'read_file', arguments: { paths: [path], include_anchors: true } }),
    // The session/set_mode call selects plan mode.
    // The respond plan call ends the model turn.
    // Its approval answer arrives as the next prompt.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // The respond question operation requests an ACP form.
    // Its options field supplies 2 to 5 labels.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'respond',
      arguments: {
        operation: 'question',
        text: questions.map(question => question.question).join('\n\n'),
        options: questions[0]?.options.map(({ label }) => label) ?? [],
      },
    }),
    // The native tool takes one array even when the turn starts one child.
    spawnSubagent: (id, request) => diracSubagentsToolCall(id, [request]),
    spawnSubagentBatch: diracSubagentsToolCall,
    // Dirac has no separate background-shell tool.
    backgroundBash: null,
    // No to-do tool: the `respond plan` entries are the only checklist.
    updateTodos: null,
    // Goals are interactive-CLI only.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    // No MCP client in the current core.
    mcpTool: null,
    codeExecution: (id, source) => ({ id, name: 'execute_command', arguments: { script: source, language: 'node' } }),
    workflowTools: null,
  },
  [AgentProvider.QODER]: {
    // Qoder's stream-json layer emits Anthropic-shaped messages. Its plan exit
    // reads the plan file and accepts no plan argument.
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'Edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'Write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: 'EnterPlanMode', arguments: {} }),
    exitPlanMode: null,
    exitPlanModeFromFile: id => ({ id, name: 'ExitPlanMode', arguments: {} }),
    askUserQuestion: (id, questions) => ({ id, name: 'AskUserQuestion', arguments: { questions: questions.map(withMultiSelect) } }),
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose' } }),
    spawnSubagentBatch: null,
    backgroundBash: (id, command) => ({
      id,
      name: 'Bash',
      arguments: { command, description: 'Run the scripted command in the background', run_in_background: true },
    }),
    // `WriteTodos` replaces the whole list. Its items take `description` and
    // `status` only -- the schema refuses every other key. Qoder CLI 1.1.65's
    // status enum also holds `cancelled` and `blocked`.
    updateTodos: (id, steps) => ({
      id,
      name: 'WriteTodos',
      arguments: { todos: steps.map(({ step, status }) => ({ description: step, status: acceptedTodoStatus('Qoder CLI WriteTodos', CANCELLED_AND_BLOCKED_TODO_STATUSES, status) })) },
    }),
    createGoal: (id, objective) => ({ id, name: 'CreateGoal', arguments: { objective } }),
    // `UpdateGoal` takes one status word: `complete` or `blocked`. Its schema
    // states no reason field, so the builder drops the one its caller holds.
    completeGoal: id => ({ id, name: 'UpdateGoal', arguments: { status: 'complete' } }),
    blockGoal: id => ({ id, name: 'UpdateGoal', arguments: { status: 'blocked' } }),
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: ['Workflow'],
  },
  [AgentProvider.DROID]: {
    bash: (id, command) => droidExecuteToolCall(id, { command, summary: 'Run the scripted command', riskLevel: 'medium' }),
    edit: (id, request) => ({
      id: droidCallId(id),
      name: 'Edit',
      arguments: { file_path: request.path, old_str: request.before, new_str: request.after },
    }),
    write: (id, request) => ({
      id: droidCallId(id),
      name: 'Create',
      arguments: { file_path: request.path, content: request.content },
    }),
    read: (id, path) => ({ id: droidCallId(id), name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: null,
    exitPlanMode: (id, plan) => ({ id: droidCallId(id), name: 'ExitSpecMode', arguments: { plan } }),
    exitPlanModeFromFile: null,
    // Droid's AskUser takes one plain-text `questionnaire`, not a questions
    // array. The format is numbered `[question]` blocks with `[topic]` and
    // `[option]` lines.
    askUserQuestion: (id, questions) => ({
      id: droidCallId(id),
      name: 'AskUser',
      arguments: {
        questionnaire: questions.map((q, i) => [
          `${i + 1}. [question] ${q.question}${q.multiSelect ? ' (multi)' : ''}`,
          '[topic] Question',
          ...q.options.map(o => `[option] ${o.label}`),
        ].join('\n')).join('\n\n'),
      },
    }),
    // `explorer` is a built-in child type. Droid runs it in the background
    // when `await` is false; it does not accept a `background` argument.
    spawnSubagent: (id, { description, prompt, background }) => ({
      id: droidCallId(id),
      name: 'Task',
      arguments: {
        subagent_type: 'explorer',
        description,
        prompt,
        await: background !== true,
      },
    }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    updateTodos: (id, steps) => ({
      id: droidCallId(id),
      name: 'TodoWrite',
      arguments: {
        todos: steps.map(s => ({ content: s.step, status: acceptedTodoStatus('Factory Droid TodoWrite', BASE_TODO_STATUSES, s.status) })),
      },
    }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id: droidCallId(id), name: `${server}___${tool}`, arguments: input }),
    codeExecution: (id, source) => droidScriptToolCall(id, source),
    workflowTools: null,
  },
  [AgentProvider.COMMAND_CODE]: {
    bash: (id, command) => ({ id, name: COMMAND_CODE_TOOL.ShellCommand, arguments: { command, description: 'Run the scripted command.' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit_file', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write_file', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'read_file', arguments: { file_path: path } }),
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: null,
    spawnSubagent: (id, { description, prompt, agentType, background }) => ({ id, name: COMMAND_CODE_TOOL.Agent, arguments: { description, prompt, subagent_type: agentType ?? 'general', run_in_background: background ?? false } }),
    spawnSubagentBatch: null,
    backgroundBash: (id, command) => ({ id, name: COMMAND_CODE_TOOL.ShellCommand, arguments: { command, description: 'Run the scripted background command.', run_in_background: true } }),
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: null,
  },
  [AgentProvider.DEEPSEEK_HARNESS]: {
    bash: (id, command) => ({ id, name: DEEPSEEK_HARNESS_TOOL.Bash, arguments: { command, description: 'Run the scripted command.' } }),
    edit: (id, { path, before, after }) => ({ id, name: DEEPSEEK_HARNESS_TOOL.Edit, arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: DEEPSEEK_HARNESS_TOOL.Write, arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: DEEPSEEK_HARNESS_TOOL.Read, arguments: { file_path: path } }),
    enterPlanMode: null,
    exitPlanMode: (id, plan) => ({ id, name: DEEPSEEK_HARNESS_TOOL.ExitPlanMode, arguments: { plan } }),
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => ({ id, name: DEEPSEEK_HARNESS_TOOL.AskUserQuestion, arguments: { questions: questions.map(({ freeText: _freeText, ...question }, index) => ({ id: `question-${index + 1}`, ...question })) } }),
    spawnSubagent: (id, { description, prompt, background }) => ({ id, name: DEEPSEEK_HARNESS_TOOL.Subagent, arguments: { description, prompt, run_in_background: background ?? true } }),
    spawnSubagentBatch: null,
    backgroundBash: (id, command) => ({ id, name: DEEPSEEK_HARNESS_TOOL.Bash, arguments: { command, description: 'Run the scripted background command.', run_in_background: true } }),
    updateTodos: (id, steps) => ({ id, name: DEEPSEEK_HARNESS_TOOL.TodoWrite, arguments: { todos: steps.map(step => ({ content: step.step, status: acceptedTodoStatus('DeepSeek Harness TodoWrite', BASE_TODO_STATUSES, step.status) })) } }),
    // update_goal requires the goal ID and the revision that get_goal returns, which the operations here cannot carry:
    // `deepseekHarnessUpdateGoalToolCall` states that call.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: (id, source) => ({ id, name: DEEPSEEK_HARNESS_TOOL.Workflow, arguments: { script: source, meta: { name: 'native-code', description: 'Run the scripted native source.' } } }),
    workflowTools: [DEEPSEEK_HARNESS_TOOL.Workflow],
  },
  [AgentProvider.GEMINI_CLI]: {
    bash: (id, command) => ({ id, name: GEMINI_TOOL.RunShellCommand, arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: GEMINI_TOOL.Replace, arguments: { file_path: path, old_string: before, new_string: after, instruction: 'Apply the requested replacement exactly.' } }),
    write: (id, { path, content }) => ({ id, name: GEMINI_TOOL.WriteFile, arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: GEMINI_TOOL.ReadFile, arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: GEMINI_TOOL.EnterPlanMode, arguments: {} }),
    exitPlanMode: null,
    exitPlanModeFromFile: id => geminiPlanApprovalToolCall(id, GEMINI_E2E_PLAN_FILENAME),
    askUserQuestion: null,
    spawnSubagent: (id, { prompt }) => ({ id, name: GEMINI_TOOL.InvokeAgent, arguments: { agent_name: 'generalist', prompt } }),
    spawnSubagentBatch: null,
    backgroundBash: null,
    updateTodos: (id, steps) => ({ id, name: GEMINI_TOOL.TodoWrite, arguments: { todos: steps.map(step => ({ description: step.step, status: acceptedTodoStatus('Gemini CLI write_todos', CANCELLED_AND_BLOCKED_TODO_STATUSES, step.status) })) } }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp_${server}_${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: null,
  },
  [AgentProvider.MUSE_CODE]: {
    bash: (id, command) => ({ id, name: MUSE_TOOL.Bash, namespace: MUSE_TOOL_NAMESPACE, arguments: { command, description: 'Run the scripted command.' } }),
    edit: (id, { path, before, after }) => ({ id, name: MUSE_TOOL.EditFile, namespace: MUSE_TOOL_NAMESPACE, arguments: { path, find: before, replace: after } }),
    write: (id, { path, content }) => ({ id, name: MUSE_TOOL.WriteFile, namespace: MUSE_TOOL_NAMESPACE, arguments: { path, content } }),
    read: (id, path) => ({ id, name: MUSE_TOOL.ReadFile, namespace: MUSE_TOOL_NAMESPACE, arguments: { path } }),
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: (id, questions) => ({ id, name: MUSE_TOOL.RequestUserInput, namespace: MUSE_TOOL_NAMESPACE, arguments: { questions: questions.map((question, index) => ({
      id: `question-${index + 1}`,
      header: question.header,
      question: question.question,
      options: question.options.map(option => ({ label: option.label, description: option.description, ...(option.preview ? { preview: { format: 'markdown', content: option.preview } } : {}) })),
      ...(question.multiSelect ? { selection: { mode: 'multiple', min_selections: 1, max_selections: question.options.length } } : {}),
    })) } }),
    spawnSubagent: (id, { description, prompt, agentType }) => ({ id, name: MUSE_TOOL.SubagentSpawn, namespace: MUSE_TOOL_NAMESPACE, arguments: { command_id: id, role: 'general-purpose', task_name: description, objective: prompt, ...(agentType ? { subagent_type: agentType } : {}) } }),
    spawnSubagentBatch: null,
    backgroundBash: (id, command) => ({ id, name: MUSE_TOOL.Bash, namespace: MUSE_TOOL_NAMESPACE, arguments: { command, description: 'Run the scripted background command.', yield_time_ms: 0 } }),
    updateTodos: (id, steps) => ({ id, name: MUSE_TOOL.WriteTodos, namespace: MUSE_TOOL_NAMESPACE, arguments: { todos: steps.map(step => ({ text: step.step, status: acceptedTodoStatus('Muse Code write_todos', MUSE_TODO_STATUSES, step.status) })) } }),
    createGoal: (id, objective) => ({ id, name: MUSE_TOOL.CreateGoal, namespace: MUSE_TOOL_NAMESPACE, arguments: { objective } }),
    completeGoal: id => ({ id, name: MUSE_TOOL.UpdateGoal, namespace: MUSE_TOOL_NAMESPACE, arguments: { status: 'complete' } }),
    blockGoal: (id, reason) => {
      requireReason('Muse Code update_goal', reason)
      return { id, name: MUSE_TOOL.UpdateGoal, namespace: MUSE_TOOL_NAMESPACE, arguments: { status: 'blocked' } }
    },
    // Muse ignores the response namespace. Its full registered MCP ID selects the server and tool.
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp__${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: [MUSE_TOOL.Workflow],
  },
  [AgentProvider.FAST_AGENT]: {
    // Fast Agent's -x shell runtime supplies these coding tools.
    // Their model names have no namespace prefix.
    // The ACP title can add local/ or environment/ for display.
    // The execute tool runs shell commands.
    // A bash call returns "Tool 'bash' is not available".
    bash: (id, command) => ({ id, name: 'execute', arguments: { command } }),
    // `edit_file` is an exact-string replace; an empty `old_string` creates.
    edit: (id, { path, before, after }) => ({ id, name: 'edit_file', arguments: { path, old_string: before, new_string: after } }),
    // `write_text_file` creates or overwrites a file.
    write: (id, { path, content }) => ({ id, name: 'write_text_file', arguments: { path, content } }),
    // `read_text_file` reads a text file.
    read: (id, path) => ({ id, name: 'read_text_file', arguments: { path } }),
    // There is no plan-approval request. The ACP `plan` update is a to-do
    // display only.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // No questions over ACP: the `__human_input` tool asks through the
    // terminal form of Fast Agent, not through ACP. Under ACP, stdin is not a
    // terminal. The form ends with its default cancel action, and the result
    // reads "The Human cancelled the input request".
    askUserQuestion: null,
    // The `subagent` tool takes `message`; `task` is not in its native schema.
    spawnSubagent: (id, { description, prompt }) => {
      const label = description.trim()
      if (label.length < 1 || label.length > 32 || !/^[\w -]+$/.test(label)
        || !/^[A-Z0-9]$/i.test(label[0] ?? '') || !/^[A-Z0-9]$/i.test(label.at(-1) ?? '')) {
        throw new Error('The native Fast Agent label must contain 1 to 32 ASCII characters. It must start and end with a letter or digit.')
      }
      return { id, name: 'subagent', arguments: { message: prompt, label: description } }
    },
    spawnSubagentBatch: null,
    // The execute schema accepts these fields:
    //
    // - command.
    // - args.
    // - env.
    // - cwd.
    //
    // It accepts no background flag and offers no separate background tool.
    // The current model protocol supplies no detached-command call to script.
    backgroundBash: null,
    // No to-do tool; the `plan` ACP update is a display.
    updateTodos: null,
    // No goal concept in the ACP surface.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    // MCP tool names join the server and tool with two underscores. The ACP
    // title may add a display prefix, but the model sends this native name.
    mcpTool: (id, { server, tool, input }) => ({ id, name: `${server}__${tool}`, arguments: input }),
    codeExecution: null,
    workflowTools: null,
  },
} as const satisfies Record<Exclude<AgentProvider, AgentProvider.UNSPECIFIED>, ProviderToolVocabulary>

function vocabulary(provider: AgentProvider): ProviderToolVocabulary {
  const entry = (TOOL_VOCABULARY as Record<number, ProviderToolVocabulary | undefined>)[provider]
  if (!entry)
    throw new Error(`No tool vocabulary for AgentProvider ${provider}`)
  return entry
}

function requireBuilder<T>(builder: T | null, provider: AgentProvider, operation: string): T {
  if (!builder)
    throw new Error(`AgentProvider ${provider} has no ${operation} builder in this table`)
  return builder
}

/** Keep the full tool identity through Droid's native call_ preservation rule. */
function droidCallId(id: string): string {
  if (!id)
    throw new Error('The native Droid tool call requires a nonempty ID.')
  return id.startsWith('call_') ? id : `call_${id}`
}

/** A shell command, in the provider's own shell tool. */
export function bashToolCall(provider: AgentProvider, id: string, command: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).bash, provider, 'shell')(id, command)
}

/** Supply the native CLI catalog argv from the actual registered agent and server. */
export function lettaMcpCatalogArguments(agentId: string, server: string): string[] {
  if (!agentId || agentId !== agentId.trim() || !/^[\w-]+$/.test(agentId)
    || !server || server !== server.trim() || !/^[\w-]+$/.test(server)) {
    throw new Error('The native Letta MCP catalog requires valid agent and server IDs.')
  }
  return ['mcp', 'tools', server, '--full', '--agent', agentId]
}

/** Capture the actual CLI catalog through Letta's native Bash tool. */
export function lettaMcpCatalogToolCall(id: string, options: {
  agentId: string
  server: string
  executable: string
  nodeExecutable: string
  captureScriptPath: string
  receiptId: string
}): MockModelToolCall {
  if (!id || id !== id.trim() || !/^[\w-]+$/.test(id)
    || !options.receiptId || options.receiptId !== options.receiptId.trim() || !/^[\w-]+$/.test(options.receiptId)
    || ![options.executable, options.nodeExecutable, options.captureScriptPath].every(isAbsolute)) {
    throw new Error('The native Letta MCP capture requires valid identities and absolute executable paths.')
  }
  const args = lettaMcpCatalogArguments(options.agentId, options.server)
  const command = [options.nodeExecutable, options.captureScriptPath, options.receiptId, id, options.executable, ...args]
    .map(quotePosixShellArgument)
    .join(' ')
  return bashToolCall(AgentProvider.LETTA, id, command)
}

/** Invoke a registered Letta MCP tool through the native CLI and its Bash tool. */
export function lettaMcpCliToolCall(id: string, agentId: string, toolId: string, input: Record<string, unknown>): MockModelToolCall {
  if (!id.trim() || !agentId.trim() || agentId !== agentId.trim() || !/^[\w-]+$/.test(agentId))
    throw new Error('The native Letta MCP call requires a call ID and a valid agent ID.')
  const prefix = 'mcp__'
  const remainder = toolId.slice(prefix.length)
  const separator = remainder.indexOf('__', 1)
  if (!toolId.startsWith(prefix) || !/^[\w-]+$/.test(remainder) || separator < 1 || separator + 2 >= remainder.length)
    throw new Error('The native Letta MCP tool ID must identify its server and tool.')
  const serialized = JSON.stringify(input)
  const command = ['letta', 'mcp', 'call', toolId, '--agent', agentId, '--args', serialized].map(quotePosixShellArgument).join(' ')
  return bashToolCall(AgentProvider.LETTA, id, command)
}

/** A single-hunk file edit, in the provider's own edit tool. */
export function editToolCall(provider: AgentProvider, id: string, request: EditRequest): MockModelToolCall {
  return requireBuilder(vocabulary(provider).edit, provider, 'edit')(id, request)
}

/** Run a command in the background, which opens a SHELL row in the registry. */
export function backgroundBashToolCall(provider: AgentProvider, id: string, command: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).backgroundBash, provider, 'background shell')(id, command)
}

/** Spawn a subagent, in the provider's own tool. */
export function spawnSubagentToolCall(provider: AgentProvider, id: string, request: SubagentRequest): MockModelToolCall {
  return requireBuilder(vocabulary(provider).spawnSubagent, provider, 'subagent spawn')(id, request)
}

/** Spawn several subagents in one native call, which runs them at the same time. */
export function spawnSubagentBatchToolCall(provider: AgentProvider, id: string, requests: readonly SubagentRequest[]): MockModelToolCall {
  return requireBuilder(vocabulary(provider).spawnSubagentBatch, provider, 'subagent batch spawn')(id, requests)
}

export function updateTodosToolCall(provider: AgentProvider, id: string, steps: TodoStep[]): MockModelToolCall {
  return requireBuilder(vocabulary(provider).updateTodos, provider, 'to-do list update')(id, steps)
}

/** One incremental call of Pi's native todo extension. */
export function piTodoToolCall(callId: string, request: PiTodoRequest): MockModelToolCall {
  return { id: callId, name: PI_TOOL.Todo, arguments: { ...request } }
}

/**
 * Ask the Cursor CLI for its request context again before the turn ends.
 *
 * The CLI states the rules that it loaded from the project in its answer, and the
 * mock records them as `nativeRequest.contextRules` of the same model request. The
 * mock asks once at the start of each turn already (`cursorSurface.ts`), so this
 * call adds only a second answer, which replaces the first.
 */
export function cursorRequestContextToolCall(id: string): MockModelToolCall {
  return { id, name: CURSOR_REQUEST_CONTEXT_TOOL, arguments: {} }
}

/** Run the installed Pi subagents extension with its native workflow script field. */
export function piWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'SubagentWorkflow', arguments: { script } }
}

/** Match the installed Pi 0.99.1 direct tool catalog. */
function piNativeMcpToolName(server: string, tool: string): string {
  const name = `mcp__${server}__${tool}`.replace(/[^\w-]/g, '_')
  if (name.length <= 64)
    return name
  const hash = createHash('sha256').update(`${server}\0${tool}`).digest('hex').slice(0, 8)
  return `${name.slice(0, 55)}_${hash}`
}

/** Call one of Pi's built-in MCP resource tools. */
export function piMcpResourceToolCall(id: string, request: { operation: 'list' | 'templates' | 'read', server: string, uri?: string }): MockModelToolCall {
  if (!id || !request.server || (request.operation === 'read' && !request.uri))
    throw new Error('A native Pi resource call requires a call ID, server, and a URI for a read.')
  const names = { list: 'list_mcp_resources', templates: 'list_mcp_resource_templates', read: 'read_mcp_resource' }
  return { id, name: names[request.operation], arguments: { server: request.server, ...(request.operation === 'read' ? { uri: request.uri } : {}) } }
}

/** Run a script through Pi's built-in codemode tool. */
export function piCodemodeToolCall(id: string, code: string): MockModelToolCall {
  return { id, name: 'codemode', arguments: { code } }
}

/** Script the provider's native code executor without changing its source text. */
export function codeExecutionToolCall(provider: AgentProvider, id: string, source: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).codeExecution, provider, 'audited native code executor')(id, source)
}

/** The names of the native workflow tools of `provider`. A provider that the suite knows no workflow tool of has none. */
export function workflowToolNames(provider: AgentProvider): readonly string[] {
  return vocabulary(provider).workflowTools ?? []
}

/**
 * The name of each native workflow tool that a provider of the table offers.
 * A provider with no workflow support must offer none of them, so a check of its catalog reads the whole list.
 */
export const WORKFLOW_TOOL_NAMES: readonly string[] = [...new Set(Object.values(TOOL_VOCABULARY).flatMap(entry => entry.workflowTools ?? []))].sort()

/** Invoke CodeBuddy's direct REPL in its native ptc agent configuration. */
export function codebuddyReplToolCall(id: string, code: string): MockModelToolCall {
  return { id, name: 'REPL', arguments: { code } }
}

/** Invoke the current Droid shell tool with its required summary and explicit risk level. */
export function droidExecuteToolCall(id: string, request: { command: string, summary: string, riskLevel: 'low' | 'medium' | 'high' }): MockModelToolCall {
  const callId = droidCallId(id)
  if (!request.command.trim() || !request.summary.trim() || !['low', 'medium', 'high'].includes(request.riskLevel))
    throw new Error('The native Droid Execute call requires an ID, command, summary, and valid risk level.')
  return { id: callId, name: 'Execute', arguments: { ...request } }
}

/** Run one native Droid script with its exact source and optional observation period. */
export function droidScriptToolCall(id: string, script: string, waitForMs?: number): MockModelToolCall {
  if (!script.trim() || new TextEncoder().encode(script).byteLength > 512 * 1024)
    throw new Error('The native Droid script requires nonempty source within 512 KiB.')
  if (waitForMs !== undefined && (!Number.isFinite(waitForMs) || waitForMs < 0))
    throw new RangeError('The native Droid script wait requires finite nonnegative milliseconds.')
  return { id: droidCallId(id), name: 'Script', arguments: { script, ...(waitForMs === undefined ? {} : { waitForMs }) } }
}

/** Invoke the disposable Pi extension that requests a native multiline editor. */
export function piEditorProbeToolCall(id: string): MockModelToolCall {
  return { id, name: 'editor_probe', arguments: {} }
}

/** Probe Fast Agent's native human-input route that requires an elicitation handler. */
export function fastAgentHumanInputToolCall(callId: string, message: string, answers: readonly string[]): MockModelToolCall {
  if (answers.length === 0)
    throw new Error('The Fast Agent human-input schema requires an answer choice.')
  return {
    id: callId,
    name: '__human_input',
    arguments: {
      message,
      schema: {
        type: 'object',
        properties: { answer: { type: 'string', enum: [...answers] } },
        required: ['answer'],
      },
    },
  }
}

/** Start a session goal from the model's side, in the provider's own tool. */
export function createGoalToolCall(provider: AgentProvider, id: string, objective: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).createGoal, provider, 'goal creation')(id, objective)
}

/** Mark the session goal complete, in the provider's own tool. */
export function completeGoalToolCall(provider: AgentProvider, id: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).completeGoal, provider, 'goal completion')(id)
}

/** One call of a Model Context Protocol tool, in the provider's own shape. */
export function mcpToolCall(provider: AgentProvider, id: string, request: McpToolRequest): MockModelToolCall {
  return requireBuilder(vocabulary(provider).mcpTool, provider, 'MCP tool')(id, request)
}

/** Load Droid's deferred MCP tool schema before the model calls that tool. */
export function droidToolSearchToolCall(id: string, query: string, maxResults?: number): MockModelToolCall {
  if (maxResults !== undefined && (!Number.isSafeInteger(maxResults) || maxResults <= 0))
    throw new RangeError('The native Droid tool search requires a positive safe result maximum.')
  return { id: droidCallId(id), name: 'ToolSearch', arguments: { query, ...(maxResults === undefined ? {} : { max_results: maxResults }) } }
}

/** Wait for CodeBuddy's project MCP server before its tool enters the model catalog. */
export function codebuddyWaitForMcpServersToolCall(id: string, servers: string[]): MockModelToolCall {
  return { id, name: 'WaitForMcpServers', arguments: { servers } }
}

/** Discover CodeBuddy's deferred Workflow tool before executing it. */
export function codebuddyFindWorkflowToolCall(id: string): MockModelToolCall {
  return codebuddyFindToolsToolCall(id, ['Workflow'])
}

/** Load the exact deferred CodeBuddy tool names through its native search tool. */
export function codebuddyFindToolsToolCall(id: string, toolNames: readonly string[]): MockModelToolCall {
  if (toolNames.length === 0 || toolNames.some(name => !name.trim()))
    throw new Error('The native CodeBuddy search requires exact tool names.')
  return { id, name: 'ToolSearch', arguments: { tool_names: [...toolNames] } }
}

/** Run CodeBuddy's deferred Workflow tool with its native wrapper. */
export function codebuddyWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'DeferExecuteTool', arguments: { toolName: 'Workflow', params: { script } } }
}

/**
 * One TaskCreate call of the Task family. Each provider of the family takes a subject and a description, and Claude
 * Code and Command Code also take the present-continuous text that a running task shows.
 */
function taskFamilyCreateCall(name: string, id: string, task: { subject: string, description: string, activeForm?: string | undefined }): MockModelToolCall {
  if (task.subject.trim() === '' || task.description.trim() === '')
    throw new Error(`The native ${name} call requires a nonempty subject and description.`)
  return { id, name, arguments: { subject: task.subject, description: task.description, ...(task.activeForm === undefined ? {} : { activeForm: task.activeForm }) } }
}

/** One TaskUpdate call of the Task family, which changes the status of one task by its native ID. */
function taskFamilyUpdateCall(name: string, id: string, taskId: string, status: TaskFamilyStatus): MockModelToolCall {
  if (taskId.trim() === '')
    throw new Error(`The native ${name} call requires the ID of its task.`)
  return { id, name, arguments: { taskId, status } }
}

/**
 * Create one Claude Code task (2.1.289); its result states the task ID. Claude Code offers the Task family, as
 * TaskCreate, TaskGet, TaskList and TaskUpdate, only under the conditions that the Claude `updateTodos` entry states.
 */
export function claudeTaskCreateToolCall(id: string, subject: string, description: string, activeForm?: string): MockModelToolCall {
  return taskFamilyCreateCall('TaskCreate', id, { subject, description, activeForm })
}

/** Change one Claude Code task by its native ID. The schema takes `deleted` beside the three list statuses. */
export function claudeTaskUpdateToolCall(id: string, taskId: string, status: TaskFamilyStatus): MockModelToolCall {
  return taskFamilyUpdateCall('TaskUpdate', id, taskId, status)
}

/** Create one CodeBuddy task; its result returns the full native todo list. */
export function codebuddyTaskCreateToolCall(id: string, subject: string, description: string): MockModelToolCall {
  return taskFamilyCreateCall('TaskCreate', id, { subject, description })
}

/** Change one CodeBuddy task by its native numeric-string ID. */
export function codebuddyTaskUpdateToolCall(id: string, taskId: string, status: TaskFamilyStatus): MockModelToolCall {
  return taskFamilyUpdateCall('TaskUpdate', id, taskId, status)
}

/** Ask Letta's vision tool to open a local image. */
export function lettaViewImageToolCall(id: string, path: string): MockModelToolCall {
  return { id, name: 'ViewImage', arguments: { path } }
}

/** Create one Letta task; the native result gives its stable task ID. */
export function lettaTaskCreateToolCall(id: string, subject: string, description: string): MockModelToolCall {
  return taskFamilyCreateCall('TaskCreate', id, { subject, description })
}

/** Change one Letta task by its native task ID. */
export function lettaTaskUpdateToolCall(id: string, taskId: string, status: TaskFamilyStatus): MockModelToolCall {
  return taskFamilyUpdateCall('TaskUpdate', id, taskId, status)
}

/** Read the full Letta task list. */
export function lettaTaskListToolCall(id: string): MockModelToolCall {
  return { id, name: 'TaskList', arguments: {} }
}

/** Mark the session goal blocked with reason, in the provider's own tool. */
export function blockGoalToolCall(provider: AgentProvider, id: string, reason: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).blockGoal, provider, 'goal block')(id, reason)
}

/** A whole-file write, in the provider's own write tool. */
export function writeToolCall(provider: AgentProvider, id: string, request: WriteRequest): MockModelToolCall {
  return requireBuilder(vocabulary(provider).write, provider, 'write')(id, request)
}

/** Copilot sends its apply-patch grammar as raw custom-tool input. */
export function copilotApplyPatchToolCall(id: string, patch: string): MockModelToolCall {
  return { id, name: COPILOT_TOOL.ApplyPatch, input: patch }
}

/** A file read, in the provider's own read tool. */
export function readToolCall(provider: AgentProvider, id: string, path: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).read, provider, 'read')(id, path)
}

export function zcodeReadRangeToolCall(id: string, path: string, range: { offset: number, limit: number }): MockModelToolCall {
  if (!Number.isSafeInteger(range.offset) || range.offset < 0 || !Number.isSafeInteger(range.limit) || range.limit < 1)
    throw new Error('The native ZCode Read range requires a nonnegative integer offset and a positive integer limit.')
  const call = readToolCall(AgentProvider.ZCODE, id, path)
  return { ...call, arguments: { ...call.arguments, offset: range.offset, limit: range.limit } }
}

/** Codex reads image bytes with view_image, not a shell command. */
export function codexViewImageToolCall(id: string, path: string): MockModelToolCall {
  return codexExecToolCall(
    id,
    `const result = await tools.view_image({ path: ${JSON.stringify(path)} })\nimage(result.image_url)`,
  )
}

/** Request Codex's native approval before a scripted shell command runs. */
export function codexEscalatedCommandToolCall(id: string, command: string): MockModelToolCall {
  return codexCommandCall(id, { cmd: command, sandbox_permissions: 'require_escalated', justification: 'Run the scripted approval test.' })
}

/** Kimi Code reads image files through ReadMediaFile, not its text Read tool. */
export function kimiReadMediaFileToolCall(id: string, path: string): MockModelToolCall {
  return { id, name: 'ReadMediaFile', arguments: { path } }
}

/** Codewhale discovers deferred native and MCP tools through its own search tool. */
export function codewhaleToolSearchToolCall(id: string, query: string): MockModelToolCall {
  return { id, name: CODEWHALE_TOOL.ToolSearch, arguments: { query } }
}

/** Codewhale reads images through its vision-aware native media tool. */
export function codewhaleReadMediaToolCall(id: string, path: string): MockModelToolCall {
  return { id, name: CODEWHALE_TOOL.ReadMedia, arguments: { path } }
}

/** Goose's image reader returns image content from a local path. */
export function gooseReadImageToolCall(id: string, source: string): MockModelToolCall {
  return { id, name: 'read_image', arguments: { source } }
}

/** Return the native Goose classifier's exact list of read-only tool request IDs. */
export function goosePermissionJudgmentToolCall(id: string, readOnlyRequestIds: readonly string[]): MockModelToolCall {
  return { id, name: 'platform__tool_by_tool_permission', arguments: { read_only_request_ids: [...readOnlyRequestIds] } }
}

/** Reasonix's image reader returns structured image content from a local path. */
export function reasonixViewImageToolCall(id: string, path: string): MockModelToolCall {
  return { id, name: 'view_image', arguments: { path } }
}

/** ZCode's installed Node tool returns emitted PNG bytes as a native image result. */
export function zcodeNodeImageToolCall(id: string, base64: string, label: string): MockModelToolCall {
  return {
    id,
    name: 'mcp__node_repl__js',
    arguments: {
      code: `nodeRepl.write(${JSON.stringify(label)}); await nodeRepl.emitImage({ base64: ${JSON.stringify(base64)}, mimeType: 'image/png' })`,
      title: `Show ${label}`,
    },
  }
}

/** Script Cursor's native GenerateImage call and its generated PNG result. */
export function cursorGenerateImageToolCall(id: string, description: string, filePath: string, imageData: string): MockModelToolCall {
  return { id, name: CURSOR_GENERATE_IMAGE_TOOL, arguments: { description, filePath, imageData } }
}

/** Script Cursor's native create-plan query and its approval control. */
export function cursorCreatePlanToolCall(id: string, name: string, overview: string, plan: string): MockModelToolCall {
  return { id, name: CURSOR_CREATE_PLAN_TOOL, arguments: { name, overview, plan } }
}

/** Script Cursor's native web-fetch query, which asks the client for permission. */
export function cursorWebFetchPermissionToolCall(id: string, url: string): MockModelToolCall {
  return { id, name: CURSOR_WEB_FETCH_TOOL, arguments: { url } }
}

/** Load the bundled skill that the installed ZCode CreateWorkflow tool requires. */
export function zcodeWorkflowSkillToolCall(id: string): MockModelToolCall {
  return { id, name: 'Skill', arguments: { skill: 'dynamic-workflows' } }
}

/** Start a named ZCode workflow with an inline TypeScript script. */
export function zcodeCreateWorkflowToolCall(id: string, name: string, script: string): MockModelToolCall {
  return { id, name: 'CreateWorkflow', arguments: { name, script } }
}

/** Read the exact native ZCode workflow result without another execution. */
export function zcodeGetWorkflowRunToolCall(id: string, runId: string): MockModelToolCall {
  if (typeof id !== 'string' || !id.trim() || id.includes('\0') || typeof runId !== 'string' || !runId.trim() || runId.includes('\0'))
    throw new Error('The native ZCode workflow query requires a call ID and run ID.')
  return { id, name: 'GetWorkflowRun', arguments: { run_id: runId } }
}

/**
 * The question in Claude's own fields. `multiSelect` is required by Claude's schema, so state it rather than omit it.
 * `freeText` is no field of that schema, so it stays out.
 */
function withMultiSelect({ freeText: _freeText, ...question }: QuestionRequest): Record<string, unknown> {
  return { ...question, multiSelect: question.multiSelect ?? false }
}

/** Ask the user to choose, in the provider's own tool. */
export function askUserQuestionToolCall(provider: AgentProvider, id: string, questions: QuestionRequest[]): MockModelToolCall {
  return requireBuilder(vocabulary(provider).askUserQuestion, provider, 'ask user question')(id, questions)
}

/** Enter plan mode, in the provider's own tool. */
export function enterPlanModeToolCall(provider: AgentProvider, id: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).enterPlanMode, provider, 'enter plan mode')(id)
}

/** Leave plan mode and raise the plan for approval. */
export function exitPlanModeToolCall(provider: AgentProvider, id: string, plan: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).exitPlanMode, provider, 'exit plan mode')(id, plan)
}

/** Leave plan mode and raise the plan file that the model wrote, offering choices. */
export function exitPlanModeFromFileToolCall(provider: AgentProvider, id: string, approaches: PlanApproachRequest[]): MockModelToolCall {
  return requireBuilder(vocabulary(provider).exitPlanModeFromFile, provider, 'exit plan mode from a plan file')(id, approaches)
}

/**
 * One operation of MiMo Code's task tool.
 * Each native task call creates one item or changes one existing item.
 * MiMo assigns its own IDs in creation order, such as T1 and T2.
 */
export type MiMoTaskOperation
  = | { action: 'create', summary: string }
    | { action: 'start' | 'done' | 'abandon', id: string }

/**
 * The native workflow script calls agent for each subagent.
 * The tool is experimental.
 * The E2E environment enables it through MIMOCODE_EXPERIMENTAL_WORKFLOW_TOOL.
 */
export function mimoWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'workflow', arguments: { operation: 'run', script } }
}

/** Start Kimi Code's native AgentSwarm with one prompt per item. */
export function kimiAgentSwarmToolCall(id: string, description: string, promptTemplate: string, items: string[]): MockModelToolCall {
  return { id, name: 'AgentSwarm', arguments: { description, subagent_type: 'coder', prompt_template: promptTemplate, items } }
}

/** Run Qwen Code's native workflow tool with an inline script. */
export function qwenWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'workflow', arguments: { script } }
}

/** Run Codewhale's structured workflow with actual read-only children. */
export function codewhaleWorkflowToolCall(id: string, goal: string, prompt: string, children?: readonly { label: string, prompt: string }[]): MockModelToolCall {
  if (children !== undefined && !Array.isArray(children))
    throw new Error('The native workflow children must be an array.')
  const assignments = children === undefined ? [{ label: 'Probe child', prompt }] : children
  if (assignments.length === 0 || assignments.some(child => typeof child !== 'object' || child === null || typeof child.label !== 'string' || typeof child.prompt !== 'string' || child.prompt.trim() === ''))
    throw new Error('The native workflow requires a nonempty child assignment.')
  return {
    id,
    name: 'workflow',
    arguments: {
      action: 'run',
      plan: {
        goal,
        risk: 'read_only',
        phases: [],
        children: assignments.map(child => ({ label: child.label, prompt: child.prompt, type: 'explore', file_scope: [] })),
        gates: [],
      },
    },
  }
}

/** Run Grok Build's native Rhai workflow source. */
export function grokWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'workflow', arguments: { source: { type: 'script', script } } }
}

/** Add one Cline teammate before routing a team task. */
export function clineSpawnTeammateToolCall(id: string, agentId: string, rolePrompt: string): MockModelToolCall {
  return { id, name: 'team_spawn_teammate', arguments: { agentId, rolePrompt } }
}

/** Queue a Cline teammate run that emits native run lifecycle events. */
export function clineRunTeammateTaskToolCall(id: string, agentId: string, task: string): MockModelToolCall {
  return { id, name: 'team_run_task', arguments: { agentId, task, runMode: 'async' } }
}

/** Run Claude Code's native Workflow tool with a self-contained script. */
export function claudeWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'Workflow', arguments: { script } }
}

/** Run Qoder CLI's native Workflow tool with a self-contained script. */
export function qoderWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'Workflow', arguments: { script } }
}

/** Read the complete installed Reasonix capability list without a page cursor. */
export function reasonixListCapabilitiesToolCall(id: string): MockModelToolCall {
  if (!id.trim())
    throw new Error('The Reasonix capability list requires a nonempty call ID.')
  return { id, name: 'use_capability', arguments: { action: 'list' } }
}

/** Read one exact installed Reasonix capability descriptor. */
export function reasonixInspectCapabilityToolCall(id: string, capabilityId: string): MockModelToolCall {
  if (!id.trim() || !capabilityId.trim() || capabilityId !== capabilityId.trim())
    throw new Error('The Reasonix capability inspect requires exact nonempty call and capability IDs.')
  return { id, name: 'use_capability', arguments: { action: 'inspect', capability_id: capabilityId } }
}

/**
 * One change of status in Oh My Pi's todo tool (omp 18.6.0). The tool addresses a task by its verbatim text, and it
 * refuses a task ID.
 *
 * - `start`: the task becomes in_progress, and omp moves any other in_progress task back to pending.
 * - `done`: the task becomes completed.
 * - `drop`: the task becomes abandoned.
 * - `block`: the task becomes blocked, and the reason becomes its blocker note.
 *
 * After each change, omp makes the first pending task in_progress when no task is.
 */
export type OhMyPiTodoOperation
  = | { op: 'start' | 'done' | 'drop', task: string }
    | { op: 'block', task: string, reason: string }

/** One call of Oh My Pi's todo tool that changes the status of one task. */
export function ohMyPiTodoToolCall(id: string, operation: OhMyPiTodoOperation): MockModelToolCall {
  if (operation.task.trim() === '')
    throw new Error('The native Oh My Pi todo operation requires the verbatim text of its task.')
  if (operation.op === 'block')
    requireReason('Oh My Pi todo block', operation.reason)
  return { id, name: 'todo', arguments: { ...operation } }
}

/**
 * The native yield tool sends the child's report to its parent through data.
 * It also ends the child run.
 * Oh My Pi offers it only to a subagent.
 */
export function ohMyPiYieldToolCall(id: string, report: string): MockModelToolCall {
  return { id, name: 'yield', arguments: { data: report } }
}

/** One call of MiMo Code's to-do tool. */
export function mimoTaskToolCall(id: string, operation: MiMoTaskOperation): MockModelToolCall {
  return { id, name: 'task', arguments: { operation } }
}

/** Cancel one existing native MiMo actor through its model tool. */
export function mimoActorCancelToolCall(id: string, actorId: string): MockModelToolCall {
  if (actorId.trim() === '')
    throw new Error('A MiMo actor cancellation requires an actor ID.')
  return { id, name: MIMO_TOOL.Actor, arguments: { operation: { action: MIMO_ACTOR_ACTION.Cancel, actor_id: actorId } } }
}

/**
 * The native shell tool accepts interactive=true for a command that needs keyboard input.
 * It then requests that input from the client.
 * LeapMux refuses the request because its UI cannot send keyboard input to that command.
 */
export function mimoInteractiveBashToolCall(id: string, command: string): MockModelToolCall {
  return { id, name: 'bash', arguments: { command, description: 'Run the scripted interactive command', interactive: true } }
}

/**
 * The native switch_to_execution tool sends the plan to execution mode.
 * It leaves plan mode without requesting approval.
 * The shared exitPlanMode contract requires approval, so it cannot represent that native operation.
 */
export function kiroSwitchToExecutionToolCall(id: string, plan: string): MockModelToolCall {
  return { id, name: 'switch_to_execution', arguments: { plan } }
}

/**
 * Kiro gives each task a numeric ID in create order, starting at 1.
 * The complete command changes a task to its completed state.
 * Kiro supplies no separate state for a task in progress.
 */
export function kiroCompleteTodosToolCall(id: string, taskIds: string[]): MockModelToolCall {
  return { id, name: 'todo_list', arguments: { command: 'complete', completed_task_ids: taskIds, context_update: 'The scripted tasks are done.' } }
}

/**
 * Whether this table supplies a builder for one operation.
 */
export function hasToolFor(provider: AgentProvider, operation: keyof ProviderToolVocabulary): boolean {
  return vocabulary(provider)[operation] !== null
}

export function diracRespondToolCall(id: string, operation: 'complete' | 'progress' | 'plan' | 'question', text: string, options?: string[]): MockModelToolCall {
  return {
    id,
    name: 'respond',
    arguments: { operation, text, ...(options ? { options } : {}) },
  }
}

/** Return the summary that Dirac's native /smol command asks the model to write. */
export function diracCondenseToolCall(id: string, context: string): MockModelToolCall {
  return { id, name: 'condense', arguments: { context } }
}

export function diracEditAnchorCapture(content: string): Record<string, string> {
  return { editAnchor: `([A-Z][a-zA-Z]*§${escapeRegExp(content)})` }
}

/**
 * The name of Junie's answer tool. Junie's transcript shows a call of this tool
 * as the answer text and as no tool row, so a turn that holds only this call
 * has no tool activity.
 */
export const JUNIE_ANSWER_TOOL = 'answer'

/**
 * The native answer tool delivers the final text.
 * Its default is_terminal value ends the session task.
 * Junie refuses a model response that contains only text.
 * It returns "Your response is missing a tool call" and retries six times before cancelling the prompt.
 * Each scripted main-agent response therefore requires a tool call.
 * The answer tool carries that response text.
 */
export function junieAnswerToolCall(id: string, fullAnswer: string): MockModelToolCall {
  return { id, name: JUNIE_ANSWER_TOOL, arguments: { full_answer: fullAnswer } }
}

/** The bundled Junie docs child ends its task through `submit`. */
export function junieSubagentSubmitToolCall(id: string, summary: string): MockModelToolCall {
  return { id, name: 'submit', arguments: { solution_summary: summary } }
}

/** Submit Junie's plan tabs and delivery stages through its current tool. */
export function junieSubmitPlanToolCall(
  id: string,
  name: string,
  proposal: Array<{ name: string, content: string }>,
  deliveryPlan: Array<{ name: string, description: string }>,
): MockModelToolCall {
  return { id, name: 'submit', arguments: { name, proposal, delivery_plan: deliveryPlan } }
}

/** Submit Gemini's native plan filename without supplying invented plan text. */
export function geminiPlanApprovalToolCall(id: string, filename: string): MockModelToolCall {
  return { id, name: GEMINI_TOOL.ExitPlanMode, arguments: { plan_filename: filename } }
}

/** Create one native Command Code task. */
export function commandCodeTaskCreateToolCall(id: string, subject: string, description: string, activeForm?: string): MockModelToolCall {
  return taskFamilyCreateCall(COMMAND_CODE_TOOL.TaskCreate, id, { subject, description, activeForm })
}

/** Change one native Command Code task by its actual ID. */
export function commandCodeTaskUpdateToolCall(id: string, taskId: string, status: TaskFamilyStatus): MockModelToolCall {
  return taskFamilyUpdateCall(COMMAND_CODE_TOOL.TaskUpdate, id, taskId, status)
}

/** Ask the native Command Code runtime to load its deferred tool schemas. */
export function commandCodeLoadToolsToolCall(id: string, query: string, maxResults?: number): MockModelToolCall {
  return { id, name: 'search_tools', arguments: { query, ...(maxResults === undefined ? {} : { max_results: maxResults }) } }
}

/** Return the exact native result that completes a Gemini child task. */
export function geminiCompleteTaskToolCall(id: string, response: string): MockModelToolCall {
  return { id, name: GEMINI_TOOL.CompleteTask, arguments: { result: { response } } }
}

/** Replace the native Gemini checklist with all supported task statuses. */
export function geminiTodoSnapshotToolCall(id: string, todos: readonly { description: string, status: (typeof CANCELLED_AND_BLOCKED_TODO_STATUSES)[number] }[]): MockModelToolCall {
  return updateTodosToolCall(AgentProvider.GEMINI_CLI, id, todos.map(todo => ({ step: todo.description, status: todo.status })))
}

/**
 * The capture source that reads the goal ID from the result of DeepSeek Harness's get_goal, whose text is the compact
 * JSON `{"goal":{"id":...,"revision":...},...}` (dsh-tool-goal 0.2.0-rc.2). A later update_goal states the ID as the
 * `{{goalId}}` placeholder of a step with this capture.
 */
export const DEEPSEEK_HARNESS_GOAL_ID_CAPTURE = '"goal":\\{"id":"([^"]+)"'

/** Read the current DeepSeek Harness goal, with the ID and the revision that update_goal requires. */
export function deepseekHarnessGetGoalToolCall(id: string): MockModelToolCall {
  return { id, name: 'get_goal', arguments: {} }
}

/** How a DeepSeek Harness update_goal call ends a goal. A blocked goal states its blocking condition. */
export type DeepseekHarnessGoalEnd = { action: 'complete' } | { action: 'blocked', reason: string }

/**
 * End the DeepSeek Harness goal through update_goal (dsh-tool-goal 0.2.0-rc.2), which compares the goal ID and the
 * revision that get_goal returned before it changes the goal.
 *
 * - The revision must be a number, so a capture placeholder cannot state it. A goal that LeapMux set and did not edit
 *   is at revision 1.
 * - In an automatic goal round, the tool refuses `blocked` before the third consecutive round.
 */
export function deepseekHarnessUpdateGoalToolCall(id: string, goal: { goalId: string, revision: number }, end: DeepseekHarnessGoalEnd): MockModelToolCall {
  if (goal.goalId.trim() === '' || goal.goalId !== goal.goalId.trim() || !Number.isSafeInteger(goal.revision) || goal.revision < 1)
    throw new Error('The native DeepSeek Harness update_goal call requires a goal ID and a positive integer revision.')
  return {
    id,
    name: 'update_goal',
    arguments: {
      goal_id: goal.goalId,
      revision: goal.revision,
      action: end.action,
      ...(end.action === 'blocked' ? { blocked_reason: requireReason('DeepSeek Harness update_goal', end.reason) } : {}),
    },
  }
}

/** Read an image through the native Deepseek Harness image tool. */
export function deepseekHarnessReadImageToolCall(id: string, path: string): MockModelToolCall {
  return { id, name: DEEPSEEK_HARNESS_TOOL.ReadImage, arguments: { file_path: path } }
}

/** Request the exact wider native sandbox permission through Deepseek Harness. */
export function deepseekHarnessEscalatedBashToolCall(id: string, command: string, justification: string): MockModelToolCall {
  return { id, name: DEEPSEEK_HARNESS_TOOL.Bash, arguments: { command, description: 'Run the scripted command.', sandbox_permissions: 'danger-full-access', justification } }
}

/** Run native code that can call the registered DeepSeek tools. */
export function deepseekHarnessRunCodeToolCall(id: string, source: string): MockModelToolCall {
  if (typeof id !== 'string' || !id.trim() || typeof source !== 'string' || !source.trim())
    throw new Error('The native DeepSeek code call requires a nonempty call ID and source.')
  return { id, name: DEEPSEEK_HARNESS_TOOL.RunCode, arguments: { description: 'Read the native MCP value.', code: source } }
}
