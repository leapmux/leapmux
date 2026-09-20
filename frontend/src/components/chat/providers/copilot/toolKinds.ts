import type { ToolKind } from '../../model/toolKind'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'

/**
 * The shared presentation kind for one native tool.
 *
 * The names are Copilot's own, read from `session.tools.getBuiltinDescriptors` on the
 * installed runtime. A tool this table does not list is a Model Context Protocol
 * tool, an extension tool, or one a later release adds -- and each of those draws
 * the shared card, so the row states `mcp` rather than the uncategorized kind whose
 * wrench identifies nothing the agent ran. `toolVocabulary.test.ts` fails the suite when
 * a name the CONTRACT holds reaches that fallback.
 */
const COPILOT_TOOL_KINDS: Record<string, ToolKind> = {
  [COPILOT_TOOL.View]: 'read',
  [COPILOT_TOOL.Create]: 'write',
  [COPILOT_TOOL.Edit]: 'edit',
  [COPILOT_TOOL.StrReplaceEditor]: 'edit',
  [COPILOT_TOOL.ApplyPatch]: 'edit',
  [COPILOT_TOOL.Bash]: 'execute',
  // The four calls that manage a background shell by id: each one identifies a shell the
  // session already started and carries no command of its own, which is `task`.
  [COPILOT_TOOL.ReadBash]: 'task',
  [COPILOT_TOOL.ListBash]: 'task',
  [COPILOT_TOOL.StopBash]: 'task',
  [COPILOT_TOOL.WriteBash]: 'task',
  [COPILOT_TOOL.Glob]: 'glob',
  [COPILOT_TOOL.Grep]: 'grep',
  [COPILOT_TOOL.WritePowerShell]: 'execute',
  [COPILOT_TOOL.LocalShell]: 'execute',
  [COPILOT_TOOL.Sql]: 'execute',
  [COPILOT_TOOL.SessionStoreSql]: 'execute',
  [COPILOT_TOOL.StrReplace]: 'edit',
  [COPILOT_TOOL.Delete]: 'delete',
  [COPILOT_TOOL.Move]: 'move',
  [COPILOT_TOOL.WebFetch]: 'fetch',
  [COPILOT_TOOL.FetchDocumentation]: 'fetch',
  [COPILOT_TOOL.WebSearch]: 'web_search',
  [COPILOT_TOOL.SearchCodeSubagent]: 'search',
  [COPILOT_TOOL.Task]: 'agent',
  [COPILOT_TOOL.UpdateTodo]: 'todo',
  // A question asks the reader, which is not a thought.
  [COPILOT_TOOL.AskUser]: 'question',
  // Leaving plan mode changes the mode the session runs in, which the Agent Client
  // Protocol spells `switch_mode` and Cursor sends under that exact word.
  [COPILOT_TOOL.ExitPlanMode]: 'switch_mode',
  // Both tool-search names find a TOOL rather than a file, which is still a search.
  [COPILOT_TOOL.ToolSearch]: 'search',
  [COPILOT_TOOL.GenericToolSearch]: 'search',
  // A language-server query answers a symbol, a definition or a diagnostic. Each
  // one is a lookup over the code index, which is what `search` says.
  [COPILOT_TOOL.Lsp]: 'search',
  // The session's own extensibility surface: a packaged skill, and the two calls
  // that install, remove or reload one.
  [COPILOT_TOOL.Skill]: 'skill',
  [COPILOT_TOOL.ExtensionsManage]: 'skill',
  [COPILOT_TOOL.ExtensionsReload]: 'skill',
  // Two calls that report state and touch nothing: the turn's own completion
  // summary, and a progress note part-way through it.
  [COPILOT_TOOL.TaskComplete]: 'report',
  [COPILOT_TOOL.ReportProgress]: 'report',
  // The scratch board the agent keeps for itself, across turns.
  [COPILOT_TOOL.ContextBoard]: 'memory',
  [COPILOT_TOOL.ListAgents]: 'agents',
  // Reading a subagent's report observes a run the transcript already opened, and
  // writing to one sends it a follow-up. Neither is a file read or a file write.
  [COPILOT_TOOL.ReadAgent]: 'task',
  [COPILOT_TOOL.WriteAgent]: 'message',
}

export function copilotToolKind(toolName: string): ToolKind {
  // `Object.hasOwn`, not `??`: a Model Context Protocol server chooses its own tool names,
  // and `constructor` or `toString` answers from `Object.prototype` -- a truthy
  // value, so the `mcp` fallback below would never run for it. The `??` on the OWN
  // read alone is for the type system; every own value in the table is a `ToolKind`.
  const own = Object.hasOwn(COPILOT_TOOL_KINDS, toolName) ? COPILOT_TOOL_KINDS[toolName] : undefined
  return own ?? 'mcp'
}
