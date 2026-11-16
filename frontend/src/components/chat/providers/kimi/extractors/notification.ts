import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { KIMI_COMPACTION_PHASE, KIMI_EVENT, KIMI_NOTICE_LEVEL, KIMI_ORIGIN, KIMI_TRANSCRIPT_MARKER, KIMI_TRANSCRIPT_TURN } from '~/generated/contracts/kimi-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'
import { KIMI_GOAL_CONTINUATION, kimiEvent } from '../protocol'
import { kimiTranscriptMarker, kimiTranscriptRetry, kimiTranscriptRow, kimiTranscriptTaskText, kimiTranscriptTurn } from '../transcript'

/**
 * Describe an autonomous native turn start from its original origin.
 *
 * Activation and delivery markers preserve their own facts, which can occur without
 * a new turn. User messages and task notifications already explain their turns.
 *
 * A Map, for the reason `kimi/toolKinds.ts` gives: the key is a WIRE word.
 */
const KIMI_ORIGIN_PHRASES: ReadonlyMap<string, (name: string) => string> = new Map([
  [
    KIMI_ORIGIN.SystemTrigger,
    (name: string) => name === KIMI_GOAL_CONTINUATION
      ? 'Continuing the goal'
      : name
        ? `Started by ${name}`
        : 'Started by the agent',
  ],
  [
    KIMI_ORIGIN.CronJob,
    (name: string) => name
      ? `Scheduled prompt ${name} started a turn`
      : 'A scheduled prompt started a turn',
  ],
  [
    KIMI_ORIGIN.CronMissed,
    (name: string) => name
      ? `Missed scheduled prompt ${name} ran late`
      : 'A missed scheduled prompt ran late',
  ],
  [
    KIMI_ORIGIN.Retry,
    () => 'Retrying the turn',
  ],
  [
    KIMI_ORIGIN.HookResult,
    (name: string) => name
      ? `Hook ${name} started a turn`
      : 'A hook started a turn',
  ],
  [
    KIMI_ORIGIN.SkillActivation,
    (name: string) => name
      ? `Skill ${name} started a turn`
      : 'A skill started a turn',
  ],
  [
    KIMI_ORIGIN.PluginCommand,
    (name: string) => name
      ? `Plugin command ${name} started a turn`
      : 'A plugin command started a turn',
  ],
  [
    KIMI_ORIGIN.Injection,
    () => 'The agent received injected context',
  ],
  [
    KIMI_ORIGIN.ShellCommand,
    (name: string) => name
      ? `Shell command ${name} started a turn`
      : 'A shell command started a turn',
  ],
  [
    KIMI_ORIGIN.CompactionSummary,
    () => 'Continuing from the compacted context',
  ],
])

/**
 * Read one Kimi Code notification row into the shared notification model.
 *
 * The SOLE notification seam, for a standalone row and for one entry of a consolidated
 * thread alike. Returns an empty list for a row this provider does not own, and for a
 * row of its own that states nothing it can word.
 */
export function kimiNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const taskText = kimiTranscriptTaskText(msg)
  if (taskText !== null) {
    return taskText.trim()
      ? [
          { kind: 'text', text: taskText },
        ]
      : []
  }
  const turn = kimiTranscriptTurn(msg)
  if (turn) {
    if (pickString(turn, 'state') !== KIMI_TRANSCRIPT_TURN.Running)
      return []
    const origin = pickObject(turn, 'origin')
    if (pickString(origin, 'kind') === 'task') {
      const prompt = pickString(turn, 'prompt')
      return prompt.trim()
        ? [
            { kind: 'text', text: prompt },
          ]
        : []
    }
    const original = pickObject(origin, 'payload') ?? origin
    const phrase = KIMI_ORIGIN_PHRASES.get(pickString(original, 'kind'))
    return phrase
      ? [
          {
            kind: 'text',
            text: phrase(kimiOriginTitle(original)),
          },
        ]
      : []
  }
  const retry = kimiTranscriptRetry(msg)
  if (retry) {
    return [
      kimiRetryNotification(retry),
    ]
  }
  const marker = kimiTranscriptMarker(msg)
  if (marker) {
    const payload = pickObject(marker, 'payload')
    if (pickString(marker, 'marker') === KIMI_TRANSCRIPT_MARKER.Skill)
      return kimiSkillActivation(payload, kimiTranscriptRow(msg)?.agentId ?? '')
    if (pickString(marker, 'marker') === KIMI_TRANSCRIPT_MARKER.CronFired)
      return kimiCronDelivery(payload)
    if (pickString(marker, 'marker') === KIMI_TRANSCRIPT_MARKER.Notice) {
      const message = pickString(payload, 'message')
      const level = pickString(payload, 'level')
      const code = pickString(pickObject(payload, 'event'), 'code')
      if (!message.trim()) {
        return level === KIMI_NOTICE_LEVEL.Error && code.trim()
          ? [
              { kind: 'text', text: `Error: ${code}` },
            ]
          : []
      }
      const prefix = level === KIMI_NOTICE_LEVEL.Error
        ? code
          ? `Error (${code}): `
          : 'Error: '
        : level === 'warning'
          ? 'Warning: '
          : ''
      return [
        { kind: 'text', text: `${prefix}${message}` },
      ]
    }
    if (pickString(marker, 'marker') === KIMI_TRANSCRIPT_MARKER.Compaction) {
      switch (pickString(payload, 'phase')) {
        case KIMI_COMPACTION_PHASE.Completed:
          return [
            {
              kind: 'compaction',
              phase: 'end',
              detail: kimiCompactionDetails(payload ?? {}),
            },
          ]
        case KIMI_COMPACTION_PHASE.Cancelled:
          return [
            { kind: 'compaction', phase: 'end', outcome: 'aborted' },
          ]
        default:
          return []
      }
    }
    return pickString(marker, 'marker') === KIMI_TRANSCRIPT_MARKER.Hook && payload?.blocked === true
      ? [
          {
            kind: 'hook',
            outcome: 'blocked',
            event: pickString(payload, 'hookEvent'),
            detail: pickString(payload, 'content'),
          },
        ]
      : []
  }
  const event = kimiEvent(msg)
  if (!event)
    return []
  const data = event.data
  switch (event.type) {
    case KIMI_EVENT.ConfigWarning:
      return (Array.isArray(data.warnings)
        ? data.warnings
        : []).flatMap((warning): NotificationEntry[] => {
        const message = isObject(warning)
          ? pickString(warning, 'message')
          : ''
        return message.trim()
          ? [
              { kind: 'text', text: `Warning: ${message}` },
            ]
          : []
      })
    case KIMI_EVENT.Warning: {
      const message = pickString(data, 'message')
      return message
        ? [
            { kind: 'text', text: `Warning: ${message}` },
          ]
        : []
    }
    default:
      return []
  }
}

/** Original origins retain provider-specific identities rather than a shared name field. */
function kimiOriginTitle(origin: Record<string, unknown> | null): string {
  switch (pickString(origin, 'kind')) {
    case KIMI_ORIGIN.SkillActivation:
      return pickString(origin, 'skillName')
    case KIMI_ORIGIN.PluginCommand: {
      const plugin = pickString(origin, 'pluginId')
      const command = pickString(origin, 'commandName')
      return plugin && command
        ? `${plugin}/${command}`
        : ''
    }
    case KIMI_ORIGIN.CronJob:
    case KIMI_ORIGIN.CronMissed:
      return pickString(origin, 'jobId')
    default:
      return pickString(origin, 'name')
  }
}

/** Activation markers can arrive without a separate turn or a tool card. */
function kimiSkillActivation(payload: Record<string, unknown> | null, agentId: string): NotificationEntry[] {
  if (!payload || !agentId || payload.agentId !== agentId
    || !pickString(payload, 'activationId').trim() || !pickString(payload, 'trigger').trim()) {
    return []
  }
  const plugin = payload.variant === KIMI_ORIGIN.PluginCommand
  if (payload.variant !== undefined && !plugin)
    return []
  const optionalFields = plugin
    ? ['commandArgs']
    : ['skillArgs', 'skillPath', 'skillSource']
  if (optionalFields.some(key => payload[key] !== undefined && typeof payload[key] !== 'string'))
    return []
  const name = plugin
    ? [
        pickString(payload, 'pluginId').trim(),
        pickString(payload, 'commandName').trim(),
      ]
    : [
        pickString(payload, 'skillName').trim(),
      ]
  if (name.some(value => !value))
    return []
  const lines = [
    plugin
      ? `Activated plugin command ${name.join('/')}`
      : `Activated skill ${name[0]}`,
  ]
  const args = pickString(payload, plugin ? 'commandArgs' : 'skillArgs')
  if (args)
    lines.push(`Arguments: ${args}`)
  lines.push(`Trigger: ${pickString(payload, 'trigger')}`)
  if (!plugin) {
    const source = pickString(payload, 'skillSource')
    const path = pickString(payload, 'skillPath')
    if (source)
      lines.push(`Source: ${source}`)
    if (path)
      lines.push(`Path: ${path}`)
  }
  return [
    {
      kind: 'text',
      text: lines.join('\n'),
    },
  ]
}

/** A fire confirms input delivery, including input buffered during an active turn. */
function kimiCronDelivery(payload: Record<string, unknown> | null): NotificationEntry[] {
  const origin = pickObject(payload, 'origin')
  const jobId = pickString(origin, 'jobId')
  const cron = pickString(origin, 'cron')
  const prompt = pickString(payload, 'prompt')
  if (!origin || origin.kind !== KIMI_ORIGIN.CronJob || !jobId.trim() || !cron.trim() || !prompt.trim()
    || typeof origin.recurring !== 'boolean' || typeof origin.stale !== 'boolean'
    || typeof origin.coalescedCount !== 'number' || !Number.isSafeInteger(origin.coalescedCount) || origin.coalescedCount < 0) {
    return []
  }
  return [
    {
      kind: 'text',
      text: [
        `Scheduled prompt ${jobId} was delivered`,
        `Schedule: ${cron}`,
        `Recurring: ${origin.recurring ? 'yes' : 'no'}`,
        `Coalesced firings: ${origin.coalescedCount}`,
        `Stale: ${origin.stale ? 'yes' : 'no'}`,
        '',
        prompt,
      ].join('\n'),
    },
  ]
}

/** Identify a stored start only for the shared history filter. */
export function kimiCompactionStart(payload: Record<string, unknown>): boolean {
  const marker = kimiTranscriptMarker(payload)
  if (marker)
    return pickString(marker, 'marker') === KIMI_TRANSCRIPT_MARKER.Compaction && pickString(pickObject(marker, 'payload'), 'phase') === KIMI_COMPACTION_PHASE.Started
  return false
}

/** What a compaction states about the context it rewrote. */
function kimiCompactionDetails(data: Record<string, unknown>): CompactionDetails {
  const result = pickObject(data, 'result')
  const pre = pickNumber(result, 'tokensBefore')
  const post = pickNumber(result, 'tokensAfter')
  return {
    ...(pre !== null
      ? { pre }
      : {}),
    ...(post !== null
      ? { post }
      : {}),
  }
}

/**
 * The compaction boundary one row states, or null for any other row.
 *
 * The context-usage grid reads it the moment the boundary lands, so the gauge drops to
 * the compacted size before the next usage report arrives.
 */
export function kimiCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const marker = kimiTranscriptMarker(parsed.parentObject)
  if (marker) {
    const payload = pickObject(marker, 'payload')
    return pickString(marker, 'marker') === KIMI_TRANSCRIPT_MARKER.Compaction && pickString(payload, 'phase') === KIMI_COMPACTION_PHASE.Completed
      ? kimiCompactionDetails(payload ?? {})
      : null
  }
  return null
}

function kimiRetryNotification(data: Record<string, unknown>): NotificationEntry {
  const attempt = pickNumber(data, 'nextAttempt')
  const maxAttempts = pickNumber(data, 'maxAttempts')
  const delayMs = pickNumber(data, 'delayMs')
  const error = pickString(data, 'errorMessage').trim() || pickString(data, 'errorName').trim()
  return retryEntry('api', {
    ...(attempt !== null
      ? { attempt }
      : {}),
    ...(maxAttempts !== null
      ? { maxAttempts }
      : {}),
    ...(delayMs !== null
      ? { delayMs }
      : {}),
    ...(error
      ? { error }
      : {}),
  })
}
