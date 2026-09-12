import type { AgentResultSource } from '../../results/agentResult'
import { REASONIX_TOOL } from '~/generated/contracts/reasonix-protocol'
import { pickString } from '~/lib/jsonPick'

const REFERENCE = /^Subagent reference(?: \(failed\))?: (sa_[\w-]*)$/
const OUTCOME = /^Subagent outcome: status=(completed|partial|failed|cancelled) retryable=(true|false)(?: error_code=(\S+))?$/
const ANSWER_MARKER = '\n\nFinal answer:\n'
const CONTINUATION_GUIDANCE = 'To continue this same subagent transcript in a later call, pass this ref as `continue_from`. Start a fresh subagent when the next task is independent.'

function nativeGuidance(text: string, reference?: string): { forkedFrom?: string } | null {
  const value = text.trim()
  if (!value)
    return {}
  if (!reference)
    return null
  if (value === CONTINUATION_GUIDANCE)
    return {}
  const firstBreak = value.indexOf('\n')
  const fork = /^Forked from: (sa_[\w-]*)$/.exec(value.slice(0, firstBreak))
  if (firstBreak < 0 || !fork)
    return null
  const expected = `The requested ref resolves to an ancestor conversation transcript, so the framework continues a copy owned by the current conversation. To continue this copied subagent transcript in a later call, pass ${reference} as \`continue_from\`. Start a fresh subagent when the next task is independent.`
  return value.slice(firstBreak + 1) === expected ? { forkedFrom: fork[1] } : null
}

interface ReasonixAgentOutput {
  toolName: string
  input: Record<string, unknown>
  output: string
  originalOutput?: string
  status: unknown
}

/** Parse native status only when the tool can supply a status header. */
export function reasonixAgentResult({ toolName, input, output: fullOutput, originalOutput, status }: ReasonixAgentOutput): AgentResultSource {
  // Remove the native error wrapper only when it repeats the original ACP headline.
  const errorPrefix = status === 'failed' && originalOutput ? `error: ${originalOutput}\n` : ''
  const error = errorPrefix && fullOutput.startsWith(errorPrefix) ? originalOutput ?? '' : ''
  const output = error ? fullOutput.slice(errorPrefix.length) : fullOutput
  // Successful read-only runs return the answer verbatim because their transcript is ephemeral.
  const canHaveStatus = toolName !== REASONIX_TOOL.ReadOnlyTask || status !== 'completed'
  const firstBreak = output.indexOf('\n')
  const reference = canHaveStatus ? REFERENCE.exec(firstBreak < 0 ? output : output.slice(0, firstBreak)) : null
  const outcomeStart = reference ? firstBreak + 1 : 0
  const outcomeEnd = output.indexOf('\n', outcomeStart)
  const details = canHaveStatus ? OUTCOME.exec(output.slice(outcomeStart, outcomeEnd < 0 ? undefined : outcomeEnd)) : null
  const structured = details !== null
  const nativeStatus = structured ? details[1] : undefined
  const stopped = status === 'cancelled' || nativeStatus === 'cancelled'
  const failed = (status === 'failed' && nativeStatus !== 'partial') || nativeStatus === 'failed'
  const background = toolName === REASONIX_TOOL.Task && !structured && /^Started background task "[^"\r\n]+" \(/.test(output)
  const unknown = nativeStatus === 'partial' || (canHaveStatus && !structured && (reference !== null || output.startsWith('Subagent outcome: ') || input.run_in_background === true))
  const outcome = stopped ? 'stopped' : failed ? 'failed' : background ? 'running' : unknown ? 'unknown' : 'completed'
  const marker = structured ? output.indexOf(ANSWER_MARKER, outcomeStart) : -1
  const remaining = structured && outcomeEnd >= 0 ? output.slice(outcomeEnd, marker < 0 ? undefined : marker) : ''
  const guidance = structured ? nativeGuidance(remaining, reference?.[1]) : null
  const retained = structured && guidance === null ? remaining.replace(/^\n{1,2}/, '') : ''
  const answer = marker >= 0 ? output.slice(marker + ANSWER_MARKER.length) : ''
  const metadata: AgentResultSource['metadata'] = []
  if (structured) {
    if (reference)
      metadata.push({ label: 'Agent ID', value: reference[1] })
    metadata.push({ label: 'Retryable', value: details[2] === 'true' ? 'Yes' : 'No' })
    if (guidance?.forkedFrom)
      metadata.push({ label: 'Forked from', value: guidance.forkedFrom })
    if (details[3])
      metadata.push({ label: 'Error code', value: details[3] })
  }
  const report = structured ? retained + (retained && answer ? '\n\n' : '') + answer : output
  return {
    description: pickString(input, 'description'),
    agentId: structured && reference ? reference[1] : '',
    status: outcome === 'unknown' ? nativeStatus === 'partial' ? 'partial' : 'returned a result' : outcome,
    outcome,
    metadata,
    body: [error, report].filter(value => value !== '').join('\n\n'),
  }
}
