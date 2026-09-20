import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ClaudeToolRow } from './toolCommon'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { proseResult, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { claudeToolFailureResult } from './failure'

/** What one Claude `RemoteTrigger` call answered, read out of the endpoint's JSON. */
export interface RemoteTriggerResult {
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

// Memoize structured-payload parses by `tool_use_result` identity, so a row that
// is built more than once in a render -- the body, the toolbar's own derivation
// from the same row -- does not re-run JSON.parse on the same response body.
const structuredCache = new WeakMap<Record<string, unknown>, RemoteTriggerResult | null>()

/**
 * Build a {@link RemoteTriggerResult} from a Claude `RemoteTrigger`
 * tool_result. Prefers the typed `tool_use_result` payload; falls back to
 * parsing the literal `HTTP {status}\n{json}` text content the tool emits.
 * Returns null when neither shape is recognized.
 */
export function claudeRemoteTriggerFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): RemoteTriggerResult | null {
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
  // Both groups always participate in a match of this regex; `?? ''` is the
  // type-level guard alone.
  return buildSource(Number(match[1]), match[2] ?? '')
}

function buildSource(status: number, json: string): RemoteTriggerResult {
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

/**
 * The trigger a call ASKED for: the shared reading, plus Claude's two deviations.
 *
 * `action` is the first deviation. The shared entry answers `other` for every call,
 * because the providers that state an action state it in the TOOL NAME, which that
 * table never sees. Claude states it in an `action` ARGUMENT instead.
 *
 * `body.name` is the second. `RemoteTrigger` sends the trigger endpoint's request body
 * whole under `body`, so the label of a create or an update sits one level down, and no
 * shared entry reads it. It WINS over the root `name` that the shared entry reads, and
 * the order is deliberate: `body` is the only place a Claude trigger tool states a
 * label, so a root `name` beside it comes from a shape nobody has seen here. That shape
 * must not displace the label the call really sent.
 *
 * The id and the schedule come from the shared entry and are read here no longer.
 * Claude spells `trigger_id` the way every provider does, so a reading of its own was a
 * second copy of a neutral one -- the rule that `ToolRequestOverrides` states.
 *
 * The return type is load-bearing, not decorative. TypeScript runs the excess-property
 * check on a fresh object literal in an ANNOTATED position alone, so an un-annotated
 * request accepts a key that `TriggerRequest` never declares, and no renderer can read
 * it. `toolTableEntriesAreAnnotated.test.ts` keeps every TABLE entry in that form, and
 * it reaches no helper a table entry calls -- so only a reader keeps this one annotated.
 */
export function claudeTriggerRequest(input: Record<string, unknown>): ToolRequestByKind['trigger'] {
  const shared = DEFAULT_TOOL_REQUESTS.trigger(input)
  const name = pickString(pickObject(input, 'body'), 'name') || shared.name
  return {
    ...shared,
    action: claudeTriggerAction(pickString(input, 'action')),
    // The label rides only when one of the two halves carried it.
    ...(name !== undefined ? { name } : {}),
  }
}

/**
 * The trigger pair: the action it ran, and the endpoint's answer.
 *
 * The HTTP status words the row TITLE, and it also decides the outcome. An endpoint
 * that answered outside 2xx failed the call although it answered, so the payload
 * overrides the status with `failed`.
 */
export function claudeTriggerSpec(request: ToolRequestByKind['trigger'], args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'trigger'> {
  if (!result) {
    // An action the tool does not spell falls back to the tool's own name.
    return {
      kind: 'trigger',
      request,
      ...(request.action === 'other' ? { title: args.toolName } : {}),
    }
  }
  const source = claudeRemoteTriggerFromToolResult(result.toolUseResult, result.resultContent)
  if (!source) {
    // The failure rung sits HERE rather than ahead of the parse, which is this kind's
    // one deviation from the ladder {@link claudeToolFailureResult} states. An endpoint that
    // answered outside 2xx still answered: the branch below titles the row with that
    // status and draws the response body, which says more than the raw text does. A
    // call the tool itself failed carries no `HTTP <status>` line for the parse to
    // read, so it lands here -- and it states its reason under the row's failed status
    // rather than claiming, as `unparsedResult` does, that the call completed.
    const failure = claudeToolFailureResult(result)
    return { kind: 'trigger', request, result: failure ?? unparsedResult(result.resultContent) }
  }
  const ok = source.status >= 200 && source.status < 300
  const triggerName = pickString(source.trigger ?? undefined, 'name')
  const triggerId = pickString(source.trigger ?? undefined, 'id')
  const tail = triggerName && triggerId ? `${triggerName} (${triggerId})` : (triggerName || triggerId)
  return {
    kind: 'trigger',
    request,
    title: tail ? `HTTP ${source.status} · ${tail}` : `HTTP ${source.status}`,
    // An endpoint that answered outside 2xx failed the call although it answered.
    ...(ok ? {} : { statusOverride: 'failed' }),
    result: proseResult(prettifyJson(source.parsed ?? source.json), 'plain'),
  }
}

/** The action word the `action` argument states, or `other` for one it does not spell. */
function claudeTriggerAction(action: string): ToolRequestByKind['trigger']['action'] {
  if (action === 'list' || action === 'get' || action === 'create' || action === 'update' || action === 'run' || action === 'delete')
    return action
  return 'other'
}
