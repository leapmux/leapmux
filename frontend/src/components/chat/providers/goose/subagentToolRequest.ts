import { pickObject, pickString } from '~/lib/jsonPick'

/** Goose sends subagent requests through logging metadata, without their results. */
function subagentRequestData(parent: Record<string, unknown>): Record<string, unknown> | undefined {
  const notification = pickObject(pickObject(parent, '_meta'), 'toolNotification')
  if (notification?.type !== 'message')
    return undefined
  const data = pickObject(pickObject(notification, 'params'), 'data')
  return data?.type === 'subagent_tool_request' ? data : undefined
}

export function isGooseSubagentToolRequest(parent: Record<string, unknown>): boolean {
  return subagentRequestData(parent) !== undefined
}

export function gooseSubagentToolCall(parent: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(subagentRequestData(parent), 'tool_call') ?? undefined
}

export function gooseSubagentToolRequestName(parent: Record<string, unknown>): string {
  return pickString(gooseSubagentToolCall(parent), 'name')
}
