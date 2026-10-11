import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { OH_MY_PI_COMMAND, OH_MY_PI_CUSTOM_TYPE, OH_MY_PI_EVENT, OH_MY_PI_EXTENSION_METHOD, OH_MY_PI_ROLE } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { toTokenCount } from '../../../model/notification'
import {
  OH_MY_PI_ASYNC_JOB_TYPE_TASK,
  OH_MY_PI_IRC_CUSTOM_TYPE,
  OH_MY_PI_IRC_CUSTOM_TYPE_PREFIX,
  OH_MY_PI_NOTIFY_TYPE,
  OH_MY_PI_SKILL_PROMPT_CUSTOM_TYPE,
} from '../protocol'

/**
 * The trigger of an automatic compaction.
 *
 * omp states the finer reason (`threshold`, `overflow`, `idle`, `incomplete`) on the
 * START frame alone, and the end frame states none. This reader reads one frame at a
 * time, so the end frame cannot reach the start frame's reason, and it states the
 * trigger that holds for every `auto_compaction_end`.
 */
const AUTO_COMPACTION_TRIGGER = 'auto'

/** The trigger of a compaction that the worker's own `compact` command asked for. */
const MANUAL_COMPACTION_TRIGGER = 'manual'

/** What one compaction states about the context it rewrote: the trigger and the size before it. omp states no size after. */
function compactionDetail(trigger: string, result: Record<string, unknown> | null | undefined): CompactionDetails {
  const pre = toTokenCount(pickNumber(result, 'tokensBefore') ?? undefined)
  return { trigger, ...(pre !== undefined ? { pre } : {}) }
}

/**
 * Whether one `auto_compaction_end` states a compaction that rewrote the context.
 *
 * omp states a failure with no `result`, and with its own sentence in `errorMessage`
 * (`session-maintenance.ts`). An aborted or a skipped compaction rewrote nothing
 * either.
 */
function autoCompactionRewrote(msg: Record<string, unknown>): boolean {
  return msg.aborted !== true && msg.skipped !== true && !pickString(msg, 'errorMessage') && isObject(msg.result)
}

/** Whether one frame is omp's answer to the worker's `compact` command. */
function isCompactResponse(msg: Record<string, unknown>): boolean {
  return pickString(msg, 'type') === OH_MY_PI_EVENT.Response && pickString(msg, 'command') === OH_MY_PI_COMMAND.Compact
}

/**
 * The compaction boundary one omp message states, or null when it states none.
 *
 * A compaction that omp aborted, skipped or failed rewrote nothing, so the context
 * size did not move and the usage grid must not refresh from it. A `compact` command
 * omp refused ("Nothing to compact") is the same.
 */
export function ohMyPiCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const inner = getInnerMessage(parsed)
  if (!isObject(inner))
    return null
  if (pickString(inner, 'type') === OH_MY_PI_EVENT.AutoCompactionEnd)
    return autoCompactionRewrote(inner) ? compactionDetail(AUTO_COMPACTION_TRIGGER, pickObject(inner, 'result')) : null
  if (isCompactResponse(inner))
    return inner.success === true ? compactionDetail(MANUAL_COMPACTION_TRIGGER, pickObject(inner, 'data')) : null
  return null
}

/** The entry of one `auto_compaction_end`: a boundary, or the reason that it is none. */
function autoCompactionEndEntry(msg: Record<string, unknown>): NotificationEntry {
  if (msg.aborted === true)
    return { kind: 'compaction', phase: 'end', error: 'aborted' }
  if (msg.skipped === true)
    return { kind: 'status', text: 'Compaction skipped' }
  // omp words each failure as a whole sentence ("Auto-compaction failed: ...",
  // "Context overflow recovery failed: ..."), so the sentence stands alone.
  const error = pickString(msg, 'errorMessage').trim()
  if (error)
    return { kind: 'text', text: error }
  if (!isObject(msg.result))
    return { kind: 'compaction', phase: 'end', error: 'omp stated no result' }
  return { kind: 'compaction', phase: 'end', detail: compactionDetail(AUTO_COMPACTION_TRIGGER, pickObject(msg, 'result')) }
}

/** One retry frame read into the shared retry entry. */
function retryEntry(msg: Record<string, unknown>, ended: boolean): NotificationEntry {
  const attempt = pickNumber(msg, 'attempt', undefined)
  if (!ended) {
    const maxAttempts = pickNumber(msg, 'maxAttempts', undefined)
    const delayMs = pickNumber(msg, 'delayMs', undefined)
    const error = pickString(msg, 'errorMessage') || undefined
    return {
      kind: 'retry',
      scope: 'api',
      ...(attempt !== undefined ? { attempt } : {}),
      ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      ...(delayMs !== undefined ? { delayMs } : {}),
      ...(error !== undefined ? { error } : {}),
    }
  }
  // A retry that SUCCEEDED ends the stall, and the row says so. One that failed gave
  // up, which is what `willRetry: false` states.
  const succeeded = msg.success === true
  const error = succeeded ? undefined : pickString(msg, 'finalError') || undefined
  return {
    kind: 'retry',
    scope: 'api',
    ...(attempt !== undefined ? { attempt } : {}),
    ...(succeeded ? { succeeded: true } : { willRetry: false }),
    ...(error !== undefined ? { error } : {}),
  }
}

/** The text of one custom message: its content, as a string or as text blocks. */
function customText(message: Record<string, unknown>): string {
  const content = message.content
  if (typeof content === 'string')
    return content
  if (Array.isArray(content))
    return content.filter(isObject).map(block => pickString(block, 'text')).filter(Boolean).join('\n')
  return ''
}

/** The pair of tags omp wraps around a notice that it writes for the model. */
const SYSTEM_NOTICE = /^<system-notice>\n?([\s\S]*?)\n?<\/system-notice>$/

/** A notice's text without the `<system-notice>` tags that omp writes for the model. */
function withoutSystemNotice(text: string): string {
  const trimmed = text.trim()
  return (SYSTEM_NOTICE.exec(trimmed)?.[1] ?? trimmed).trim()
}

/**
 * The line one agent-to-agent message draws, from the words omp states in its
 * details. The content is a template for the model, so it is read only for a record
 * whose details state no body.
 *
 * The four record shapes are omp's own (`irc-bridge.ts`, `irc/bus.ts`,
 * `task/workpool.ts`).
 */
function ircLine(message: Record<string, unknown>): string {
  const details = pickObject(message, 'details')
  const from = pickString(details, 'from')
  const to = pickString(details, 'to')
  switch (pickString(message, 'customType')) {
    case OH_MY_PI_IRC_CUSTOM_TYPE.Incoming: {
      const body = pickString(details, 'message').trim()
      if (body)
        return from ? `Message from ${from}: ${body}` : body
      break
    }
    case OH_MY_PI_IRC_CUSTOM_TYPE.AutoReply: {
      const body = pickString(details, 'body').trim()
      if (body)
        return to ? `Automatic reply to ${to}: ${body}` : body
      break
    }
    case OH_MY_PI_IRC_CUSTOM_TYPE.Relay: {
      const body = pickString(details, 'body').trim()
      if (body)
        return from && to ? `Message from ${from} to ${to}: ${body}` : body
      break
    }
    case OH_MY_PI_IRC_CUSTOM_TYPE.WorkPool: {
      const body = pickString(details, 'body').trim()
      const pool = pickString(details, 'pool')
      if (body)
        return pool && to ? `Pool ${pool} to ${to}: ${body}` : body
      break
    }
  }
  return customText(message).trim()
}

/** One line of the heading of a job's section in a delivery of several: `── Job <id> (<label>) ──`. */
const JOB_SECTION_HEADER = /^── Job (\S+)(?: \(.*\))? ──$/

/**
 * The result of each job one background delivery states, by job id.
 *
 * omp puts each job's result in the rendered content alone (`async-result.md`), and
 * `details.jobs` holds none:
 *
 *   <system-notice>
 *   Background job <id> has completed. Resume your work using the result below.
 *   <result>
 *   </system-notice>
 *
 * A delivery of several jobs opens each result with `── Job <id> (<label>) ──`.
 */
function deliveredResults(content: string, jobIds: readonly string[]): Map<string, string> {
  const rows = withoutSystemNotice(content).replace(/\r\n/g, '\n').split('\n')
  // The first row is omp's headline for the model.
  rows.shift()
  const results = new Map<string, string>()
  if (jobIds.length === 1) {
    const only = jobIds[0]
    const result = rows.join('\n').trim()
    if (only !== undefined && result)
      results.set(only, result)
    return results
  }
  let current: string | undefined
  let lines: string[] = []
  const flush = () => {
    const result = lines.join('\n').trim()
    if (current !== undefined && result)
      results.set(current, result)
  }
  for (const row of rows) {
    const header = JOB_SECTION_HEADER.exec(row)
    if (header?.[1] !== undefined && jobIds.includes(header[1])) {
      flush()
      current = header[1]
      lines = []
      continue
    }
    lines.push(row)
  }
  flush()
  return results
}

/** A text inside a Markdown code fence that is longer than every run of backticks the text holds. */
function fenced(text: string): string {
  // A loop rather than a spread into `Math.max`: a long output holds more runs than a
  // call can take as arguments.
  let longest = 0
  for (const run of text.matchAll(/`+/g))
    longest = Math.max(longest, run[0].length)
  const fence = '`'.repeat(Math.max(3, longest + 1))
  return `${fence}\n${text}\n${fence}`
}

/**
 * The entries of the notice omp injects when background jobs finish.
 *
 * A command's output reaches the reader only here, so each command job draws its
 * output as a report under the job's label. A subagent's job is different: its
 * report reaches the subagent's own transcript and registry row, and its result here
 * is omp's `<task-result>` markup for the model, so it states only that the job
 * finished. A job whose output this build cannot find states only that, too.
 */
function backgroundDeliveryEntries(message: Record<string, unknown>): NotificationEntry[] {
  const jobs = pickObject(message, 'details')?.jobs
  const records = Array.isArray(jobs) ? jobs.filter(isObject) : []
  if (records.length === 0)
    return [{ kind: 'text', text: 'A background job finished' }]
  const results = deliveredResults(customText(message), records.map(job => pickString(job, 'jobId')))
  return records.map((job): NotificationEntry => {
    const jobId = pickString(job, 'jobId')
    const label = pickString(job, 'label') || jobId || 'job'
    const result = results.get(jobId)
    if (result === undefined || pickString(job, 'type') === OH_MY_PI_ASYNC_JOB_TYPE_TASK)
      return { kind: 'group', groupKey: 'omp-background-job', prefix: 'Background job finished', entry: label }
    return { kind: 'subagent-report', label, text: fenced(result) }
  })
}

/**
 * The entries of one custom message that omp writes for the model and displays.
 *
 * None of them is the assistant's reply, so each draws as a notice:
 *
 * - A background delivery states each job and its output.
 * - An agent-to-agent message states nothing here. omp sends it as an `irc_message`
 *   frame too, which draws it, so this copy hides.
 * - A skill's file states the skill by name. The file is the skill's instructions for
 *   the model, and the reader's own message already states the call.
 * - Every other notice states omp's own words, without the tags it writes for the
 *   model.
 */
function customMessageEntries(message: Record<string, unknown>): NotificationEntry[] {
  const customType = pickString(message, 'customType')
  if (customType === OH_MY_PI_CUSTOM_TYPE.AsyncResult)
    return backgroundDeliveryEntries(message)
  if (customType.startsWith(OH_MY_PI_IRC_CUSTOM_TYPE_PREFIX))
    return []
  if (customType === OH_MY_PI_SKILL_PROMPT_CUSTOM_TYPE) {
    const name = pickString(pickObject(message, 'details'), 'name')
    if (name)
      return [{ kind: 'text', text: `Loaded the skill ${name}` }]
  }
  const text = withoutSystemNotice(customText(message))
  return text ? [{ kind: 'text', text }] : []
}

/** The words that open an extension notice of one severity, or '' for plain information. */
function notifyPrefix(notifyType: string): string {
  switch (notifyType) {
    case OH_MY_PI_NOTIFY_TYPE.Warning:
      return 'Warning: '
    case OH_MY_PI_NOTIFY_TYPE.Error:
      return 'Error: '
    default:
      return ''
  }
}

/**
 * Read one omp notification frame into the shared notification model.
 *
 * The worker persists each of these frames as it arrived. The compaction pair and the
 * retries become the structured entries every provider draws the same way; the rest
 * state one line of their own. A custom `message_end` reads here too, because each one
 * is a notice omp writes for the model (see `customMessageEntries`).
 */
export function ohMyPiNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  switch (pickString(msg, 'type')) {
    case OH_MY_PI_EVENT.MessageEnd: {
      const message = pickObject(msg, 'message')
      return message && pickString(message, 'role') === OH_MY_PI_ROLE.Custom ? customMessageEntries(message) : []
    }
    case OH_MY_PI_EVENT.AutoCompactionStart:
      return [{ kind: 'compaction', phase: 'start' }]
    case OH_MY_PI_EVENT.AutoCompactionEnd:
      return [autoCompactionEndEntry(msg)]
    case OH_MY_PI_EVENT.AutoRetryStart:
      return [retryEntry(msg, false)]
    case OH_MY_PI_EVENT.AutoRetryEnd:
      return [retryEntry(msg, true)]
    case OH_MY_PI_EVENT.RetryFallbackApplied: {
      const from = pickString(msg, 'from')
      const to = pickString(msg, 'to')
      const reason = pickString(msg, 'reason')
      return [{ kind: 'text', text: `Switched the model${from ? ` from ${from}` : ''}${to ? ` to ${to}` : ''}${reason ? ` (${reason})` : ''}` }]
    }
    case OH_MY_PI_EVENT.RetryFallbackSucceeded: {
      const model = pickString(msg, 'model')
      return [{ kind: 'text', text: model ? `The fallback model ${model} answered` : 'The fallback model answered' }]
    }
    case OH_MY_PI_EVENT.Notice: {
      const message = pickString(msg, 'message').trim()
      return message ? [{ kind: 'text', text: message }] : []
    }
    case OH_MY_PI_EVENT.ExtensionError: {
      const extension = pickString(msg, 'extensionPath')
      const event = pickString(msg, 'event')
      const error = pickString(msg, 'error')
      return [{ kind: 'text', text: `Extension error${extension ? ` in ${extension}` : ''}${event ? ` (${event})` : ''}${error ? `: ${error}` : ''}` }]
    }
    case OH_MY_PI_EVENT.CommandOutput: {
      const text = pickString(msg, 'text').trim()
      return text ? [{ kind: 'text', text }] : []
    }
    case OH_MY_PI_EVENT.TodoReminder: {
      const open = Array.isArray(msg.todos) ? msg.todos.length : 0
      const attempt = pickNumber(msg, 'attempt', undefined)
      const maxAttempts = pickNumber(msg, 'maxAttempts', undefined)
      const tries = attempt !== undefined && maxAttempts !== undefined ? ` (reminder ${attempt} of ${maxAttempts})` : ''
      return [{ kind: 'status', text: `${open} to-do ${open === 1 ? 'item is' : 'items are'} open; the agent continues${tries}` }]
    }
    case OH_MY_PI_EVENT.IrcMessage: {
      const message = pickObject(msg, 'message')
      const text = message ? ircLine(message) : ''
      return text ? [{ kind: 'text', text }] : []
    }
    case OH_MY_PI_EVENT.RpcFrameError: {
      const error = pickString(msg, 'error')
      return [{ kind: 'text', text: `omp could not send a frame${error ? `: ${error}` : ''}` }]
    }
    case OH_MY_PI_EVENT.Response: {
      if (!isCompactResponse(msg))
        return []
      if (msg.success === true)
        return [{ kind: 'compaction', phase: 'end', detail: compactionDetail(MANUAL_COMPACTION_TRIGGER, pickObject(msg, 'data')) }]
      const error = pickString(msg, 'error')
      return [{ kind: 'status', text: error ? `Compaction skipped: ${error}` : 'Compaction skipped' }]
    }
    case OH_MY_PI_EVENT.ExtensionUIRequest: {
      const method = pickString(msg, 'method')
      if (method === OH_MY_PI_EXTENSION_METHOD.Notify) {
        const message = pickString(msg, 'message').trim()
        return message ? [{ kind: 'text', text: `${notifyPrefix(pickString(msg, 'notifyType'))}${message}` }] : []
      }
      if (method === OH_MY_PI_EXTENSION_METHOD.OpenURL) {
        const url = pickString(msg, 'url')
        return url ? [{ kind: 'text', text: `Open ${url}` }] : []
      }
      return method ? [{ kind: 'text', text: `Extension request: ${method}` }] : []
    }
    default:
      return []
  }
}
