// Oh My Pi wire words that only the browser reads. A word that the worker reads too
// lives in `~/generated/contracts/ohmypi-protocol`.

/**
 * The prefix of the `customType` of each agent-to-agent message omp records:
 * `irc:incoming`, `irc:autoreply`, `irc:relay` and `irc:workpool`.
 */
export const OH_MY_PI_IRC_CUSTOM_TYPE_PREFIX = 'irc:'

/** The `customType` of each agent-to-agent message that omp records. */
export const OH_MY_PI_IRC_CUSTOM_TYPE = {
  Incoming: 'irc:incoming',
  AutoReply: 'irc:autoreply',
  Relay: 'irc:relay',
  WorkPool: 'irc:workpool',
} as const

/** The `customType` of the message that holds the file of a skill the reader called. */
export const OH_MY_PI_SKILL_PROMPT_CUSTOM_TYPE = 'skill-prompt'

/** The `type` of a background job that runs a subagent, in the job list of an async-result message. */
export const OH_MY_PI_ASYNC_JOB_TYPE_TASK = 'task'

/** The prefix of a Model Context Protocol tool name: `mcp__<server>_<tool>`. */
export const OH_MY_PI_MCP_TOOL_PREFIX = 'mcp__'

/** The scheme of an omp device path, which `read` and `write` dispatch to a device tool. */
export const OH_MY_PI_DEVICE_SCHEME = 'xd://'

/** The `details.kind` of a `read` whose path was a URL. */
export const OH_MY_PI_READ_KIND_URL = 'url'

/** The language words of an `eval` cell. */
export const OH_MY_PI_EVAL_LANGUAGE = {
  JavaScript: 'js',
  Python: 'py',
} as const

/**
 * The status omp's `yield` tool states for a subagent that gave up: the call states
 * an `error` instead of a result.
 */
export const OH_MY_PI_YIELD_STATUS_ABORTED = 'aborted'

/** The `op` of each `hub` operation that the browser reads apart from the others. */
export const OH_MY_PI_HUB_OP = {
  Send: 'send',
  Wait: 'wait',
  Start: 'start',
  Ps: 'ps',
  Logs: 'logs',
  Stop: 'stop',
  Restart: 'restart',
  Describe: 'describe',
} as const

/** The `notifyType` of an extension notice that is more than plain information. */
export const OH_MY_PI_NOTIFY_TYPE = {
  Warning: 'warning',
  Error: 'error',
} as const
