import type { ToolKind } from '../../model/toolKind'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'

/**
 * The shared tool kind each Codewhale tool declares.
 *
 * Codewhale reports a tool by NAME, in the item's `metadata.tool_name`. The item's
 * own `kind` is a name heuristic of the runtime -- `bash` arrives as `tool_call` and
 * `todo_write` as `file_change` -- so this table, not the item kind, is where the
 * name becomes the closed kind that drives the icon, the label, the title and the
 * input summary.
 *
 * Every tool `CODEWHALE_TOOL` lists reaches a kind, so only a tool from a later
 * release takes the uncategorized row; `toolVocabulary.test.ts` is the guard. Four
 * facade tools -- `File`, `Web`, `Git` and `Run` -- state their real operation in an
 * `action` argument, and `codewhaleReclassify` refines their kind from it.
 *
 * A Map rather than an object: a Model Context Protocol tool may be called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const CODEWHALE_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  // Files. The legacy spellings are hidden aliases the runtime still accepts.
  [CODEWHALE_TOOL.Read, 'read'],
  [CODEWHALE_TOOL.ReadFile, 'read'],
  [CODEWHALE_TOOL.Write, 'write'],
  [CODEWHALE_TOOL.WriteFile, 'write'],
  [CODEWHALE_TOOL.Edit, 'edit'],
  [CODEWHALE_TOOL.EditFile, 'edit'],
  [CODEWHALE_TOOL.ApplyPatch, 'edit'],
  [CODEWHALE_TOOL.FimEdit, 'edit'],
  [CODEWHALE_TOOL.ListDir, 'list'],
  [CODEWHALE_TOOL.ProjectMap, 'list'],
  [CODEWHALE_TOOL.FileSearch, 'glob'],
  [CODEWHALE_TOOL.GrepFiles, 'grep'],
  // The facade reads a file unless its `action` says otherwise.
  [CODEWHALE_TOOL.File, 'read'],
  // A picture or a document read from a path, and the text read out of an image.
  [CODEWHALE_TOOL.ReadMedia, 'read'],
  [CODEWHALE_TOOL.ImageOcr, 'read'],
  [CODEWHALE_TOOL.ImageAnalyze, 'read'],
  // A stored value the runtime hands back by reference.
  [CODEWHALE_TOOL.HandleRead, 'read'],
  [CODEWHALE_TOOL.RetrieveToolResult, 'read'],

  // Commands. Each of these runs a process in the workspace.
  [CODEWHALE_TOOL.Bash, 'execute'],
  [CODEWHALE_TOOL.LegacyBash, 'execute'],
  [CODEWHALE_TOOL.TaskShellStart, 'execute'],
  [CODEWHALE_TOOL.TerminalRun, 'execute'],
  [CODEWHALE_TOOL.TerminalSend, 'execute'],
  [CODEWHALE_TOOL.CodeExecution, 'execute'],
  [CODEWHALE_TOOL.JsExecution, 'execute'],
  [CODEWHALE_TOOL.PandocConvert, 'execute'],
  [CODEWHALE_TOOL.Git, 'execute'],
  [CODEWHALE_TOOL.GitStatus, 'execute'],
  [CODEWHALE_TOOL.GitDiff, 'execute'],
  [CODEWHALE_TOOL.GitLog, 'execute'],
  [CODEWHALE_TOOL.GitShow, 'execute'],
  [CODEWHALE_TOOL.GitBlame, 'execute'],
  [CODEWHALE_TOOL.Run, 'execute'],
  [CODEWHALE_TOOL.RunTests, 'execute'],
  [CODEWHALE_TOOL.RunVerifiers, 'execute'],

  // Background work the transcript already opened: a shell job, a terminal, a
  // durable task, a server the runtime started, or a workflow run.
  [CODEWHALE_TOOL.TaskShellWait, 'task'],
  [CODEWHALE_TOOL.TerminalWait, 'task'],
  [CODEWHALE_TOOL.TerminalCancel, 'task'],
  [CODEWHALE_TOOL.TerminalReset, 'task'],
  [CODEWHALE_TOOL.Tasks, 'task'],
  [CODEWHALE_TOOL.Workflow, 'task'],
  [CODEWHALE_TOOL.StartMcpServer, 'task'],
  [CODEWHALE_TOOL.StartRegistryMcpServer, 'task'],

  // Work that fires later.
  [CODEWHALE_TOOL.Automation, 'trigger'],
  [CODEWHALE_TOOL.SendLater, 'trigger'],

  // Subagents: one tool launches and manages them, the rest coordinate them.
  [CODEWHALE_TOOL.Agent, 'agent'],
  [CODEWHALE_TOOL.AgentsCoordinate, 'agents'],
  [CODEWHALE_TOOL.AgentsList, 'agents'],
  [CODEWHALE_TOOL.AgentsInterrupt, 'agents'],
  [CODEWHALE_TOOL.AgentsFollowup, 'message'],
  [CODEWHALE_TOOL.AgentsMessage, 'message'],
  [CODEWHALE_TOOL.AgentsWait, 'wait'],
  // A notice to the reader is a message to a peer outside the session.
  [CODEWHALE_TOOL.Notify, 'message'],

  // Checklists and plans. `update_plan` states its steps as `plan[{step, status}]`,
  // which `codewhaleTodoItems` reads beside the `todos` list of the others.
  [CODEWHALE_TOOL.TodoWrite, 'todo'],
  [CODEWHALE_TOOL.LegacyTodoWrite, 'todo'],
  [CODEWHALE_TOOL.Todo, 'todo'],
  [CODEWHALE_TOOL.ChecklistWrite, 'todo'],
  [CODEWHALE_TOOL.ChecklistUpdate, 'todo'],
  [CODEWHALE_TOOL.UpdatePlan, 'todo'],
  [CODEWHALE_TOOL.WorkUpdate, 'todo'],

  // The web. The facade searches unless its `action` says otherwise.
  [CODEWHALE_TOOL.FetchURL, 'fetch'],
  [CODEWHALE_TOOL.WebSearch, 'web_search'],
  [CODEWHALE_TOOL.Web, 'web_search'],
  [CODEWHALE_TOOL.WebRun, 'web_search'],
  [CODEWHALE_TOOL.WaitForDevServer, 'wait'],

  // Queries against a corpus the session holds: the tool registry, and the
  // language-server index.
  [CODEWHALE_TOOL.ToolSearch, 'search'],
  [CODEWHALE_TOOL.Lsp, 'search'],

  // Long-lived memory and the archive of earlier sessions.
  [CODEWHALE_TOOL.Remember, 'memory'],
  [CODEWHALE_TOOL.MemorySearch, 'memory'],
  [CODEWHALE_TOOL.MemoryGet, 'memory'],
  [CODEWHALE_TOOL.SessionSearch, 'memory'],
  [CODEWHALE_TOOL.SessionGet, 'memory'],
  [CODEWHALE_TOOL.Note, 'memory'],

  [CODEWHALE_TOOL.LoadSkill, 'skill'],

  // Calls whose answer is a report: a goal record, a check of the workspace, a
  // review, a proposal the reader acts on. Each states free-form arguments and
  // answers with text, which is the shape the report kind declares.
  [CODEWHALE_TOOL.CreateGoal, 'report'],
  [CODEWHALE_TOOL.GetGoal, 'report'],
  [CODEWHALE_TOOL.UpdateGoal, 'report'],
  [CODEWHALE_TOOL.Diagnostics, 'report'],
  [CODEWHALE_TOOL.ValidateData, 'report'],
  [CODEWHALE_TOOL.Review, 'report'],
  [CODEWHALE_TOOL.Verify, 'report'],
  [CODEWHALE_TOOL.Harness, 'report'],
  [CODEWHALE_TOOL.GitCommitPlan, 'report'],
  [CODEWHALE_TOOL.Github, 'report'],
  [CODEWHALE_TOOL.Finance, 'report'],
  [CODEWHALE_TOOL.Speech, 'report'],
  [CODEWHALE_TOOL.RevertTurn, 'report'],
  [CODEWHALE_TOOL.RequestPluginInstall, 'report'],
  [CODEWHALE_TOOL.RegistrySync, 'report'],
  // The two batch tools run other tools and answer with their combined report.
  [CODEWHALE_TOOL.ExecuteTools, 'report'],
  [CODEWHALE_TOOL.MultiToolUseParallel, 'report'],

  [CODEWHALE_TOOL.RequestUserInput, 'question'],
])

/**
 * The prefix of a Model Context Protocol tool's model-facing name:
 * `mcp_<server>_<tool>` (`McpPool::mcp_model_tool_name`).
 */
export const CODEWHALE_MCP_PREFIX = 'mcp_'

/**
 * The server and the tool a Model Context Protocol name states, or null for any
 * other name.
 *
 * The runtime joins the two with `_`, and a server name may hold `_` too, so the
 * split at the FIRST separator is the reading that is right for every server name
 * without one. The row's label keeps the whole name, which is never wrong.
 */
export function codewhaleMcpToolName(toolName: string): { server: string, tool: string } | null {
  if (!toolName.startsWith(CODEWHALE_MCP_PREFIX) || CODEWHALE_TOOL_KINDS.has(toolName))
    return null
  const rest = toolName.slice(CODEWHALE_MCP_PREFIX.length)
  const separator = rest.indexOf('_')
  if (separator <= 0 || separator === rest.length - 1)
    return null
  return { server: rest.slice(0, separator), tool: rest.slice(separator + 1) }
}

/** The kind of one Codewhale tool. An empty name states no kind; an unknown one is uncategorized. */
export function codewhaleToolKind(toolName: string): ToolKind {
  const kind = CODEWHALE_TOOL_KINDS.get(toolName)
  if (kind !== undefined)
    return kind
  if (codewhaleMcpToolName(toolName))
    return 'mcp'
  return toolName ? 'other' : 'unspecified'
}
