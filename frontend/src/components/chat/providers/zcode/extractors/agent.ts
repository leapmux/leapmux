import type { AgentRun } from '../../../model/tools/agent'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_AGENT_LAUNCH } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { formatDuration, formatNumber } from '../../../rendererUtils'
import { zcodeErrorText, zcodeExtractTool, zcodeToolInput } from './toolCommon'

/** ZCode's model formatter appends this identity and usage footer to a completed agent report. */
const RESULT_FOOTER = /\nagentId: ([^'\r\n]+) \(use SendMessage with to: '\1', summary: '<5-10 word recap>' to continue this agent\)\n<usage>(?:subagent_tokens: (\d+)\n)?tool_uses: (\d+)\nduration_ms: (\d+)<\/usage>$/

/** Require the complete native receipt so a quoted or partial message stays visible. */
function launchedAgent(content: string): { agentId: string, outputFile?: string } | null {
  const grammar = ZCODE_AGENT_LAUNCH
  const lines = content.split('\n')
  if (lines[0] !== grammar.Header || lines[2] !== grammar.Background)
    return null
  const identity = lines[1] ?? ''
  if (!identity.startsWith(grammar.IdentityPrefix))
    return null
  const agentId = identity.slice(grammar.IdentityPrefix.length).split(grammar.IdentityMiddle, 1)[0] ?? ''
  if (!agentId || /['\r\n]/.test(agentId)
    || identity !== `${grammar.IdentityPrefix}${agentId}${grammar.IdentityMiddle}${agentId}${grammar.IdentitySuffix}`) {
    return null
  }
  if (lines.length === 4 && lines[3] === grammar.Wait)
    return { agentId }
  if (lines.length === 6 && lines[3] === grammar.Work && lines[4]?.startsWith(grammar.OutputPrefix) && lines[5] === grammar.OutputRule) {
    const outputFile = lines[4].slice(grammar.OutputPrefix.length)
    return outputFile
      ? { agentId, outputFile }
      : null
  }
  return null
}

/** Preserve unrecognized text. Remove a footer only when its full native grammar matches. */
export function zcodeAgentResult(row: ZCodeRow): AgentRun | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update || (!update.result && !update.isError))
    return null
  const input = zcodeToolInput(row)
  const content = update.isError
    ? zcodeErrorText(update) || 'Tool call failed'
    : update.result?.content ?? ''
  const footer = !update.isError
    ? RESULT_FOOTER.exec(content)
    : null
  const launch = !update.isError && !footer
    ? launchedAgent(content)
    : null
  const metadata: AgentRun['metadata'] = []
  const agentId = footer?.[1] ?? launch?.agentId ?? ''
  if (agentId)
    metadata.push({ label: 'Agent ID', value: agentId })
  if (launch?.outputFile)
    metadata.push({ label: 'Output', value: launch.outputFile })
  if (footer) {
    // Tuple-typed, so the label destructures as the string it is spelled as.
    const counters: Array<[string, string | undefined]> = [
      ['Tokens', footer[2]],
      ['Tool uses', footer[3]],
      ['Duration', footer[4]],
    ]
    for (const [label, value] of counters) {
      if (value === undefined)
        continue
      const number = Number(value)
      metadata.push({
        label,
        value: Number.isSafeInteger(number)
          ? label === 'Duration'
            ? formatDuration(number)
            : formatNumber(number)
          : value,
      })
    }
  }
  return {
    description: pickString(input, 'description').trim(),
    agentId,
    statusLabel: update.isError
      ? 'failed'
      : launch
        ? 'launched asynchronously'
        : footer ? 'completed' : 'returned a result',
    outcome: update.isError
      ? 'failed'
      : launch
        ? 'running'
        : footer ? 'completed' : 'unknown',
    metadata,
    body: footer
      ? content.slice(0, footer.index)
      : launch
        ? pickString(input, 'prompt')
        : content,
    ...(launch
      ? { bodyLabel: 'Prompt' as const }
      : {}),
  }
}
