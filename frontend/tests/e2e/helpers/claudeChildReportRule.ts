import type { MockModelRule } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { CLAUDE_TOOL_NAMES } from '../../../src/components/chat/providers/claude/toolNames'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'

export interface ClaudeChildReportOptions {
  spawnCallId: string
  report: string
  reply: string
  completionStatus: string
  completionReply: string
}

function nativeResultText(content: unknown): string | undefined {
  if (typeof content === 'string')
    return content
  if (!Array.isArray(content) || content.length !== 1 || !isObject(content[0]) || content[0].type !== 'text' || typeof content[0].text !== 'string')
    return undefined
  return content[0].text
}

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
      const text = nativeResultText(block.content)
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

function validateOptions(options: ClaudeChildReportOptions): void {
  if (!options)
    throw new Error('The Claude parent report rule requires text options.')
  for (const key of ['spawnCallId', 'report', 'reply', 'completionStatus', 'completionReply'] as const) {
    if (typeof options[key] !== 'string' || options[key].trim() === '')
      throw new Error(`The Claude parent report rule requires nonempty text for ${key}.`)
  }
}

/** Match one actual delivered report from one native child, with an explicit scripted parent reply. */
export function claudeChildReportRule(requestBody: unknown, options: ClaudeChildReportOptions): MockModelRule {
  validateOptions(options)
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
export function claudeChildCompletionRule(requestBody: unknown, options: ClaudeChildReportOptions): MockModelRule {
  validateOptions(options)
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
export async function registerClaudeChildReportRules(modelScript: Pick<ModelScript, 'status' | 'rule'>, options: ClaudeChildReportOptions): Promise<{ reportRule: MockModelRule, completionRule: MockModelRule }> {
  validateOptions(options)
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
