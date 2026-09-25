import type { MockModelToolCall } from './mockModelScript'
// A RELATIVE import, not `~/...`. See the note in `../agentSettings.ts`.
import { AMP_TOOL_NAME } from '../../../src/components/chat/providers/amp/toolNames'
import { CLINE_TOOL_NAME } from '../../../src/components/chat/providers/cline/toolNames'
import { AMP_SHELL_TOOL, AMP_SUBAGENT_TOOL } from '../../../src/generated/contracts/amp-protocol'
import { CLINE_TOOL } from '../../../src/generated/contracts/cline-protocol'
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
  /**
   * Run the subagent in the background, for a provider whose spawn tool takes
   * the flag. A provider that states the flag gets it EITHER way, because a
   * default that differs between releases would decide the test's path. Every
   * other provider ignores the field.
   */
  background?: boolean
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

/** One approach a plan offers. The approval surface lists each one as a choice. */
export interface PlanApproachRequest {
  label: string
  description: string
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
  /**
   * Leave plan mode, which raises the plan FILE for approval.
   *
   * For a provider whose exit call carries no plan. The model writes the plan
   * with `write` first, and the call offers the approaches as choices. Kimi Code
   * is the one such provider, and it chooses the plan path at random, so a test
   * captures the path from the request (see `KIMI_PLAN_FILE_CAPTURE`).
   */
  exitPlanModeFromFile: ((id: string, approaches: PlanApproachRequest[]) => MockModelToolCall) | null
  /** Ask the user to choose, which raises a control request. */
  askUserQuestion: ((id: string, questions: QuestionRequest[]) => MockModelToolCall) | null
  /** Spawn a subagent, which opens a registry row and a child transcript. */
  spawnSubagent: ((id: string, request: SubagentRequest) => MockModelToolCall) | null
  /**
   * Run a command in the BACKGROUND, which opens a SHELL row in the registry.
   *
   * A background shell is a different row kind from a subagent: it carries no
   * child agent and its row is static. Null where no test needs one,
   * on the same terms as `updateTodos`.
   */
  backgroundBash: ((id: string, command: string) => MockModelToolCall) | null
  /**
   * Write the session's to-do list, which drives the indicator's to-dos chip.
   *
   * Null where no test needs one, rather than where the provider has no
   * tool: filling one in means reading the provider's own argument shape off the
   * wire or out of its source, which is work this table does only on demand.
   */
  updateTodos: ((id: string, steps: TodoStep[]) => MockModelToolCall) | null
  /**
   * Start a session goal from the model's side, which the provider may raise
   * for approval.
   *
   * Null where no test needs one, on the same terms as `updateTodos`.
   */
  createGoal: ((id: string, objective: string) => MockModelToolCall) | null
  /**
   * Mark the session goal complete, which ends the provider's goal loop.
   *
   * Null where no test needs one, on the same terms as `updateTodos`.
   */
  completeGoal: ((id: string) => MockModelToolCall) | null
  /**
   * Mark the session goal blocked, with the reason, which ends the provider's
   * goal loop with no completion.
   *
   * Null where no test needs one, on the same terms as `updateTodos`.
   */
  blockGoal: ((id: string, reason: string) => MockModelToolCall) | null
  /**
   * Call one tool of a Model Context Protocol server, in the agent's own shape.
   *
   * Null where no test needs one, on the same terms as `updateTodos`.
   */
  mcpTool: ((id: string, request: McpToolRequest) => MockModelToolCall) | null
}

/** One call of a Model Context Protocol tool: the server, its tool, and the input. */
export interface McpToolRequest {
  server: string
  tool: string
  input: Record<string, unknown>
}

/**
 * A description reduced to an identifier a provider will accept.
 *
 * These spawn tools take a NAME beside the prompt rather than free text:
 *
 * - Codex's `task_name`.
 * - Copilot's `name`.
 * - Codewhale's `name`, the child's session name.
 * - Oh My Pi's task `name`, which becomes the subagent's id.
 *
 * Each one is an identifier, and the spaces and punctuation that a description
 * carries do not belong in it.
 */
function identifierFrom(description: string): string {
  const identifier = description.toLowerCase().replaceAll(/[^a-z0-9]+/g, '_').replaceAll(/^_+|_+$/g, '')
  // Codex and Copilot send this as a REQUIRED name. A description that is
  // empty, or that holds punctuation alone, reduces to nothing -- and a
  // provider that refuses an empty name refuses the spawn, which surfaces far
  // from here as a subagent that never opened a registry row. A stable fallback
  // keeps the call well formed, and it is greppable when a test shows it.
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

/** Each line of `text`, marked with `mark`, as a hunk of an apply_patch text states it. */
function patchLines(mark: '+' | '-', text: string): string {
  return text.split('\n').map(line => `${mark}${line}`).join('\n')
}

/**
 * An apply_patch text that replaces `before` with `after` in one hunk.
 *
 * Codex and Amp read the same patch format. Each line of a side carries its
 * mark, because an unmarked line is not part of the hunk.
 */
function updateFilePatch({ path, before, after }: EditRequest): string {
  return `*** Begin Patch\n*** Update File: ${path}\n@@\n${patchLines('-', before)}\n${patchLines('+', after)}\n*** End Patch`
}

/** An apply_patch text that creates the file at `path` with `content`. */
function addFilePatch({ path, content }: WriteRequest): string {
  return `*** Begin Patch\n*** Add File: ${path}\n${patchLines('+', content)}\n*** End Patch`
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
    exitPlanModeFromFile: null,
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
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
  },
  [AgentProvider.CODEWHALE]: {
    bash: (id, command) => ({ id, name: 'bash', arguments: { command } }),
    // `edit` takes a LIST of replacements, each matched against the original
    // file, and `path` resolves against the workspace.
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, edits: [{ oldText: before, newText: after }] } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'read', arguments: { path } }),
    // Plan mode is a thread setting, and no tool enters or leaves it.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    // `request_user_input` is a DEFERRED tool. The runtime answers the FIRST
    // call with the tool's schema and runs nothing, so a test scripts this call
    // twice: the second one raises the question. Each question needs an `id`,
    // which the answer repeats, and `multi_select` is the runtime's spelling.
    askUserQuestion: (id, questions) => ({
      id,
      name: 'request_user_input',
      arguments: {
        questions: questions.map((question, index) => ({
          id: `question_${index + 1}`,
          header: question.header,
          question: question.question,
          options: question.options.map(({ label, description }) => ({ label, description })),
          allow_free_text: false,
          multi_select: question.multiSelect ?? false,
        })),
      },
    }),
    // One `agent` tool holds every subagent action, and `start` returns at once
    // with the child's id while the child runs on. `name` is the child's session
    // name, so it takes an identifier. `explore` is the read-only role.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'agent',
      arguments: { action: 'start', name: identifierFrom(description), type: 'explore', prompt },
    }),
    // `bash` refuses a `background` argument, so a background job goes through
    // `task_shell_start`. It is a DEFERRED tool like `request_user_input`: the
    // first call loads its schema, and a test scripts the call twice.
    backgroundBash: (id, command) => ({ id, name: 'task_shell_start', arguments: { command } }),
    // `todo_write` replaces the whole list, and its rows say `content` where the
    // shared shape says `step`.
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_write',
      arguments: { todos: steps.map(({ step, status }) => ({ content: step, status })) },
    }),
    // A goal is a thread setting that LeapMux writes, so the model creates none.
    createGoal: null,
    // `update_goal` ends the goal loop. The runtime accepts `complete` only with
    // a verification receipt, which a scripted turn cannot supply, so a test
    // ends the loop with `blocked`.
    completeGoal: null,
    blockGoal: (id, reason) => ({ id, name: 'update_goal', arguments: { status: 'blocked', blocker: reason } }),
    mcpTool: null,
  },
  [AgentProvider.CODEX]: {
    bash: (id, command) => codexExec(
      id,
      `const result = await tools.exec_command({ cmd: ${JSON.stringify(command)} })\ntext(result.output)`,
    ),
    edit: (id, request) => codexApplyPatch(id, updateFilePatch(request)),
    write: (id, request) => codexApplyPatch(id, addFilePatch(request)),
    read: (id, path) => codexExec(
      id,
      `const result = await tools.exec_command({ cmd: ${JSON.stringify(`cat ${path}`)} })\ntext(result.output)`,
    ),
    // Codex drives plan mode through a session mode, not a tool.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
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
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
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
    exitPlanModeFromFile: null,
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
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
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
    exitPlanModeFromFile: null,
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
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
  },
  [AgentProvider.GOOSE]: {
    bash: (id, command) => ({ id, name: 'shell', arguments: { command } }),
    edit: (id, { path, before, after }) => ({ id, name: 'edit', arguments: { path, before, after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { path, content } }),
    read: (id, path) => ({ id, name: 'analyze', arguments: { path } }),
    // No plan-mode tool in its declaration.
    enterPlanMode: null,
    exitPlanMode: null,
    exitPlanModeFromFile: null,
    askUserQuestion: null,
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'delegate', arguments: { instructions: prompt, description } }),
    backgroundBash: null,
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
  },
  [AgentProvider.KIMI_CODE]: {
    // Each argument shape below is the tool's own JSON Schema, which Kimi Code
    // sends in every model request. Each schema sets
    // `additionalProperties: false`, so an extra field fails the call.
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
    // `multi_select` in snake case, and an option takes a label and a
    // description only: the schema refuses the `preview` that Claude takes.
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
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'coder' } }),
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
      arguments: { todos: steps.map(({ step, status }) => ({ title: step, status: status === 'completed' ? 'done' : status })) },
    }),
    // Outside Never Ask, the server raises a goal-start approval for this call.
    createGoal: (id, objective) => ({ id, name: 'CreateGoal', arguments: { objective } }),
    // `status` takes `active`, `complete` or `blocked`.
    completeGoal: id => ({ id, name: 'UpdateGoal', arguments: { status: 'complete' } }),
    blockGoal: null,
    mcpTool: null,
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
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
  },
  // Every MiMo input field is snake_case, as the tool schemas in MiMo Code 0.1.14's
  // own model request state. The names differ from OpenCode's in two places that
  // matter: `task` is the to-do tool, and `actor` spawns a subagent.
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
    // `run` BLOCKS until the subagent reports, so the parent's next scripted turn
    // reads the report. `spawn` returns at once and wakes the parent later with a
    // notification turn of its own, which no script could place in order.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'actor',
      arguments: { operation: { action: 'run', subagent_type: 'general', description, prompt } },
    }),
    backgroundBash: null,
    // The to-do tool acts on ONE item for each call, so it cannot write a list in
    // one call. `mimoTaskToolCall` below states each operation.
    updateTodos: null,
    // MiMo's goal is a session setting that the user writes, not a model tool.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
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
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
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
    exitPlanModeFromFile: null,
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose' } }),
    backgroundBash: null,
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: 'mcp', arguments: { tool: `${server}_${tool}`, args: input } }),
  },
  [AgentProvider.GROK_BUILD]: {
    // Read off the tool schemas in Grok Build 1.0.41's own model request:
    // `description` is REQUIRED beside `command`.
    bash: (id, command) => ({ id, name: 'run_terminal_command', arguments: { command, description: 'Run the scripted command' } }),
    edit: (id, { path, before, after }) => ({ id, name: 'search_replace', arguments: { file_path: path, old_string: before, new_string: after } }),
    write: (id, { path, content }) => ({ id, name: 'write', arguments: { file_path: path, content } }),
    read: (id, path) => ({ id, name: 'read_file', arguments: { target_file: path } }),
    enterPlanMode: id => ({ id, name: 'enter_plan_mode', arguments: {} }),
    // Grok's `exit_plan_mode` takes NO argument: the plan is the markdown file
    // the model writes while in plan mode, and Grok reads it when the call runs.
    // The plan text a test passes therefore never reaches the approval.
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
    backgroundBash: (id, command) => ({ id, name: 'run_terminal_command', arguments: { command, description: 'Run the scripted command in the background', background: true } }),
    // `merge: false` replaces the list, so the scripted steps are the whole list.
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_write',
      arguments: { merge: false, todos: steps.map(({ step, status }, index) => ({ id: String(index + 1), content: step, status })) },
    }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: (id, { server, tool, input }) => ({ id, name: 'use_tool', arguments: { tool_name: `${server}__${tool}`, tool_input: input } }),
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
    // Qwen 0.24 runs an agent in the BACKGROUND unless the call says otherwise,
    // so the flag is always stated.
    spawnSubagent: (id, { description, prompt, background }) => ({
      id,
      name: 'agent',
      arguments: { description, prompt, subagent_type: 'general-purpose', run_in_background: background ?? false },
    }),
    backgroundBash: (id, command) => ({ id, name: 'run_shell_command', arguments: { command, description: 'Run the scripted command in the background', is_background: true } }),
    updateTodos: (id, steps) => ({
      id,
      name: 'todo_write',
      arguments: { todos: steps.map(({ step, status }, index) => ({ id: String(index + 1), content: step, status })) },
    }),
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
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
    // `user_input` asks ONE question, and only in a spec mode, which offers the
    // tool. Its options are objects with a title. It has no header, so the builder
    // leaves the header out, and it has no multi-select, so the builder refuses one.
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
    // `name` states an agent that Kiro bundles. `context-gatherer` is the one the
    // default mode offers, and `explanation` is the reason the row states.
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'invoke_sub_agent', arguments: { name: 'context-gatherer', prompt, explanation: description } }),
    // Kiro starts a background process through its `Control Process` tool, and no
    // probe recorded the name that the model calls it by. No spec runs one, so the
    // vocabulary states none rather than a shape that nothing verified.
    backgroundBash: null,
    // `create` states the tasks, and each new task starts open. Kiro has no status
    // for a task that runs, and only a later `complete` marks a task done, so the
    // builder ignores the statuses that a test passes.
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
    // A step that reports an error through `send_message` fails the step, its round,
    // the loop and the run. Kiro then states the run failed, and the goal is blocked.
    blockGoal: (id, reason) => ({ id, name: 'send_message', arguments: { message: reason, severity: 'error' } }),
    // Kiro offers each tool of a server to the model as `mcp_<server>_<tool>`, with
    // the tool's own arguments.
    mcpTool: (id, { server, tool, input }) => ({ id, name: `mcp_${server}_${tool}`, arguments: input }),
  },
  // omp's own tool names and argument shapes (`tools/*.ts` in oh-my-pi, and the
  // probes of omp 18.2.11). The E2E profile sets `edit.mode: replace`, whose edit
  // takes the text before and after; the default hashline edit addresses lines by a
  // hash of the file that a scripted turn cannot compute.
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
    // `task` takes a list of tasks and runs each as a subagent of the bundled `task`
    // agent. The NAME becomes the subagent's id, which must be an identifier.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: 'task',
      arguments: { context: description, tasks: [{ name: identifierFrom(description), agent: 'task', task: prompt }] },
    }),
    backgroundBash: null,
    // `todo` opens a list with `init`, grouped in phases, and marks its first task
    // in progress itself: `init` states no status, so the statuses of the steps a
    // test passes are omp's to choose.
    updateTodos: (id, steps) => ({ id, name: 'todo', arguments: { op: 'init', list: [{ phase: 'Plan', items: steps.map(step => step.step) }] } }),
    // omp starts goal mode from its terminal UI only; RPC offers no goal tool.
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
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
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
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
    spawnSubagent: (id, { description, prompt }) => ({ id, name: 'Agent', arguments: { description, prompt, subagent_type: 'general-purpose' } }),
    backgroundBash: null,
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    mcpTool: null,
  },
  // Amp's own tool names and argument shapes, from the tool code of the Amp CLI
  // and the probes of the real one. The model calls a tool through the mock's Amp
  // surface, which leases every tool but a subagent to the CLI's executor -- so
  // each call below runs for real in the agent's working directory, behind the
  // LeapMux permission helper.
  [AgentProvider.AMP]: {
    bash: (id, command) => ({ id, name: AMP_SHELL_TOOL.ShellCommand, arguments: { command } }),
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
    // Amp moves a command to the background when the command outlives `timeout_ms`.
    // A zero wait races the command's spawn, and Amp then returns before it knows the
    // process, so the wait is one second.
    backgroundBash: (id, command) => ({ id, name: AMP_SHELL_TOOL.ShellCommand, arguments: { command, timeout_ms: 1_000 } }),
    // Amp's agent modes have no to-do tool, and Amp has no session goal.
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    // No test drives a Model Context Protocol tool through Amp.
    mcpTool: null,
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
    // `switch_to_act_mode` takes NO argument: the plan is the answer that the model
    // wrote before the call, and the approval shows that answer. The plan text a
    // test passes therefore never reaches the approval. The worker offers the tool
    // in Plan mode alone.
    exitPlanMode: id => ({ id, name: CLINE_TOOL.SwitchToActMode, arguments: {} }),
    exitPlanModeFromFile: null,
    // `ask_question` asks ONE question with 2 to 5 options, each a bare label. It has
    // no header and no multi-select, so the builder refuses what the tool cannot say.
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
    // `spawn_agent` takes a system prompt and a task, and no label. LeapMux titles the
    // subagent's row with the first line of the task, so the description leads the
    // task and the marked prompt follows it.
    spawnSubagent: (id, { description, prompt }) => ({
      id,
      name: CLINE_TOOL.SpawnAgent,
      arguments: { systemPrompt: 'You are a subagent. Do the task, then report the result.', task: `${description}\n\n${prompt}` },
    }),
    // Cline's commands run in the foreground of their call.
    backgroundBash: null,
    // Cline has no to-do list and no session goal.
    updateTodos: null,
    createGoal: null,
    completeGoal: null,
    blockGoal: null,
    // No test drives a Model Context Protocol tool through Cline.
    mcpTool: null,
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

/** Mark the session goal blocked with reason, in the provider's own tool. */
export function blockGoalToolCall(provider: AgentProvider, id: string, reason: string): MockModelToolCall {
  return requireBuilder(vocabulary(provider).blockGoal, provider, 'goal block')(id, reason)
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

/** Leave plan mode and raise the plan file that the model wrote, offering choices. */
export function exitPlanModeFromFileToolCall(provider: AgentProvider, id: string, approaches: PlanApproachRequest[]): MockModelToolCall {
  return requireBuilder(vocabulary(provider).exitPlanModeFromFile, provider, 'exit plan mode from a plan file')(id, approaches)
}

/**
 * One operation of MiMo Code's to-do tool, `task`.
 *
 * MiMo keeps a list of work items, and each call creates one item or moves one
 * item that an earlier call created. The ids are MiMo's own (`T1`, `T2`, ...), in
 * the order of creation.
 */
export type MiMoTaskOperation
  = | { action: 'create', summary: string }
    | { action: 'start' | 'done' | 'abandon', id: string }

/**
 * One run of MiMo Code's workflow tool. The script calls `agent(...)` for each
 * subagent of the run. The tool is experimental, and the E2E environment turns it
 * on (`MIMOCODE_EXPERIMENTAL_WORKFLOW_TOOL`).
 */
export function mimoWorkflowToolCall(id: string, script: string): MockModelToolCall {
  return { id, name: 'workflow', arguments: { operation: 'run', script } }
}

/**
 * An Oh My Pi subagent's `yield`, which hands its report to the parent as `data` and
 * ends the subagent's run. omp offers the tool to a subagent alone.
 */
export function ohMyPiYieldToolCall(id: string, report: string): MockModelToolCall {
  return { id, name: 'yield', arguments: { data: report } }
}

/** One call of MiMo Code's to-do tool. */
export function mimoTaskToolCall(id: string, operation: MiMoTaskOperation): MockModelToolCall {
  return { id, name: 'task', arguments: { operation } }
}

/**
 * A MiMo Code shell command that waits for keyboard input.
 *
 * MiMo's shell tool takes `interactive: true` for a command such as a password
 * prompt, and then asks the client for the input. LeapMux refuses that request at
 * once, because no user can type into the command.
 */
export function mimoInteractiveBashToolCall(id: string, command: string): MockModelToolCall {
  return { id, name: 'bash', arguments: { command, description: 'Run the scripted interactive command', interactive: true } }
}

/**
 * Kiro's plan-mode exit: the model hands the plan to the execution mode.
 *
 * Kiro raises no approval for the plan -- the switch runs at once and the session
 * leaves plan mode -- so it is no `exitPlanMode`, whose contract is an approval.
 */
export function kiroSwitchToExecutionToolCall(id: string, plan: string): MockModelToolCall {
  return { id, name: 'switch_to_execution', arguments: { plan } }
}

/**
 * Mark tasks of Kiro's to-do list done.
 *
 * Kiro numbers the tasks of a list from 1, in the order `create` stated them, and a
 * task is done or not: `complete` is the one call that changes a task's state.
 */
export function kiroCompleteTodosToolCall(id: string, taskIds: string[]): MockModelToolCall {
  return { id, name: 'todo_list', arguments: { command: 'complete', completed_task_ids: taskIds, context_update: 'The scripted tasks are done.' } }
}

/** Whether a provider offers a tool for one operation. */
export function hasToolFor(provider: AgentProvider, operation: keyof ProviderToolVocabulary): boolean {
  return vocabulary(provider)[operation] !== null
}
