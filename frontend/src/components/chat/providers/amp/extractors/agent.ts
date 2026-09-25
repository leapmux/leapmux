import type { AgentRequest, AgentResult } from '../../../model/tools/agent'
import { AMP_SUBAGENT_TOOL } from '~/generated/contracts/amp-protocol'
import { pickString, stringArray } from '~/lib/jsonPick'

/**
 * Amp's four subagent tools.
 *
 *   Task       {description, prompt}        a general subagent
 *   oracle     {task, context?, files?}     a second model's advice
 *   librarian  {query, context?}            research in code on GitHub
 *   finder     {query}                      a search of the workspace
 *
 * Amp runs each on its server and prints only the call and its final result, so a
 * call has no child transcript. The worker keys the call's registry row by the call's
 * own id, which the request states so that the row points at it.
 */

/** The first non-blank line of a text, trimmed. */
function firstLine(text: string): string {
  return text.split('\n').map(line => line.trim()).find(line => line !== '') ?? ''
}

/**
 * The specialist's name before its request. The worker titles the registry row with
 * the same rule, and `testdata/amp_subagent_title_conformance.json` holds the two to it.
 */
function labeled(label: string, request: string): string {
  const line = firstLine(request)
  return line ? `${label}: ${line}` : label
}

/** The non-blank parts, with a blank line between them. */
function joinParts(...parts: string[]): string {
  return parts.filter(part => part.trim() !== '').join('\n\n')
}

/** The launch one subagent call states. */
export function ampAgentRequest(toolName: string, args: Record<string, unknown>, callId: string): AgentRequest {
  const common = { promptFormat: 'markdown' as const, registryKey: callId }
  switch (toolName) {
    case AMP_SUBAGENT_TOOL.Task: {
      const prompt = pickString(args, 'prompt')
      return { ...common, description: pickString(args, 'description').trim() || firstLine(prompt) || 'Subagent', prompt }
    }
    case AMP_SUBAGENT_TOOL.Oracle: {
      const task = pickString(args, 'task')
      const files = stringArray(args.files)
      return {
        ...common,
        description: labeled('Oracle', task),
        agentType: toolName,
        prompt: joinParts(task, pickString(args, 'context'), files.length > 0 ? files.map(file => `- ${file}`).join('\n') : ''),
      }
    }
    case AMP_SUBAGENT_TOOL.Librarian: {
      const query = pickString(args, 'query')
      return { ...common, description: labeled('Librarian', query), agentType: toolName, prompt: joinParts(query, pickString(args, 'context')) }
    }
    default: {
      const query = pickString(args, 'query')
      return { ...common, description: labeled('Finder', query), agentType: toolName, prompt: query }
    }
  }
}

/** The report one finished subagent call returned, as the run it describes. */
export function ampAgentResult(request: AgentRequest, text: string, failed: boolean): AgentResult {
  return {
    agents: [{
      description: request.description,
      ...(request.registryKey !== undefined ? { registryKey: request.registryKey } : {}),
      agentId: '',
      outcome: failed ? 'failed' : 'completed',
      metadata: [],
      body: text,
    }],
  }
}
