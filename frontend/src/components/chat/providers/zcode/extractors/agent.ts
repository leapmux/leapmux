import type { AgentResultSource } from '../../../results/agentResult'
import type { ZCodeRow } from './toolCommon'
import { pickString } from '~/lib/jsonPick'
import { formatDuration, formatNumber } from '../../../rendererUtils'
import { zcodeErrorText, zcodeExtractTool, zcodeToolInput } from './toolCommon'

/** ZCode's model formatter appends this identity and usage footer to a completed agent report. */
const RESULT_FOOTER = /\nagentId: ([^'\r\n]+) \(use SendMessage with to: '\1' to continue this agent\)\n<usage>(?:subagent_tokens: (\d+)\n)?tool_uses: (\d+)\nduration_ms: (\d+)<\/usage>$/

/** These lines come from ZCode's formatAgentOutputForModel serializer. */
const LAUNCH_BACKGROUND = 'The agent is working in the background. You will be notified automatically when it completes.'
const LAUNCH_WAIT = 'Briefly tell the user what you launched and end your response. Do not generate any other text - agent results will arrive in a subsequent message.'
const LAUNCH_WORK = 'Do not duplicate this agent\'s work - avoid working with the same files or topics it is using. Work on non-overlapping tasks, or briefly tell the user what you launched and end your response.'
const LAUNCH_OUTPUT = 'Do NOT Read or tail this file via the shell tool. If the user asks for progress, say the agent is still running; you\'ll get a completion notification.'

function launchedAgent(content: string): { agentId: string, outputFile?: string } | null {
  if (!content.startsWith('Async agent launched successfully.\n'))
    return null
  const lines = content.split('\n', 7)
  const identity = /^agentId: ([^'\r\n]+) \(internal ID - do not mention to user\. Use SendMessage with to: '\1' to continue this agent\.\)$/.exec(lines[1])
  if (!identity || lines[2] !== LAUNCH_BACKGROUND)
    return null
  if (lines.length === 4 && lines[3] === LAUNCH_WAIT)
    return { agentId: identity[1] }
  if (lines.length === 6 && lines[3] === LAUNCH_WORK && lines[4].startsWith('output_file: ') && lines[5] === LAUNCH_OUTPUT)
    return { agentId: identity[1], outputFile: lines[4].slice('output_file: '.length) }
  return null
}

/** Preserve unrecognized text. Remove a footer only when its full native grammar matches. */
export function zcodeAgentResult(row: ZCodeRow): AgentResultSource | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update || (!update.result && !update.isError))
    return null
  const input = zcodeToolInput(row)
  const content = update.isError ? zcodeErrorText(update) || 'Tool call failed' : update.result?.content ?? ''
  const footer = !update.isError ? RESULT_FOOTER.exec(content) : null
  const launch = !update.isError && !footer ? launchedAgent(content) : null
  const metadata: AgentResultSource['metadata'] = []
  const agentId = footer?.[1] ?? launch?.agentId ?? ''
  if (agentId)
    metadata.push({ label: 'Agent ID', value: agentId })
  if (launch?.outputFile)
    metadata.push({ label: 'Output', value: launch.outputFile })
  if (footer) {
    for (const [label, value] of [['Tokens', footer[2]], ['Tool uses', footer[3]], ['Duration', footer[4]]]) {
      if (value === undefined)
        continue
      const number = Number(value)
      metadata.push({ label, value: Number.isSafeInteger(number) ? label === 'Duration' ? formatDuration(number) : formatNumber(number) : value })
    }
  }
  return {
    description: pickString(input, 'description').trim(),
    agentId,
    status: update.isError ? 'failed' : launch ? 'launched asynchronously' : footer ? 'completed' : 'returned a result',
    outcome: update.isError ? 'failed' : launch ? 'running' : footer ? 'completed' : 'unknown',
    metadata,
    body: footer ? content.slice(0, footer.index) : launch ? pickString(input, 'prompt') : content,
    bodyLabel: launch ? 'Prompt' : undefined,
  }
}
