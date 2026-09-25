/**
 * Cline's own tool-name vocabulary, which only the browser reads.
 *
 * A LEAF module: data alone, so a test that runs without a DOM can read it, and the
 * E2E tool vocabulary reads the same names.
 *
 * The worker reads none of these names. The four names that the worker reads too --
 * `run_commands`, `spawn_agent`, `ask_question` and `switch_to_act_mode` -- live in
 * `~/generated/contracts/cline-protocol` as `CLINE_TOOL` instead: the worker streams
 * the command's output, opens a subagent's row, answers the question executor, and
 * rebuilds the session after the plan tool.
 *
 * The list holds Cline's built-in tools, as `sdk/packages/core/src/extensions/tools`
 * of Cline 3.0.64 states them, and the tools of its agent teams. A tool that is in
 * none of them -- a tool of a Model Context Protocol server, or of a later Cline --
 * keeps the generic card.
 */
export const CLINE_TOOL_NAME = {
  /** Reads files or line ranges of files. */
  ReadFiles: 'read_files',
  /** Searches the workspace with regular expressions. */
  SearchCodebase: 'search_codebase',
  /** Reads web pages. */
  FetchWebContent: 'fetch_web_content',
  /** Replaces, inserts or creates text in one file. */
  Editor: 'editor',
  /** Edits files through one `*** Begin Patch` text. */
  ApplyPatch: 'apply_patch',
  /** Runs a skill. */
  Skills: 'skills',
  /** Ends an unattended run with a summary. */
  SubmitAndExit: 'submit_and_exit',
  /** The question tool of earlier Cline releases. */
  AskFollowupQuestion: 'ask_followup_question',
  /** Manages the agenda's scheduled tasks. */
  Tasks: 'tasks',
  /** Starts a teammate. */
  TeamSpawnTeammate: 'team_spawn_teammate',
  /** Stops a teammate. */
  TeamShutdownTeammate: 'team_shutdown_teammate',
  /** States the team. */
  TeamStatus: 'team_status',
  /** Manages the team's tasks. */
  TeamTask: 'team_task',
  /** Hands a task to a teammate to run in the background. */
  TeamRunTask: 'team_run_task',
  /** Cancels a teammate run. */
  TeamCancelRun: 'team_cancel_run',
  /** Lists the teammate runs. */
  TeamListRuns: 'team_list_runs',
  /** Waits for teammate runs to end. */
  TeamAwaitRuns: 'team_await_runs',
  /** Sends a message to one teammate. */
  TeamSendMessage: 'team_send_message',
  /** Sends a message to every teammate. */
  TeamBroadcast: 'team_broadcast',
  /** Reads the messages sent to the lead. */
  TeamReadMailbox: 'team_read_mailbox',
  /** Records or reads the team's mission log. */
  TeamMissionLog: 'team_mission_log',
  /** Ends the team. */
  TeamCleanup: 'team_cleanup',
  /** Starts an outcome the team writes together. */
  TeamCreateOutcome: 'team_create_outcome',
  /** Adds a part to an outcome. */
  TeamAttachOutcomeFragment: 'team_attach_outcome_fragment',
  /** Reviews a part of an outcome. */
  TeamReviewOutcomeFragment: 'team_review_outcome_fragment',
  /** Finishes an outcome. */
  TeamFinalizeOutcome: 'team_finalize_outcome',
  /** Lists the outcomes. */
  TeamListOutcomes: 'team_list_outcomes',
} as const

export type ClineToolName = typeof CLINE_TOOL_NAME[keyof typeof CLINE_TOOL_NAME]
