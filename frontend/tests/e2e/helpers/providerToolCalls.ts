import type { MockModelToolCall } from './mockModelScript'
// A RELATIVE import, not `~/...`. See the note in `../agentSettings.ts`.
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_TASK_TOOL } from './cursorSurface'

/**
 * The tool call one provider makes for a given operation.
 *
 * The mock endpoint answers with whatever a test scripts, so a scripted tool
 * call must use the provider's OWN tool name and argument shape. Every entry
 * below was read off a request the provider actually sent, and the E2E
 * specifications that script it are what keep it true: an entry the agent no
 * longer accepts fails the spec that uses it, because the agent then runs no
 * tool and the scripted scenario is left unconsumed.
 *
 * This is the one place a provider's tool vocabulary appears in the E2E
 * helpers, and `satisfies` makes a new provider a typecheck failure here rather
 * than a test that scripts a tool no agent offers.
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

/** A subagent to spawn. */
export interface SubagentRequest {
  /** A short label, 3 to 5 words, which the registry row shows. */
  description: string
  /** The task the child performs. Mark it so the child's turns reach the script. */
  prompt: string
  /**
   * The child's answer, for a provider that resolves a subagent LOCALLY.
   *
   * Cursor is the only one. Its CLI never asks the endpoint for the child's
   * turn, so the child's answer cannot come from a rule the way it does
   * everywhere else -- the scripted call has to carry it. Every other provider
   * ignores this field and answers the child's own turn instead.
   */
  report?: string
}

/** One step of a to-do list, in the shape every provider that keeps one shares. */
export interface TodoStep {
  step: string
  status: 'pending' | 'in_progress' | 'completed'
}

/** One choice offered by a question. */
export interface QuestionOption {
  label: string
  description: string
  /**
   * Markdown shown beside the option, which the control surface renders in its
   * own region. A fenced block highlights; anything else keeps its whitespace,
   * which is what lets a box-drawing diagram survive.
   *
   * Only the providers whose question extension carries a preview use it, and
   * the builders pass the option through whole, so an unused field costs
   * nothing.
   */
  preview?: string
}

/** One question, in the shape every provider that asks one shares. */
export interface QuestionRequest {
  question: string
  /** A short chip label. Claude caps it at 12 characters, Pi at 16. */
  header: string
  options: QuestionOption[]
  multiSelect?: boolean
}

/**
 * One provider's builders.
 *
 * A null member states that the provider offers no tool for that operation. A
 * caller then gets a named error rather than a call the agent ignores.
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
  /** Ask the user to choose, which raises a control request. */
  askUserQuestion: ((id: string, questions: QuestionRequest[]) => MockModelToolCall) | null
  /** Spawn a subagent, which opens a registry row and a child transcript. */
  spawnSubagent: ((id: string, request: SubagentRequest) => MockModelToolCall) | null
  /**
   * Run a command in the BACKGROUND, which opens a SHELL row in the registry.
   *
   * A background shell is a different row kind from a subagent: it carries no
   * child agent and its row is static. Null where no test has needed one yet,
   * on the same terms as `updateTodos`.
   */
  backgroundBash: ((id: string, command: string) => MockModelToolCall) | null
  /**
   * Write the session's to-do list, which drives the indicator's to-dos chip.
   *
   * Null where no test has needed one yet, rather than where the provider has no
   * tool: filling one in means reading the provider's own argument shape off the
   * wire or out of its source, which is work this table only does on demand.
   */
  updateTodos: ((id: string, steps: TodoStep[]) => MockModelToolCall) | null
}

/**
 * A description reduced to an identifier a provider will accept.
 *
 * Two spawn tools take a NAME beside the prompt rather than free text: Codex's
 * `task_name` and Copilot's `name`. Both reject the spaces and punctuation a
 * description carries.
 */
function identifierFrom(description: string): string {
  const identifier = description.toLowerCase().replaceAll(/[^a-z0-9]+/g, '_').replaceAll(/^_+|_+$/g, '')
  // Both call sites send this as a REQUIRED name. A description that is empty,
  // or that holds punctuation alone, reduces to nothing -- and a provider that
  // refuses an empty name refuses the spawn, which surfaces far from here as a
  // subagent that never opened a registry row. A stable fallback keeps the call
  // well formed, and it is greppable when a test shows it.
  return identifier === '' ? 'scripted_subagent' : identifier
}

/**
 * The namespace holding Codex's sub-agent tools.
 *
 * `codex-rs/core/src/tools/router.rs` matches this exact string for
 * `spawn_agent`, `send_message` and `followup_task`. `functions`, which holds
 * `exec`, is the DEFAULT namespace and rides the wire without one.
 */
const CODEX_COLLABORATION_NAMESPACE = 'collaboration'

/**
 * Codex drives every SHELL tool through `exec`, an OpenAI CUSTOM tool.
 *
 * Its input is JavaScript source, not JSON, and the nested tools hang off a
 * `tools` global. `text(...)` appends a result item, so the command's output
 * reaches the transcript the same way a real turn puts it there.
 */
function codexExec(id: string, source: string): MockModelToolCall {
  return { id, name: 'exec', input: source }
}

function codexApplyPatch(id: string, patch: string): MockModelToolCall {
  // Report the result. Without `text(...)` the exec cell finishes with an empty
  // output, so a patch that was refused reads exactly like one that applied.
  return codexExec(
    id,
    `const result = await tools.apply_patch(${JSON.stringify(patch)})\ntext(typeof result === 'string' ? result : JSON.stringify(result))`,
  )
}

const TOOL_VOCABULARY = {
  [AgentProvider.CLAUDE_CODE]: {
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'Edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'Write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: 'EnterPlanMode', arguments: {} }),
    exitPlanMode: (id, plan) => ({ id, name: 'ExitPlanMode', arguments: { plan } }),
    askUserQuestion: (id, questions) => ({ id, name: 'AskUserQuestion', arguments: { questions: questions.map(withMultiSelect) } }),
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose' } }),
    // The SAME `Bash` tool, with the flag that detaches it. The CLI's own
    // description states the parameter: "You can use the `run_in_background`
    // parameter to run the command in the background."
    backgroundBash: (id, command) => ({
      id,
      name: 'Bash',
      arguments: { command, description: 'Run the scripted command in the background', run_in_background: true },
    }),
    updateTodos: null,
  },
  [AgentProvider.CODEX]: {
    bash: (id, command) => codexExec(
      id,
      `const result = await tools.exec_command({ cmd: ${JSON.stringify(command)} })\ntext(result.output)`,
    ),
    edit: (id, { path, before, after }) => codexApplyPatch(
      id,
      `*** Begin Patch\n*** Update File: ${path}\n@@\n-${before}\n+${after}\n*** End Patch`,
    ),
    write: (id, { path, content }) => codexApplyPatch(
      id,
      `*** Begin Patch\n*** Add File: ${path}\n${content.split('\n').map(line => `+${line}`).join('\n')}\n*** End Patch`,
    ),
    read: (id, path) => codexExec(
      id,
      `const result = await tools.exec_command({ cmd: ${JSON.stringify(`cat ${path}`)} })\ntext(result.output)`,
    ),
    // Codex drives plan mode through a session mode, not a tool.
    enterPlanMode: null,
    exitPlanMode: null,
    askUserQuestion: null,
    // A FUNCTION call with JSON arguments, and one that NAMES its namespace.
    //
    // Two ways of writing this call fail, both of them quietly. Inside an `exec`
    // cell, `tools.spawn_agent(...)` spawns nothing and reports nothing: the
    // cell runs, the turn continues, and the only symptom is a registry that
    // never gains a row. As a bare `spawn_agent` function call, Codex answers
    // `unsupported call: spawn_agent` in the tool OUTPUT, which again ends no
    // turn -- the model simply reads a failed tool result and carries on.
    //
    // `task_name` takes lowercase letters, digits and underscores, which the
    // tool's own schema states.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'spawn_agent',
      namespace: CODEX_COLLABORATION_NAMESPACE,
      arguments: { task_name: identifierFrom(description), message: prompt },
    }),
    // `update_plan` is the to-do list, not plan MODE -- its own argument struct
    // says so (`codex-rs/protocol/src/plan_tool.rs`). It reaches the runtime
    // through the `exec` sandbox like every other Codex tool.
    backgroundBash: null,
    updateTodos: (id, steps) => codexExec(
      id,
      `await tools.update_plan({ plan: ${JSON.stringify(steps)} })`,
    ),
  },
  [AgentProvider.GITHUB_COPILOT]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command, description: 'Run the scripted command' } }),
    // The native client offers no edit or write tool. Its probe declared
    // `bash`, `view`, `rg` and `glob` alone, so a file change goes through
    // `bash`.
    edit: null,
    write: null,
    read: (id, path) => ({ id, name: 'view', arguments: { path } }),
    // Copilot drives plan mode through its session-mode option group.
    enterPlanMode: null,
    exitPlanMode: null,
    askUserQuestion: null,
    // Copilot's `task` takes FOUR arguments, not the `description`/`prompt`
    // pair every other provider uses. `agent_type` is an enum on the tool's own
    // schema and `explore` is one of its values; `name` is the child's agent
    // id, so it takes an identifier; `description` is required as well, and the
    // CLI says so in the tool result ("Invalid input: \"description\":
    // Required") rather than failing the turn. `mode` is omitted, which leaves
    // the child in the foreground.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'task',
      arguments: { agent_type: 'explore', name: identifierFrom(description), description, prompt },
    }),
    backgroundBash: null,
    updateTodos: null,
  },
  [AgentProvider.CURSOR]: {
    // Cursor's turn arrives as protobuf on one Connect stream rather than as a
    // model API's JSON, so each tool needs its own encoder in `./cursorWire`.
    // Only the Task tool has one; the rest wait for a test that needs them.
    bash: null,
    edit: null,
    write: null,
    read: null,
    enterPlanMode: null,
    exitPlanMode: null,
    askUserQuestion: null,
    // `./cursorSurface` turns this into a `tool_call_started` /
    // `tool_call_completed` pair on the Run stream. `report` rides in the
    // arguments because Cursor resolves the child locally and never asks the
    // endpoint for its turn.
    spawnSubagent: (id, { description, prompt, report }) => ({
      id,
      name: CURSOR_TASK_TOOL,
      arguments: { description, prompt, report: report ?? '' },
    }),
    backgroundBash: null,
    updateTodos: null,
  },
  [AgentProvider.GOOSE]: {
    bash: (id, command) => ({ id, name: 'shell', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, before, after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'analyze', arguments: { path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    askUserQuestion: null,
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'delegate', arguments: { instructions: prompt, description } }),
    backgroundBash: null,
    updateTodos: null,
  },
  [AgentProvider.KILO]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { filePath: path, oldString: before, newString: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { filePath: path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { filePath: path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    askUserQuestion: null,
    // `subagent_type` is REQUIRED -- `task.ts` reads it to resolve the agent and
    // fails with "Unknown agent type" when it names none. `general` is the
    // built-in one (`agent/agent.ts`). Omitting the field spawned nothing and
    // left the child transcript empty.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'task',
      arguments: { description, prompt, subagent_type: 'general' },
    }),
    backgroundBash: null,
    updateTodos: null,
  },
  [AgentProvider.OPENCODE]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { filePath: path, oldString: before, newString: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { filePath: path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { filePath: path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    askUserQuestion: null,
    // `subagent_type` is REQUIRED -- `tool/task.ts` declares it as a plain
    // `Schema.String` beside `description` and `prompt`, and `general` is the
    // built-in agent (`agent/agent.ts`). Omitting it produced a registry row
    // whose child never ran a turn. Kilo shares this codebase and this field.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'task',
      arguments: { description, prompt, subagent_type: 'general' },
    }),
    backgroundBash: null,
    updateTodos: null,
  },
  [AgentProvider.PI]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, edits: [{ oldText: before, newText: after }] } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { path } }),
    askUserQuestion: (id, questions) => ({ id, name: 'ask_user_question', arguments: { questions: questions.map(withMultiSelect) } }),
    // Pi has no tool for ENTERING plan mode -- that is a session mode, not a
    // tool call. `plan_mode_complete` is how it leaves, and its one argument is
    // the plan itself (`pi-plan-mode/src/plan-mode.ts`, and the saved-plan test
    // beside it). The extension refuses the call outside plan mode, so a test
    // that scripts it must open the agent in plan mode first.
    enterPlanMode: null,
    exitPlanMode: (id, plan) => ({ id, name: 'plan_mode_complete', arguments: { plan } }),
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose' } }),
    backgroundBash: null,
    updateTodos: null,
  },
  [AgentProvider.REASONIX]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit_file', arguments: { path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write_file', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read_file', arguments: { path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    askUserQuestion: null,
    // `read_only_task`, whose schema is in `internal/agent/task.go`: `prompt` is
    // required and `description` is the 3-to-7-word label the dispatch line
    // shows. This table used to say Reasonix declared no subagent tool, which
    // was wrong -- the spec that needed one had already named it in a prompt.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'read_only_task',
      arguments: { prompt, description },
    }),
    backgroundBash: null,
    updateTodos: null,
  },
  [AgentProvider.ZCODE]: {
    bash: (id, command) => ({ id, name: 'Bash', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'Edit', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'Write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'Read', arguments: { file_path: path } }),
    enterPlanMode: id => ({ id, name: 'EnterPlanMode', arguments: {} }),
    exitPlanMode: (id, plan) => ({ id, name: 'ExitPlanMode', arguments: { plan } }),
    askUserQuestion: (id, questions) => ({ id, name: 'AskUserQuestion', arguments: { questions: questions.map(withMultiSelect) } }),
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose' } }),
    backgroundBash: null,
    updateTodos: null,
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
    throw new Error(`AgentProvider ${provider} offers no ${operation} tool`)
  return builder
}

/** A shell command, in the provider's own shell tool. */
export function bashToolCall(provider: AgentProvider, id: string, command: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).bash, provider, 'shell')(id, command)
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

export function updateTodosToolCall(provider: AgentProvider, id: string, steps: TodoStep[]): MockModelToolCall {
  return requireBuilder(vocabulary(provider).updateTodos, provider, 'to-do list update')(id, steps)
}

/** A whole-file write, in the provider's own write tool. */
export function writeToolCall(provider: AgentProvider, id: string, request: WriteRequest): MockModelToolCall {
  return requireBuilder(vocabulary(provider).write, provider, 'write')(id, request)
}

/** A file read, in the provider's own read tool. */
export function readToolCall(provider: AgentProvider, id: string, path: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).read, provider, 'read')(id, path)
}

/** `multiSelect` is required by Claude's schema, so state it rather than omit it. */
function withMultiSelect(question: QuestionRequest): Record<string, unknown> {
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

/** Whether a provider offers a tool for one operation. */
export function hasToolFor(provider: AgentProvider, operation: keyof ProviderToolVocabulary): boolean {
  return vocabulary(provider)[operation] !== null
}
