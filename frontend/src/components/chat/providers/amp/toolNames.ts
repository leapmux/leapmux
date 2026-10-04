/**
 * Amp's own tool-name vocabulary, which only the browser reads.
 *
 * A LEAF module: data alone, so a test that runs without a DOM can read it, and the
 * E2E tool vocabulary reads the same names.
 *
 * The worker reads none of these names. Two groups of names that the worker reads
 * too live in `~/generated/contracts/amp-protocol` instead, because the worker opens
 * a registry row for their calls:
 *
 *   - `AMP_SUBAGENT_TOOL`: the four tools that Amp runs as a subagent.
 *   - `AMP_SHELL_TOOL`: `shell_command`, `shell_command_status` and
 *     `shell_command_kill`, which start and follow a background command.
 *
 * With those two groups, the list holds the tools that Amp's local executor runs and
 * the server tools whose calls a transcript shows. A tool that is in none of them -- a
 * tool of a plugin, of a Model Context Protocol server, or of a later Amp -- keeps the
 * generic card.
 */
export const AMP_TOOL_NAME = {
  /** The name under which the executor runs `shell_command`. */
  AsyncShellCommand: 'async_shell_command',
  /** The shell tool of the modes that predate `shell_command`. */
  Bash: 'Bash',
  /** Edits files through one `*** Begin Patch` text. */
  ApplyPatch: 'apply_patch',
  /** Replaces one string in a file. */
  EditFile: 'edit_file',
  /** Writes a whole file. */
  CreateFile: 'create_file',
  /** Removes a file. */
  DeleteFile: 'delete_file',
  /** Reads a file or lists a directory. */
  Read: 'Read',
  /** Shows an image or a video file to the model. */
  ViewMedia: 'view_media',
  /** Searches file contents. */
  Grep: 'Grep',
  /** Lists files by a glob pattern. */
  Glob: 'glob',
  /** The second name of the glob tool. */
  GlobAlias: 'Glob',
  /** Searches the web. */
  WebSearch: 'web_search',
  /** Reads a web page. */
  ReadWebPage: 'read_web_page',
  /** Makes or edits an image. */
  Painter: 'painter',
  /** Loads a skill. */
  Skill: 'skill',
  /** Waits for a time. */
  Sleep: 'sleep',
} as const

export type AmpToolName = typeof AMP_TOOL_NAME[keyof typeof AMP_TOOL_NAME]
