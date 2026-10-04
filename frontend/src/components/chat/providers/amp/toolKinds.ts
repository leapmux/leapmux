import type { ToolKind } from '../../model/toolKind'
import { AMP_SHELL_TOOL, AMP_SUBAGENT_TOOL } from '~/generated/contracts/amp-protocol'
import { AMP_TOOL_NAME } from './toolNames'

/**
 * The shared tool kind each Amp tool declares.
 *
 * Amp reports a tool by NAME alone, so this table is where the name becomes the closed
 * kind that drives the icon, the label, the title and the input summary. A name that
 * the table does not list is a tool of a plugin, of a Model Context Protocol server,
 * or of a later Amp, and it keeps the generic card.
 *
 * A Map rather than an object, because a plugin can register a tool called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const AMP_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [AMP_SHELL_TOOL.ShellCommand, 'execute'],
  [AMP_TOOL_NAME.AsyncShellCommand, 'execute'],
  [AMP_TOOL_NAME.Bash, 'execute'],
  // Both manage a command that `shell_command` moved to the background, by its pid.
  [AMP_SHELL_TOOL.ShellCommandStatus, 'task'],
  [AMP_SHELL_TOOL.ShellCommandKill, 'task'],
  [AMP_TOOL_NAME.ApplyPatch, 'edit'],
  [AMP_TOOL_NAME.EditFile, 'edit'],
  [AMP_TOOL_NAME.CreateFile, 'write'],
  [AMP_TOOL_NAME.DeleteFile, 'delete'],
  [AMP_TOOL_NAME.Read, 'read'],
  // It reads a file for the model, as `Read` does, but the file is a picture or a video.
  [AMP_TOOL_NAME.ViewMedia, 'read'],
  [AMP_TOOL_NAME.Grep, 'grep'],
  [AMP_TOOL_NAME.Glob, 'glob'],
  [AMP_TOOL_NAME.GlobAlias, 'glob'],
  [AMP_TOOL_NAME.WebSearch, 'web_search'],
  [AMP_TOOL_NAME.ReadWebPage, 'fetch'],
  [AMP_TOOL_NAME.Painter, 'image'],
  [AMP_TOOL_NAME.Skill, 'skill'],
  [AMP_TOOL_NAME.Sleep, 'wait'],
  // The four tools that Amp runs as a subagent on its server. Each opens a registry row.
  [AMP_SUBAGENT_TOOL.Task, 'agent'],
  [AMP_SUBAGENT_TOOL.Oracle, 'agent'],
  [AMP_SUBAGENT_TOOL.Librarian, 'agent'],
  [AMP_SUBAGENT_TOOL.Finder, 'agent'],
])

/**
 * The tool kind one Amp tool declares, or the unspecified kind for a name it does not
 * know.
 *
 * The empty answer is what `toolVocabulary.test.ts` reads to find a tool the table
 * forgot, so it stays the literal table lookup. No ROW carries it: `ampToolCallKind`
 * gives an unknown tool the `mcp` kind that matches the card it draws.
 */
export function ampToolKind(toolName: string): ToolKind {
  return AMP_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
