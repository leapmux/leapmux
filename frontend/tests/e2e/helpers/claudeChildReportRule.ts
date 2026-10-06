import type { MockModelRule } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { CLAUDE_TOOL_NAMES } from '../../../src/components/chat/providers/claude/toolNames'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { claudeToolResultText } from './claudeChildResponse'

export interface ClaudeChildReportOptions {
  spawnCallId: string
  report: string
  reply: string
  /** The status of the native completion notification. It is `completed` unless a spec states another one. */
  completionStatus?: string
  /** The scripted parent reply to the native completion notification. */
  completionReply?: string
}

/** The completion status of a child that ends its task. */
export const CLAUDE_CHILD_COMPLETED_STATUS = 'completed'

/** The parent reply to the completion notification when a spec states no reply of its own. */
export const CLAUDE_CHILD_COMPLETION_REPLY = 'The native child completion notification arrived.'

/** The options with each default applied. */
type CompleteReportOptions = Required<ClaudeChildReportOptions>

/** Read a native child ID only from the result of its actual Agent call. */
export function claudeSpawnedChildId(requestBody: unknown, spawnCallId: string): string | undefined {
  if (!spawnCallId || !isObject(requestBody) || !Array.isArray(requestBody.messages))
    return undefined
  const messages: unknown[] = requestBody.messages
  let callCount = 0
  let childId: string | undefined
  for (const message of messages) {
    if (!isObject(message) || !Array.isArray(message.content))
      continue
    for (const block of message.content) {
      if (!isObject(block))
        continue
      if (message.role === 'assistant' && block.type === 'tool_use' && block.id === spawnCallId) {
        if (block.name !== CLAUDE_TOOL_NAMES.AGENT || !isObject(block.input) || typeof block.input.prompt !== 'string' || block.input.prompt.trim() === '')
          return undefined
        callCount += 1
      }
      if (message.role !== 'user' || block.type !== 'tool_result' || block.tool_use_id !== spawnCallId)
        continue
      if (callCount !== 1 || (Object.hasOwn(block, 'is_error') && block.is_error !== false))
        return undefined
      const text = claudeToolResultText(block.content)
      if (!text?.startsWith('Async agent launched successfully.'))
        return undefined
      const ids = [...text.matchAll(/^agentId: ([\w-]{1,128}) \(internal ID\b/gim)]
      if (ids.length !== 1 || childId !== undefined)
        return undefined
      childId = ids[0]?.[1]
    }
  }
  return callCount === 1 ? childId : undefined
}

function validateOptions(options: ClaudeChildReportOptions): CompleteReportOptions {
  if (!options)
    throw new Error('The Claude parent report rule requires text options.')
  const complete: CompleteReportOptions = {
    ...options,
    completionStatus: options.completionStatus ?? CLAUDE_CHILD_COMPLETED_STATUS,
    completionReply: options.completionReply ?? CLAUDE_CHILD_COMPLETION_REPLY,
  }
  for (const key of ['spawnCallId', 'report', 'reply', 'completionStatus', 'completionReply'] as const) {
    if (typeof complete[key] !== 'string' || complete[key].trim() === '')
      throw new Error(`The Claude parent report rule requires nonempty text for ${key}.`)
  }
  return complete
}

/** Match one actual delivered report from one native child, with an explicit scripted parent reply. */
export function claudeChildReportRule(requestBody: unknown, given: ClaudeChildReportOptions): MockModelRule {
  const options = validateOptions(given)
  const childId = claudeSpawnedChildId(requestBody, options.spawnCallId)
  if (!childId)
    throw new Error('The Claude parent report rule has no matching actual Agent spawn result.')
  const indentedReport = options.report.split('\n').map(line => `  ${line}`).join('\n')
  return {
    name: `claude-parent-report-${childId}`,
    when: {
      protocol: 'anthropic-messages',
      user: `^Another Claude session sent a message:\\n<agent-message from="${escapeRegExp(childId)}">\\n\\[Subagent hand-back\\] [^\\n]*The report follows:\\n${escapeRegExp(indentedReport)}\\n</agent-message>(?:\\n|$)`,
    },
    respond: { text: options.reply },
    once: true,
  }
}

/** Match the native completion notification with both actual IDs and its exact status. */
export function claudeChildCompletionRule(requestBody: unknown, given: ClaudeChildReportOptions): MockModelRule {
  const options = validateOptions(given)
  const childId = claudeSpawnedChildId(requestBody, options.spawnCallId)
  if (!childId)
    throw new Error('The Claude completion rule has no matching actual Agent spawn result.')
  const betweenFields = '(?:(?!<task-id>|<tool-use-id>|<status>|<result>|</?task-notification>)[\\s\\S])*'
  const directReport = escapeRegExp(options.report)
  const deliveredReport = `This agent's report was delivered to you as a message from "${escapeRegExp(childId)}" \\(its SubagentHandback call\\)\\. Read it there; it is not repeated here\\.\\n`
  return {
    name: `claude-parent-completion-${childId}`,
    when: {
      protocol: 'anthropic-messages',
      user: '^<system-reminder>\\n\\[SYSTEM NOTIFICATION - NOT USER INPUT\\]\\n'
        + '(?:(?!<task-notification>)[\\s\\S])*\\n<task-notification>\\n'
        + `<task-id>${escapeRegExp(childId)}</task-id>\\n<tool-use-id>${escapeRegExp(options.spawnCallId)}</tool-use-id>\\n`
        + '<output-file>[^\\n]*</output-file>\\n'
        + `<status>${escapeRegExp(options.completionStatus)}</status>\\n${betweenFields}`
        + `<result>(?:${directReport}|${deliveredReport})</result>\\n`
        + `${betweenFields}</task-notification>\\n</system-reminder>$`,
    },
    respond: { text: options.completionReply },
    once: true,
  }
}

/** Register the expected parent report before the held child can deliver it. */
export async function registerClaudeChildReportRules(modelScript: Pick<ModelScript, 'status' | 'rule'>, given: ClaudeChildReportOptions): Promise<{ reportRule: MockModelRule, completionRule: MockModelRule }> {
  const options = validateOptions(given)
  let requestBody: unknown
  await expect.poll(async () => {
    const requests = (await modelScript.status()).requests
    const request = requests.findLast(value => value.protocol === 'anthropic-messages' && claudeSpawnedChildId(value.body, options.spawnCallId) !== undefined)
    requestBody = request?.body
    return request !== undefined
  }).toBe(true)
  const reportRule = claudeChildReportRule(requestBody, options)
  const completionRule = claudeChildCompletionRule(requestBody, options)
  await modelScript.rule(reportRule, completionRule)
  return { reportRule, completionRule }
}
