import type { ToolKind } from '../../model/toolKind'
import { CLINE_TOOL, CLINE_TOOL_PREFIX } from '~/generated/contracts/cline-protocol'
import { CLINE_TOOL_NAME } from './toolNames'

/**
 * The shared tool kind each Cline tool declares.
 *
 * Cline reports a tool by NAME alone, so this table is where the name becomes the
 * closed kind that drives the icon, the label, the title and the input summary. A name
 * that the table does not list is a tool of a Model Context Protocol server or of a
 * later Cline, and it keeps the generic card.
 *
 * A Map rather than an object, because a server can register a tool called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const CLINE_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [CLINE_TOOL.RunCommands, 'execute'],
  [CLINE_TOOL.SpawnAgent, 'agent'],
  [CLINE_TOOL.AskQuestion, 'question'],
  [CLINE_TOOL_NAME.AskFollowupQuestion, 'question'],
  [CLINE_TOOL.SwitchToActMode, 'switch_mode'],
  [CLINE_TOOL_NAME.ReadFiles, 'read'],
  // A regular-expression search of the workspace's files.
  [CLINE_TOOL_NAME.SearchCodebase, 'grep'],
  [CLINE_TOOL_NAME.FetchWebContent, 'fetch'],
  [CLINE_TOOL_NAME.Editor, 'edit'],
  [CLINE_TOOL_NAME.ApplyPatch, 'edit'],
  [CLINE_TOOL_NAME.Skills, 'skill'],
  [CLINE_TOOL_NAME.SubmitAndExit, 'report'],
  // The agenda's scheduled tasks.
  [CLINE_TOOL_NAME.Tasks, 'trigger'],
  // The team tools. The ones that manage the team read as agent management, the
  // ones that run and follow a teammate's work read as a background task, the
  // messages read as messages, and the shared outcomes read as reports.
  [CLINE_TOOL_NAME.TeamSpawnTeammate, 'agents'],
  [CLINE_TOOL_NAME.TeamShutdownTeammate, 'agents'],
  [CLINE_TOOL_NAME.TeamStatus, 'agents'],
  [CLINE_TOOL_NAME.TeamCleanup, 'agents'],
  [CLINE_TOOL_NAME.TeamMissionLog, 'agents'],
  [CLINE_TOOL_NAME.TeamTask, 'task'],
  [CLINE_TOOL_NAME.TeamRunTask, 'task'],
  [CLINE_TOOL_NAME.TeamCancelRun, 'task'],
  [CLINE_TOOL_NAME.TeamListRuns, 'task'],
  [CLINE_TOOL_NAME.TeamAwaitRuns, 'task'],
  [CLINE_TOOL_NAME.TeamSendMessage, 'message'],
  [CLINE_TOOL_NAME.TeamBroadcast, 'message'],
  [CLINE_TOOL_NAME.TeamReadMailbox, 'message'],
  [CLINE_TOOL_NAME.TeamCreateOutcome, 'report'],
  [CLINE_TOOL_NAME.TeamAttachOutcomeFragment, 'report'],
  [CLINE_TOOL_NAME.TeamReviewOutcomeFragment, 'report'],
  [CLINE_TOOL_NAME.TeamFinalizeOutcome, 'report'],
  [CLINE_TOOL_NAME.TeamListOutcomes, 'report'],
])

/**
 * The tool kind one Cline tool declares, or the unspecified kind for a name it does
 * not know.
 *
 * The empty answer is what `toolVocabulary.test.ts` reads to find a tool the table
 * forgot, so it stays the literal table lookup. No ROW carries it: `clineToolCallKind`
 * gives an unknown tool the `mcp` kind that matches the card it draws.
 */
export function clineToolKind(toolName: string): ToolKind {
  // A configured agent of `.cline/agents/` is one tool for each agent,
  // `subagent_<name>_<hash>`, which runs a child as `spawn_agent` does.
  if (toolName.startsWith(CLINE_TOOL_PREFIX.ConfiguredAgent))
    return 'agent'
  return CLINE_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
