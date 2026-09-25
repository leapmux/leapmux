import type { ToolKind } from '../../model/toolKind'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'

/**
 * The shared tool kind each MiMo Code tool declares.
 *
 * MiMo reports a tool by NAME alone, so this table is where the name becomes the
 * closed kind that drives the icon, the label, the title and the input summary. Two
 * names differ from the OpenCode names they resemble, and the table is why the
 * OpenCode adapter cannot read this provider: `task` is MiMo's to-do tool, and `actor`
 * is the one that starts a subagent.
 *
 * Three tools hold several operations under one name -- `actor`, `workflow` and
 * `cron` -- and the kind here is the one most of their operations take.
 * `mimoCallKind` in `extractors/toolCall.ts` moves the few operations that act
 * differently.
 *
 * A Map rather than an object: a Model Context Protocol tool may be called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const MIMO_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [MIMO_TOOL.Question, 'question'],
  [MIMO_TOOL.Bash, 'execute'],
  [MIMO_TOOL.Read, 'read'],
  [MIMO_TOOL.Glob, 'glob'],
  [MIMO_TOOL.Grep, 'grep'],
  [MIMO_TOOL.Edit, 'edit'],
  [MIMO_TOOL.MultiEdit, 'edit'],
  [MIMO_TOOL.Write, 'write'],
  [MIMO_TOOL.NotebookEdit, 'edit'],
  [MIMO_TOOL.ApplyPatch, 'edit'],
  // It reads an image file for the model, which is a read of that file.
  [MIMO_TOOL.ViewImage, 'read'],
  [MIMO_TOOL.Actor, 'agent'],
  [MIMO_TOOL.Task, 'todo'],
  [MIMO_TOOL.WebFetch, 'fetch'],
  [MIMO_TOOL.WebSearch, 'web_search'],
  // Each of these queries a corpus the session holds rather than the open web: an
  // indexed code corpus, the skill registry, the language server, the tool registry,
  // and the session history.
  [MIMO_TOOL.CodeSearch, 'search'],
  [MIMO_TOOL.SkillSearch, 'search'],
  [MIMO_TOOL.Lsp, 'search'],
  [MIMO_TOOL.McpToolSearch, 'search'],
  [MIMO_TOOL.History, 'search'],
  [MIMO_TOOL.Skill, 'skill'],
  // The plan tool asks to leave plan mode, which is the change the Agent Client
  // Protocol spells `switch_mode`.
  [MIMO_TOOL.PlanExit, 'switch_mode'],
  [MIMO_TOOL.Memory, 'memory'],
  [MIMO_TOOL.Cron, 'trigger'],
  // A workflow run is a background task of the session, as ZCode's are.
  [MIMO_TOOL.Workflow, 'task'],
  // The experimental orchestrator's tool drives other sessions.
  [MIMO_TOOL.Session, 'agents'],
  // A script of tool calls, in JavaScript.
  [MIMO_TOOL.Exec, 'execute'],
  // The call that named no real tool, or broke its tool's schema. Only its arguments
  // and the error beside them identify it.
  [MIMO_TOOL.Invalid, 'other'],
])

/** The kind of one MiMo tool. An empty name states no kind; an unknown one is uncategorized. */
export function mimoToolKind(toolName: string): ToolKind {
  return MIMO_TOOL_KINDS.get(toolName) ?? (toolName ? 'other' : 'unspecified')
}
