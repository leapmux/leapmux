import { isObject, pickString } from '~/lib/jsonPick'

/** Provider-neutral source for a Claude `RemoteTrigger` tool result. */
export interface RemoteTriggerResultSource {
  status: number
  /** Raw JSON string returned by the trigger API. */
  json: string
  /** Parsed JSON when valid; null otherwise. */
  parsed: unknown
  /**
   * Best-effort top-level trigger object inside the parsed payload —
   * `parsed.trigger` when present (single-trigger responses), otherwise the
   * top-level object itself. Null when the payload isn't an object.
   */
  trigger: Record<string, unknown> | null
}

// Memoize structured-payload parses by `tool_use_result` identity so the
// dispatcher, `claudeToolResultMeta.isCollapsible`, and the copy path don't
// each re-run JSON.parse on the same response body within one render.
const structuredCache = new WeakMap<Record<string, unknown>, RemoteTriggerResultSource | null>()

/**
 * Build a {@link RemoteTriggerResultSource} from a Claude `RemoteTrigger`
 * tool_result. Prefers the typed `tool_use_result` payload; falls back to
 * parsing the literal `HTTP {status}\n{json}` text content the tool emits.
 * Returns null when neither shape is recognized.
 */
export function claudeRemoteTriggerFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): RemoteTriggerResultSource | null {
  if (toolUseResult && typeof toolUseResult.status === 'number') {
    const cached = structuredCache.get(toolUseResult)
    if (cached !== undefined)
      return cached
    const built = buildSource(toolUseResult.status, pickString(toolUseResult, 'json'))
    structuredCache.set(toolUseResult, built)
    return built
  }

  const match = /^HTTP (\d+)\n([\s\S]*)$/.exec(resultContent)
  if (!match)
    return null
  return buildSource(Number(match[1]), match[2])
}

function buildSource(status: number, json: string): RemoteTriggerResultSource {
  let parsed: unknown = null
  try {
    parsed = json ? JSON.parse(json) : null
  }
  catch {}
  const trigger = isObject(parsed) && isObject(parsed.trigger)
    ? parsed.trigger
    : (isObject(parsed) ? parsed : null)
  return { status, json, parsed, trigger }
}
