// Generates Go and TypeScript constants from contracts/*.json -- the single
// sources of truth for every value consumed on both sides of a language
// boundary. Run via `task generate-contracts`, which publishes the staging
// tree this script writes through scripts/sync-generated.mjs (mtime-preserving,
// exactly like the proto output).
//
// Order of authority, strongest first:
//   1. The JSON Schema beside each contract (shape) -- validated BEFORE any
//      output is written, so an invalid contract fails generation, not lint.
//   2. The semantic checks below (arithmetic relations, cross-references).
//   3. The proto descriptors from `buf build` (enum-keyed domains must cover
//      every non-UNSPECIFIED enum value) -- adding a proto enum value without
//      its contract metadata fails the build instead of rendering blank.
//
// Every registered domain must ship its contracts/<name>.json: a missing
// file is a hard failure, not a skip -- the migration is complete, and a
// silent skip orphans the domain's outputs at publish time (sync-generated
// prunes them), surfacing as compile errors in generated code far from the
// cause. A present-but-invalid file is a hard failure too.
//
// Naming is explicit per-domain mapping tables, not algorithmic case
// conversion: the generated names must equal the names each side already
// imports (Go: the channelwire/authscope export surface; TS: the UPPER_SNAKE
// constants), so a mapping that silently mangles one is worse than a table
// a reviewer can read. The tables are tested for injectivity.

import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { posix } from 'node:path'
import { argv, exit } from 'node:process'

import { formatFailureLines, validateSchemalessDir } from './validate-json.mjs'

/** A failed semantic check, reported with the contract file it came from. */
export class ContractError extends Error {
  constructor(file, message) {
    super(`${file}: ${message}`)
    this.file = file
  }
}

function mustBe(condition, file, message) {
  if (!condition)
    throw new ContractError(file, message)
}

/**
 * Bijects a contract object's keys with the per-language name tables that
 * render it. The tables drive both the checks and the emitters, so a JSON key
 * without a table entry would pass every check and emit NOTHING -- silently.
 * Both directions: every key has every table entry, and every table entry
 * matches a key. Keys starting with "_" are prose (_readme), not values.
 */
function checkTableCoverage(file, where, keys, tables) {
  const valueKeys = keys.filter(k => !k.startsWith('_'))
  for (const key of valueKeys) {
    for (const [tableName, table] of tables)
      mustBe(table[key] != null, file, `${where}.${key} has no ${tableName} entry -- a new value must land in every name table in the same change, or it is never emitted`)
  }
  for (const [tableName, table] of tables) {
    for (const key of Object.keys(table))
      mustBe(keys.includes(key), file, `${tableName} entry ${key} matches no ${where} key`)
  }
}

// ---------------------------------------------------------------------------
// wire: channelwire transport limits, timing, close reasons
// ---------------------------------------------------------------------------

/**
 * Derived wire values, computed from the primitives the JSON stores. The
 * derivations previously lived as expressions in Go (wire.go) and TS
 * (reassembler.ts); computing them HERE is what makes them single-sourced.
 */
export function deriveWire(w) {
  const maxPlaintextPerChunkBytes = w.maxCiphertextForChunkBytes - w.noiseAeadTagSizeBytes
  const maxReassembledMessageSizeBytes = w.maxMessageSizeBytes + w.innerEnvelopeHeadroomBytes
  const sessionKeyHardCeilingMs = w.sessionKey.maxAgeMs + w.sessionKey.hardCeilingOverrunMs
  return { maxPlaintextPerChunkBytes, maxReassembledMessageSizeBytes, sessionKeyHardCeilingMs }
}

/** JSON key -> the Go name channelwire already exports. */
export const WIRE_GO_NAMES = {
  noiseAeadTagSizeBytes: 'NoiseAEADAuthTagSize',
  maxCiphertextForChunkBytes: 'MaxCiphertextForChunk',
  maxPlaintextPerChunkBytes: 'MaxPlaintextPerChunk',
  maxMessageSizeBytes: 'MaxMessageSize',
  innerEnvelopeHeadroomBytes: 'InnerEnvelopeHeadroom',
  maxReassembledMessageSizeBytes: 'DefaultMaxReassembledMessageSize',
  maxConfigurableMessageSizeBytes: 'MaxConfigurableMessageSize',
  maxIncompleteChunked: 'DefaultMaxIncompleteChunked',
  pingMethod: 'PingMethod',
  protocolVersion: 'ProtocolVersion',
  sessionKeyMaxAgeMs: 'SessionKeyMaxAge',
  sessionKeyMinRekeyIntervalMs: 'MinRekeyInterval',
  sessionKeyHardCeilingMs: 'SessionKeyHardCeiling',
  sessionKeyRejectBackoffMs: 'DefaultRejectBackoff',
  sessionKeyVerifyTimeoutMs: 'SessionVerifyTimeout',
  sessionKeyIdleRekeyIntervalMs: 'IdleRekeyInterval',
  closeReasonTooManyConnections: 'CloseReasonTooManyConnections',
  closeReasonSnapshotTooLarge: 'CloseReasonSnapshotTooLarge',
  closeReasonForbidden: 'CloseReasonForbidden',
  closeReasonControlFlood: 'CloseReasonControlFlood',
  wsRouteUserEvents: 'WSRouteUserEvents',
  wsRouteChannel: 'WSRouteChannel',
  wsParamWorkspaceIds: 'WSParamWorkspaceIDs',
  wsParamResumeAfterHlc: 'WSParamResumeAfterHLC',
  wsParamResumeEpoch: 'WSParamResumeEpoch',
  wsSubprotocolUserEventsRelay: 'WSSubprotocolUserEventsRelay',
  wsSubprotocolChannelRelay: 'WSSubprotocolChannelRelay',
  softNonceLimit: 'SoftNonceLimit',
  hardNonceLimit: 'HardNonceLimit',
  lengthPrefixBytes: 'LengthPrefixBytes',
}

/** JSON key -> the UPPER_SNAKE name the frontend already imports. */
export const WIRE_TS_NAMES = {
  maxPlaintextPerChunkBytes: 'MAX_CHUNK_SIZE',
  maxMessageSizeBytes: 'DEFAULT_MAX_MESSAGE_SIZE',
  innerEnvelopeHeadroomBytes: 'INNER_ENVELOPE_HEADROOM',
  maxReassembledMessageSizeBytes: 'DEFAULT_MAX_REASSEMBLED_MESSAGE_SIZE',
  maxConfigurableMessageSizeBytes: 'MAX_CONFIGURABLE_MESSAGE_SIZE',
  maxIncompleteChunked: 'MAX_INCOMPLETE_CHUNKED',
  pingMethod: 'PING_METHOD',
  protocolVersion: 'PROTOCOL_VERSION',
  sessionKeyMaxAgeMs: 'SESSION_KEY_MAX_AGE_MS',
  sessionKeyMinRekeyIntervalMs: 'MIN_REKEY_INTERVAL_MS',
  sessionKeyHardCeilingMs: 'SESSION_KEY_HARD_CEILING_MS',
  sessionKeyRejectBackoffMs: 'DEFAULT_REJECT_BACKOFF_MS',
  sessionKeyVerifyTimeoutMs: 'SESSION_VERIFY_TIMEOUT_MS',
  sessionKeyIdleRekeyIntervalMs: 'IDLE_REKEY_INTERVAL_MS',
  closeReasonTooManyConnections: 'CLOSE_REASON_TOO_MANY_CONNECTIONS',
  closeReasonSnapshotTooLarge: 'CLOSE_REASON_SNAPSHOT_TOO_LARGE',
  closeReasonForbidden: 'CLOSE_REASON_FORBIDDEN',
  closeReasonControlFlood: 'CLOSE_REASON_CONTROL_FLOOD',
  wsRouteUserEvents: 'WS_USER_EVENTS_ROUTE',
  wsRouteChannel: 'WS_CHANNEL_ROUTE',
  wsParamWorkspaceIds: 'WS_PARAM_WORKSPACE_IDS',
  wsParamResumeAfterHlc: 'WS_PARAM_RESUME_AFTER_HLC',
  wsParamResumeEpoch: 'WS_PARAM_RESUME_EPOCH',
  wsSubprotocolUserEventsRelay: 'WS_SUBPROTOCOL_USER_EVENTS_RELAY',
  wsSubprotocolChannelRelay: 'WS_SUBPROTOCOL_CHANNEL_RELAY',
  softNonceLimit: 'SOFT_NONCE_LIMIT',
  hardNonceLimit: 'HARD_NONCE_LIMIT',
  lengthPrefixBytes: 'LENGTH_PREFIX_BYTES',
}

function flattenWire(w, d) {
  return {
    noiseAeadTagSizeBytes: w.noiseAeadTagSizeBytes,
    maxCiphertextForChunkBytes: w.maxCiphertextForChunkBytes,
    maxPlaintextPerChunkBytes: d.maxPlaintextPerChunkBytes,
    maxMessageSizeBytes: w.maxMessageSizeBytes,
    innerEnvelopeHeadroomBytes: w.innerEnvelopeHeadroomBytes,
    maxReassembledMessageSizeBytes: d.maxReassembledMessageSizeBytes,
    maxConfigurableMessageSizeBytes: w.maxConfigurableMessageSizeBytes,
    maxIncompleteChunked: w.maxIncompleteChunked,
    pingMethod: w.pingMethod,
    protocolVersion: w.protocolVersion,
    sessionKeyMaxAgeMs: w.sessionKey.maxAgeMs,
    sessionKeyMinRekeyIntervalMs: w.sessionKey.minRekeyIntervalMs,
    sessionKeyHardCeilingMs: d.sessionKeyHardCeilingMs,
    sessionKeyRejectBackoffMs: w.sessionKey.rejectBackoffMs,
    sessionKeyVerifyTimeoutMs: w.sessionKey.verifyTimeoutMs,
    sessionKeyIdleRekeyIntervalMs: w.sessionKey.idleRekeyIntervalMs,
    closeReasonTooManyConnections: w.closeReasons.tooManyConnections,
    closeReasonSnapshotTooLarge: w.closeReasons.snapshotTooLarge,
    closeReasonForbidden: w.closeReasons.forbidden,
    closeReasonControlFlood: w.closeReasons.controlFlood,
    wsRouteUserEvents: w.ws.routes.userEvents,
    wsRouteChannel: w.ws.routes.channel,
    wsParamWorkspaceIds: w.ws.queryParams.workspaceIds,
    wsParamResumeAfterHlc: w.ws.queryParams.resumeAfterHlc,
    wsParamResumeEpoch: w.ws.queryParams.resumeEpoch,
    wsSubprotocolUserEventsRelay: w.ws.subprotocols.userEventsRelay,
    wsSubprotocolChannelRelay: w.ws.subprotocols.channelRelay,
    softNonceLimit: w.noise.softNonceLimit,
    hardNonceLimit: w.noise.hardNonceLimit,
    lengthPrefixBytes: w.framing.lengthPrefixBytes,
  }
}

export function checkWire(w) {
  const d = deriveWire(w)
  mustBe(w.maxCiphertextForChunkBytes > 0, 'wire.json', 'maxCiphertextForChunkBytes must be positive')
  mustBe(w.noiseAeadTagSizeBytes > 0, 'wire.json', 'noiseAeadTagSizeBytes must be positive')
  mustBe(d.maxPlaintextPerChunkBytes > 0, 'wire.json', `maxCiphertextForChunkBytes (${w.maxCiphertextForChunkBytes}) minus noiseAeadTagSizeBytes (${w.noiseAeadTagSizeBytes}) must stay positive`)
  mustBe(w.maxMessageSizeBytes >= d.maxPlaintextPerChunkBytes, 'wire.json', 'maxMessageSizeBytes must be at least one chunk (maxPlaintextPerChunkBytes)')
  mustBe(w.maxConfigurableMessageSizeBytes >= w.maxMessageSizeBytes, 'wire.json', 'maxConfigurableMessageSizeBytes must be >= maxMessageSizeBytes')
  mustBe(w.sessionKey.minRekeyIntervalMs <= w.sessionKey.maxAgeMs, 'wire.json', 'sessionKey.minRekeyIntervalMs must be <= sessionKey.maxAgeMs (rekey must fit inside a key lifetime)')
  mustBe(w.sessionKey.hardCeilingOverrunMs > 0, 'wire.json', 'sessionKey.hardCeilingOverrunMs must be positive (the ceiling outlives the max age)')
  mustBe(w.sessionKey.rejectBackoffMs > 0, 'wire.json', 'sessionKey.rejectBackoffMs must be positive')
  mustBe(w.noise.softNonceLimit < 2 ** 32, 'wire.json', 'noise.softNonceLimit must stay inside the uint32 nonce space')
  mustBe(w.noise.hardNonceLimit <= 2 ** 32 - 1, 'wire.json', 'noise.hardNonceLimit must stay inside the uint32 nonce space (the counter wraps past 2^32-1)')
  mustBe(w.noise.softNonceLimit < w.noise.hardNonceLimit, 'wire.json', 'noise.softNonceLimit must be < noise.hardNonceLimit (the soft trigger fires before the wrap bound)')
  mustBe(w.framing.lengthPrefixBytes === 4, 'wire.json', 'framing.lengthPrefixBytes must be 4 -- both framers (Go WriteFramedBytes/ReadFramedBytes, TS frameBytes/unframeBytes) write a big-endian uint32; the constant documents the width, it does not parameterize it')
  // Name-table coverage: a key listed in a table but missing from flattenWire
  // renders as the literal "undefined" in generated code, and a flattened key
  // no table lists is never emitted. Both directions must fail loudly here.
  const flat = flattenWire(w, d)
  const tableKeys = new Set([...Object.keys(WIRE_GO_NAMES), ...Object.keys(WIRE_TS_NAMES)])
  for (const key of tableKeys) {
    mustBe(flat[key] !== undefined, 'wire.json', `${key} is in a wire name table but flattenWire does not provide it -- fix flattenWire, or the emitters render "undefined"`)
  }
  for (const key of Object.keys(flat)) {
    mustBe(tableKeys.has(key), 'wire.json', `flattenWire provides ${key} but no name table lists it -- it is never emitted`)
  }
  return d
}

// ---------------------------------------------------------------------------
// headers: HTTP headers the hub sets and the clients read
// ---------------------------------------------------------------------------

export const HEADERS_GO_NAMES = {
  elevationRequired: 'ElevationRequiredHeader',
  elevationExpiresAt: 'ElevationExpiresAtHeader',
  credentialRejected: 'CredentialRejectedHeader',
}

export const HEADERS_TS_NAMES = {
  elevationRequired: 'ELEVATION_REQUIRED_HEADER',
  elevationExpiresAt: 'ELEVATION_EXPIRES_AT_HEADER',
  credentialRejected: 'CREDENTIAL_REJECTED_HEADER',
}

export function checkHeaders(h) {
  for (const key of Object.keys(HEADERS_GO_NAMES)) {
    const value = h[key]
    mustBe(value != null, 'headers.json', `${key} is missing from headers.json`)
    mustBe(/^Leapmux-[A-Za-z-]+$/.test(value), 'headers.json', `${key} must be a Leapmux-Namespaced-Header token (got ${JSON.stringify(value)})`)
  }
  checkTableCoverage('headers.json', 'headers', Object.keys(h), [
    ['HEADERS_GO_NAMES', HEADERS_GO_NAMES],
    ['HEADERS_TS_NAMES', HEADERS_TS_NAMES],
  ])
  return {}
}

// ---------------------------------------------------------------------------
// listen: the listen-address vocabulary
// ---------------------------------------------------------------------------

/** Source token -> the Go constant the hub compares and sends. */
export const LISTEN_SOURCE_GO_NAMES = {
  listen: 'AddressSourceListen',
  extra: 'AddressSourceExtra',
  merged: 'AddressSourceMerged',
}

/** Source token -> the TS constant the panel renders a label for. */
export const LISTEN_SOURCE_TS_NAMES = {
  listen: 'ADDRESS_SOURCE_LISTEN',
  extra: 'ADDRESS_SOURCE_EXTRA',
  merged: 'ADDRESS_SOURCE_MERGED',
}

export function checkListen(l) {
  mustBe(typeof l.anyHost === 'string' && l.anyHost.length > 0, 'listen.json', 'anyHost must be a non-empty token')
  // The wildcard is a SENTINEL, and listenset.Parse compares it before it
  // tries netip or falls through to a host name -- so a token spelled only
  // from characters a real host can hold would take that address away from
  // every operator who wanted it. At least one character must be one no host
  // can carry.
  mustBe(/[^a-z0-9.:%[\]-]/i.test(l.anyHost), 'listen.json', `anyHost ${JSON.stringify(l.anyHost)} is spelled like a host, so it would shadow one`)
  mustBe(Number.isInteger(l.maxExtraAddresses) && l.maxExtraAddresses >= 1, 'listen.json', 'maxExtraAddresses must be an integer >= 1')
  checkTableCoverage('listen.json', 'addressSources', Object.keys(l.addressSources), [
    ['LISTEN_SOURCE_GO_NAMES', LISTEN_SOURCE_GO_NAMES],
    ['LISTEN_SOURCE_TS_NAMES', LISTEN_SOURCE_TS_NAMES],
  ])
  return {}
}

// ---------------------------------------------------------------------------
// trusted-proxies: selector limit and built-in provider catalogue
// ---------------------------------------------------------------------------

export function checkTrustedProxies(v) {
  mustBe(Number.isInteger(v.maxSelectors) && v.maxSelectors >= 1, 'trusted-proxies.json', 'maxSelectors must be an integer >= 1')
  const expected = ['cloudflare', 'cloudfront']
  mustBe(Object.keys(v.providers).join(',') === expected.join(','), 'trusted-proxies.json', 'providers must contain cloudflare and cloudfront in that order')
  const tokens = []
  for (const [key, provider] of Object.entries(v.providers)) {
    mustBe(provider.token === key, 'trusted-proxies.json', `providers.${key}.token must equal ${JSON.stringify(key)}`)
    mustBe(provider.label.length > 0, 'trusted-proxies.json', `providers.${key}.label must not be empty`)
    mustBe(provider.help.length > 0, 'trusted-proxies.json', `providers.${key}.help must not be empty`)
    tokens.push(provider.token)
  }
  mustBe(new Set(tokens).size === tokens.length, 'trusted-proxies.json', 'provider tokens must be unique')
  return {}
}

// ---------------------------------------------------------------------------
// retry: cross-language retry policies
// ---------------------------------------------------------------------------

/** Policy name -> the Go prefix streamevents et al. use. */
export const RETRY_GO_NAMES = {
  eventsRejection: 'EventsRejectionRetry',
}

/** Policy name -> the UPPER_SNAKE object the frontend spreads into backoff opts. */
export const RETRY_TS_NAMES = {
  eventsRejection: 'EVENTS_REJECTION_RETRY',
}

export function checkRetry(r) {
  checkTableCoverage('retry.json', 'policies', Object.keys(r.policies), [
    ['RETRY_GO_NAMES', RETRY_GO_NAMES],
    ['RETRY_TS_NAMES', RETRY_TS_NAMES],
  ])
  for (const [name, p] of Object.entries(r.policies)) {
    mustBe(p.initialMs > 0, 'retry.json', `policies.${name}.initialMs must be positive`)
    mustBe(p.maxMs >= p.initialMs, 'retry.json', `policies.${name}.maxMs must be >= initialMs`)
    mustBe(p.multiplier >= 1, 'retry.json', `policies.${name}.multiplier must be >= 1`)
    mustBe(p.jitterFactor >= 0 && p.jitterFactor < 1, 'retry.json', `policies.${name}.jitterFactor must be in [0, 1) -- the same bound backoffutil.NewRetry and createExponentialBackoff both reject outside of`)
    mustBe(Number.isInteger(p.maxAttempts) && p.maxAttempts >= 1, 'retry.json', `policies.${name}.maxAttempts must be an integer >= 1`)
  }
  return {}
}

// ---------------------------------------------------------------------------
// session-info: the agent_session_info wire vocabulary
// ---------------------------------------------------------------------------

/**
 * The tables of contracts/session-info.json, each with the prefix its Go
 * constants take and the name of its TS object. Ordered as the file is, so the
 * generated output reads in the same order as the source.
 *
 * The JSON key IS the name (like worker-vocab's notificationTypes), so there is
 * no separate name table to keep in step -- the schema's propertyNames pattern
 * already limits a key to the PascalCase a Go identifier and a TS property both
 * accept.
 */
export const SESSION_INFO_TABLES = [
  { json: 'keys', goPrefix: 'SessionInfoKey', ts: 'SESSION_INFO_KEY', tsType: 'SessionInfoKey', what: 'top-level `info` keys' },
  { json: 'contextUsageFields', goPrefix: 'ContextUsageField', ts: 'CONTEXT_USAGE_FIELD', tsType: 'ContextUsageField', what: 'fields of the context_usage object' },
  { json: 'rateLimitFields', goPrefix: 'RateLimitField', ts: 'RATE_LIMIT_FIELD', tsType: 'RateLimitField', what: 'fields of one rate_limits tier' },
  { json: 'runningToolFields', goPrefix: 'RunningToolField', ts: 'RUNNING_TOOL_FIELD', tsType: 'RunningToolField', what: 'fields of the running_tool object' },
  { json: 'runningToolRetryFields', goPrefix: 'RunningToolRetryField', ts: 'RUNNING_TOOL_RETRY_FIELD', tsType: 'RunningToolRetryField', what: 'fields of running_tool.retry' },
]

export function checkSessionInfo(v) {
  // Biject the JSON's own tables with SESSION_INFO_TABLES. Without this, a table
  // added to session-info.json and to its schema emits no Go and no TS, and says
  // nothing: the emitters below iterate the descriptor list alone, so the first
  // report is an undefined-constant build failure that never names the contract.
  checkTableCoverage('session-info.json', 'session-info', Object.keys(v), [
    ['SESSION_INFO_TABLES', Object.fromEntries(SESSION_INFO_TABLES.map(t => [t.json, t]))],
  ])
  for (const table of SESSION_INFO_TABLES) {
    const entries = Object.entries(v[table.json])
    mustBe(entries.length > 0, 'session-info.json', `${table.json} must hold at least one entry`)
    const tokens = entries.map(([, token]) => token)
    // Per TABLE, not across tables: two different objects may legitimately carry
    // a field of the same name, but one object cannot carry the same field twice
    // -- the second name would generate a constant nothing can distinguish.
    mustBe(new Set(tokens).size === tokens.length, 'session-info.json', `two ${table.json} entries share one wire token`)
  }
  // Claude Code writes `total_cost_usd` on its own `result` line, and the worker
  // persists that line unchanged. The browser reads the persisted row through
  // SESSION_INFO_KEY.TotalCostUsd (extractResultMetadata in messageParser.ts), and
  // claude_output.go decodes the same field through a struct tag, which must be a
  // literal and cannot follow a rename. Anthropic owns this spelling, so LeapMux
  // cannot change it: a rename would generate cleanly, pass every test, and blank
  // the per-turn cost on every Claude result divider. Pi and ZCode inject the same
  // key under the generated constant, so the read cannot go back to a literal.
  mustBe(v.keys.TotalCostUsd === 'total_cost_usd', 'session-info.json', 'keys.TotalCostUsd must stay "total_cost_usd" -- Claude Code writes that spelling on its own result line, and the browser reads the persisted row through this constant, so a rename blanks the per-turn cost with no build failure')
  return {}
}

export function emitGoSessionInfo(v) {
  const blocks = SESSION_INFO_TABLES.map((table) => {
    const decls = Object.entries(v[table.json])
      .map(([name, token]) => ({ name: `${table.goPrefix}${name}`, value: jsonString(token) }))
    return `// ${table.goPrefix}* are the ${table.what}.
const (
${goConstBlock(decls)}
)`
  })
  return `${GO_HEADER('session-info.json')}package contracts

// The agent_session_info wire vocabulary: the keys of the ephemeral info map
// the Worker broadcasts, and the nested field names of its object-valued keys.
// The browser's SESSION_INFO_KEY and friends are generated from the same
// contracts/session-info.json.

${blocks.join('\n\n')}
`
}

export function emitTsSessionInfo(v) {
  const blocks = SESSION_INFO_TABLES.map((table) => {
    const rows = Object.entries(v[table.json])
      .map(([name, token]) => `  ${name}: ${jsonString(token)},`)
      .join('\n')
    return `/** The ${table.what}. */
export const ${table.ts} = {
${rows}
} as const

export type ${table.tsType} = typeof ${table.ts}[keyof typeof ${table.ts}]`
  })
  return `${TS_HEADER('session-info.json')}
// The agent_session_info wire vocabulary, generated from
// contracts/session-info.json (the Go worker's SessionInfoKey* constants and
// friends read the same tables).

${blocks.join('\n\n')}
`
}

// ---------------------------------------------------------------------------
// worker-vocab: the worker's wire vocabulary
// ---------------------------------------------------------------------------

export function checkWorkerVocab(v) {
  const entries = Object.entries(v.notificationTypes)
  const tokens = entries.map(([, token]) => token)
  mustBe(new Set(tokens).size === tokens.length, 'worker-vocab.json', 'two notification types share one wire token')
  mustBe(!tokens.includes(v.notificationThreadWrapperType), 'worker-vocab.json', `notificationThreadWrapperType ${JSON.stringify(v.notificationThreadWrapperType)} collides with a notificationTypes token -- the browser's thread probe routes on that exact value, so a colliding envelope would be misrouted`)
  for (const key of v.workerAuthoredNotificationTypes) {
    mustBe(v.notificationTypes[key] != null, 'worker-vocab.json', `workerAuthoredNotificationTypes specifies ${key}, which is not a notificationTypes key`)
  }
  mustBe(v.modelSentinels.accountDefaultModel !== v.modelSentinels.effortAuto, 'worker-vocab.json', 'the model sentinels must be distinct values')
  return {}
}

export function emitGoWorkerVocab(v) {
  const notif = goConstBlock(Object.entries(v.notificationTypes)
    .map(([key, token]) => ({ name: `NotificationType${key}`, value: jsonString(token) })))
  return `${GO_HEADER('worker-vocab.json')}package contracts

// The worker's wire vocabulary: notification-type tokens persisted inside
// notification envelopes, the notification-thread wrapper discriminator,
// the one Codex rateLimitReachedType that lifts on a timer, and the model
// sentinels. The browser's NOTIFICATION_TYPE and friends are generated from
// the same contracts/worker-vocab.json.

// NotificationType* are the notification envelope's inner "type" tokens.
const (
${notif}
)

// NotificationThreadWrapperType is the wrapper discriminator the worker's
// wrapNotifContent stamps on every notification-thread row.
const NotificationThreadWrapperType = ${jsonString(v.notificationThreadWrapperType)}

// CodexRateLimitReachedTimeWindow is the one Codex rateLimitReachedType that
// lifts on the rolling-window timer (the others are billing/usage caps).
const CodexRateLimitReachedTimeWindow = ${jsonString(v.codexRateLimitReachedTimeWindow)}

// Model sentinels: the account-default model resolves to a different concrete
// model on relaunch; "auto" is the effort a catalog default falls back to.
const (
${goConstBlock([
  { name: 'DefaultModelSentinel', value: jsonString(v.modelSentinels.accountDefaultModel) },
  { name: 'EffortAuto', value: jsonString(v.modelSentinels.effortAuto) },
])}
)
`
}

export function emitTsWorkerVocab(v) {
  const notif = Object.entries(v.notificationTypes)
    .map(([key, token]) => `  ${key}: ${jsonString(token)},`)
    .join('\n')
  const authored = v.workerAuthoredNotificationTypes
    .map(key => `  ${jsonString(v.notificationTypes[key])},`)
    .join('\n')
  return `${TS_HEADER('worker-vocab.json')}
// The worker's wire vocabulary, generated from contracts/worker-vocab.json
// (the Go agent package's NotificationType* constants and friends read the
// same tables).

/** Notification envelope "type" tokens. */
export const NOTIFICATION_TYPE = {
${notif}
} as const

export type NotificationType = typeof NOTIFICATION_TYPE[keyof typeof NOTIFICATION_TYPE]

/** The types the WORKER is the sole writer of (standalone rows, no plugin). */
export const WORKER_AUTHORED_NOTIFICATION_TYPES = [
${authored}
] as const

/** Wrapper discriminator on every notification-thread row (wrapNotifContent). */
export const NOTIFICATION_THREAD_TYPE = ${jsonString(v.notificationThreadWrapperType)} as const

/** The one Codex rateLimitReachedType that lifts on the rolling-window timer. */
export const CODEX_RATE_LIMIT_REACHED_TIME_WINDOW = ${jsonString(v.codexRateLimitReachedTimeWindow)} as const

/** Model sentinels: the account-default model, and the auto effort. */
export const ACCOUNT_DEFAULT_MODEL = ${jsonString(v.modelSentinels.accountDefaultModel)} as const
export const EFFORT_AUTO = ${jsonString(v.modelSentinels.effortAuto)} as const
`
}

// ---------------------------------------------------------------------------
// tab-names: the pool new agent / terminal tabs are named from
// ---------------------------------------------------------------------------

export function checkTabNames(v) {
  mustBe(v.titlePrefixes.agent !== v.titlePrefixes.terminal, 'tab-names.json', 'the agent and terminal title prefixes must differ -- a shared prefix makes "Agent Gabe" and "Terminal Gabe" the same title, and plan-mode auto-rename keys on the agent prefix alone')
  for (let i = 1; i < v.names.length; i++) {
    mustBe(v.names[i - 1] < v.names[i], 'tab-names.json', `names must be sorted: ${jsonString(v.names[i])} follows ${jsonString(v.names[i - 1])}`)
  }
  return {}
}

/**
 * A quoted string list, `perRow` entries to a line, each line opened with
 * `indent`. Several short entries per line keeps the width gofmt keeps and
 * keeps a diff readable.
 *
 * One function for both languages, because only the indent differs: a tab for
 * Go and two spaces for TS. Two near-identical copies are one place for an
 * escaping fix to be applied and the other to be missed.
 */
function stringRows(values, perRow, indent) {
  const rows = []
  for (let i = 0; i < values.length; i += perRow)
    rows.push(`${indent}${values.slice(i, i + perRow).map(jsonString).join(', ')},`)
  return rows.join('\n')
}

const goStringRows = (values, perRow) => stringRows(values, perRow, '\t')
const tsStringRows = (values, perRow) => stringRows(values, perRow, '  ')

export function emitGoTabNames(v) {
  return `${GO_HEADER('tab-names.json')}package contracts

// The pool new tabs are named from, generated from contracts/tab-names.json.
// The browser's TAB_NAMES reads the same table, so the worker's fallback name
// and the dialog's pre-filled name come from one list.

// AgentTitlePrefix / TerminalTitlePrefix begin an auto-generated tab title.
// The title is the prefix, one space, and a pooled name: "Agent Gabe".
const (
${goConstBlock([
  { name: 'AgentTitlePrefix', value: jsonString(v.titlePrefixes.agent) },
  { name: 'TerminalTitlePrefix', value: jsonString(v.titlePrefixes.terminal) },
])}
)

// TabNames is the pool itself. Sorted, and every entry matches
// ^[A-Z][A-Za-z]+$, which keeps a title readable and means nothing else.
var TabNames = []string{
${goStringRows(v.names, 8)}
}
`
}

export function emitTsTabNames(v) {
  return `${TS_HEADER('tab-names.json')}
// The pool new tabs are named from, generated from contracts/tab-names.json
// (the worker's contracts.TabNames reads the same table).

/** Prefixes an auto-generated tab title: \`\${prefix} \${name}\`. */
export const AGENT_TITLE_PREFIX = ${jsonString(v.titlePrefixes.agent)} as const
export const TERMINAL_TITLE_PREFIX = ${jsonString(v.titlePrefixes.terminal)} as const

/** Sorted; every entry matches the worker's ^[A-Z][A-Za-z]+$ title shape. */
export const TAB_NAMES: readonly string[] = [
${tsStringRows(v.names, 8)}
]
`
}

// ---------------------------------------------------------------------------
// captcha: the protected RPCs' action vocabulary
// ---------------------------------------------------------------------------

export function checkCaptcha(v) {
  const entries = Object.entries(v.actions)
  const tokens = entries.map(([, token]) => token)
  mustBe(new Set(tokens).size === tokens.length, 'captcha.json', 'two captcha actions share one token')
  for (const token of tokens) {
    mustBe(/^[a-z][a-z0-9_]*$/.test(token), 'captcha.json', `action ${JSON.stringify(token)} must use only lowercase alphanumerics and underscores -- both external providers accept that set`)
    mustBe(token.length <= 32, 'captcha.json', `action ${JSON.stringify(token)} exceeds Turnstile's 32-character action cap`)
  }
  return {}
}

export function emitGoCaptcha(v) {
  return `${GO_HEADER('captcha.json')}package contracts

// The captcha action vocabulary: the action name each protected RPC's token
// is minted under (reCAPTCHA's grecaptcha.execute({action}) and Turnstile's
// action parameter). The browser's CaptchaField action union is generated
// from the same contracts/captcha.json, so a rename cannot touch one side
// only.

// CaptchaAction* are the action tokens the hub verifies server-side.
const (
${goConstBlock(Object.entries(v.actions)
  .map(([key, token]) => ({ name: `CaptchaAction${key.charAt(0).toUpperCase()}${key.slice(1)}`, value: jsonString(token) })))}
)
`
}

export function emitTsCaptcha(v) {
  const actions = Object.entries(v.actions)
    .map(([key, token]) => `  ${key}: ${jsonString(token)},`)
    .join('\n')
  return `${TS_HEADER('captcha.json')}
// The captcha action vocabulary, generated from contracts/captcha.json
// (the hub's protectedProcedures map carries the same tokens).

/** The action each protected RPC's captcha token is minted under. */
export const CAPTCHA_ACTION = {
${actions}
} as const

export type CaptchaAction = typeof CAPTCHA_ACTION[keyof typeof CAPTCHA_ACTION]
`
}

// ---------------------------------------------------------------------------
// providers: the AgentProvider enum's human-facing vocabulary
// ---------------------------------------------------------------------------

/** Every input form ParseProvider accepts, per provider (displayName first). */
export function providerAliasTable(p) {
  const table = new Map()
  for (const [alias, name] of providerAliases(p)) {
    table.set(alias, name)
  }
  return table
}

/** Yields every accepted input form as [alias, providerName], the one enumeration the checker and both emitters share. */
function* providerAliases(p) {
  for (const [name, meta] of Object.entries(p.providers)) {
    for (const alias of [meta.displayName, meta.cliAlias, ...meta.parseAliases]) {
      yield [alias, name]
    }
  }
}

export function checkProviders(p, agentEnumValues) {
  const expected = agentEnumValues.filter(n => n !== 'AGENT_PROVIDER_UNSPECIFIED')
  const present = Object.keys(p.providers)
  for (const name of present) {
    // Same constraint as scopes: the TS emitter writes AgentProvider.<suffix>.
    mustBe(/^[A-Z][A-Z0-9_]*$/.test(name.replace(/^AGENT_PROVIDER_/, '')), 'providers.json', `enum value ${name} strips to a suffix that is not a valid TS member name -- protobuf enum values must stay letter-leading after the AGENT_PROVIDER_ prefix`)
  }
  for (const name of expected) {
    mustBe(p.providers[name] != null, 'providers.json', `proto enum value ${name} has no entry -- a new provider must land here in the same change, or every surface renders it blank`)
  }
  for (const name of present) {
    mustBe(expected.includes(name), 'providers.json', `entry ${name} matches no non-UNSPECIFIED AgentProvider enum value (removed from the proto?)`)
  }
  const seen = new Map()
  for (const [alias, name] of providerAliases(p)) {
    const owner = seen.get(alias)
    mustBe(owner === undefined, 'providers.json', `alias ${JSON.stringify(alias)} is claimed by both ${owner} and ${name}`)
    seen.set(alias, name)
  }
  return {}
}

export function emitGoProviders(p, agentEnumValues) {
  const goEnum = name => `leapmuxv1.AgentProvider_${name}`
  const ordered = agentEnumValues.filter(n => n !== 'AGENT_PROVIDER_UNSPECIFIED')
  const entries = Object.entries(p.providers)
  const display = goMapBlock(entries.map(([name, m]) => ({ key: `${goEnum(name)}:`, value: jsonString(m.displayName) })))
  const aliases = goMapBlock(entries.map(([name, m]) => ({ key: `${goEnum(name)}:`, value: jsonString(m.cliAlias) })))
  const reverse = goMapBlock([...providerAliasTable(p)]
    .sort(byFirstString)
    .map(([alias, name]) => ({ key: `${jsonString(alias)}:`, value: goEnum(name) })))
  const all = ordered.map(n => `\t${goEnum(n)},`).join('\n')
  return `${GO_HEADER('providers.json')}package contracts

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// The AgentProvider vocabulary: display names both sides render, the
// kebab-case CLI alias, and the reverse parse table. Keyed by the generated
// proto enum so an entry for a value the proto no longer carries fails to
// compile rather than silently rendering.

// ProviderDisplayName is enum -> user-facing label.
var ProviderDisplayName = map[leapmuxv1.AgentProvider]string{
${display}
}

// ProviderCLIAlias is enum -> the \`leapmux control\` identifier.
var ProviderCLIAlias = map[leapmuxv1.AgentProvider]string{
${aliases}
}

// ProviderParseAliases maps every accepted input form (display name, CLI
// alias, extra aliases) back to the enum.
var ProviderParseAliases = map[string]leapmuxv1.AgentProvider{
${reverse}
}

// AllProviders is every non-UNSPECIFIED value in proto order.
var AllProviders = []leapmuxv1.AgentProvider{
${all}
}
`
}

export function emitTsProviders(p, agentEnumValues) {
  const display = Object.entries(p.providers)
    .map(([name, m]) => `  [${AgentProviderKey(name)}]: ${jsonString(m.displayName)},`)
    .join('\n')
  // Proto order, matching the Go twin's AllProviders, so the pre-probe
  // fallback list the browser renders cannot drift from the CLI's list.
  const all = agentEnumValues
    .filter(n => n !== 'AGENT_PROVIDER_UNSPECIFIED')
    .map(n => `  ${AgentProviderKey(n)},`)
    .join('\n')
  return `${TS_HEADER('providers.json')}
// The AgentProvider vocabulary, generated from contracts/providers.json
// (agentlabels on the Go side reads the same tables). The browser renders
// display names and the fallback list; parsing stays with the Go twin (the
// CLI and admin RPCs accept the aliases), so no TS parse table is emitted.
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'

/** enum -> user-facing label (the agentProviderLabel source). UNSPECIFIED is absent: callers fall back. */
export const PROVIDER_DISPLAY_NAME: Readonly<Partial<Record<AgentProvider, string>>> = {
${display}
}

/** Every non-UNSPECIFIED provider in proto order (the Go twin is contracts.AllProviders). */
export const ALL_PROVIDERS: readonly AgentProvider[] = [
${all}
]
`
}

function AgentProviderKey(name) {
  // AGENT_PROVIDER_CLAUDE_CODE -> AgentProvider.CLAUDE_CODE
  return `AgentProvider.${name.replace(/^AGENT_PROVIDER_/, '')}`
}

// ---------------------------------------------------------------------------
// tab-types: the TabType wire vocabulary
//
// One token per enum value, consumed by the CLI (--type, --tab-type, the JSON
// envelopes and $LEAPMUX_CONTROL_TAB_TYPE) and by the browser (data-tab-type,
// the shortcut context). It was hand-written on eight surfaces before this
// contract, and adding a kind meant finding all eight.

export function checkTabTypes(t, tabEnumValues) {
  const present = Object.keys(t.tabTypes)
  for (const name of tabEnumValues)
    mustBe(t.tabTypes[name] != null, 'tab-types.json', `proto enum value ${name} has no entry -- a new tab kind must land here in the same change, or the CLI prints an empty --type and the browser writes an empty data-tab-type`)
  for (const name of present)
    mustBe(tabEnumValues.includes(name), 'tab-types.json', `entry ${name} matches no TabType enum value (removed from the proto?)`)
  // UNSPECIFIED is the one empty token: the CLI reads it as "no --type given".
  mustBe(t.tabTypes.TAB_TYPE_UNSPECIFIED?.wireToken === '', 'tab-types.json', 'TAB_TYPE_UNSPECIFIED must map to the empty token, which is what an omitted --type parses to')
  const seen = new Map()
  for (const [name, m] of Object.entries(t.tabTypes)) {
    if (name !== 'TAB_TYPE_UNSPECIFIED')
      mustBe(m.wireToken !== '', 'tab-types.json', `${name} must carry a non-empty token, or it cannot be named on the command line`)
    for (const token of [m.wireToken, ...m.parseAliases]) {
      if (token === '')
        continue
      const owner = seen.get(token)
      mustBe(owner === undefined, 'tab-types.json', `token ${JSON.stringify(token)} is claimed by both ${owner} and ${name}`)
      seen.set(token, name)
    }
  }
  return {}
}

export function emitGoTabTypes(t, tabEnumValues) {
  const goEnum = name => `leapmuxv1.TabType_${name}`
  const entries = tabEnumValues.map(name => [name, t.tabTypes[name]])
  const tokens = goMapBlock(entries.map(([name, m]) => ({ key: `${goEnum(name)}:`, value: jsonString(m.wireToken) })))
  // Both spellings parse: the short token and the proto-canonical name, so a
  // value pasted back out of a JSON envelope round-trips into a flag.
  const parseRows = []
  for (const [name, m] of entries) {
    for (const token of [m.wireToken, ...m.parseAliases])
      parseRows.push([token, name])
    parseRows.push([name, name])
  }
  const parse = goMapBlock(parseRows.sort(byFirstString).map(([token, name]) => ({ key: `${jsonString(token)}:`, value: goEnum(name) })))
  const named = tabEnumValues.filter(n => n !== 'TAB_TYPE_UNSPECIFIED')
  const accepted = named.map(n => jsonString(t.tabTypes[n].wireToken)).join(', ')
  return `${GO_HEADER('tab-types.json')}package contracts

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// The TabType wire vocabulary: the lowercase token the CLI accepts and emits,
// and the reverse parse table. Keyed by the generated proto enum so an entry for
// a value the proto no longer carries fails to compile rather than answering "".

// TabTypeWireToken is enum -> the token every surface spells it with.
// TAB_TYPE_UNSPECIFIED maps to "", which is what an omitted --type parses to.
var TabTypeWireToken = map[leapmuxv1.TabType]string{
${tokens}
}

// TabTypeParseAliases is token -> enum. It carries the short token and the
// proto-canonical name for every value, so a tab_type read out of a JSON
// envelope can be handed straight back as a flag.
var TabTypeParseAliases = map[string]leapmuxv1.TabType{
${parse}
}

// TabTypeAcceptedTokens lists the non-empty tokens, for an error message that
// must name what it accepts. Derived, so it cannot fall behind the table.
const TabTypeAcceptedTokens = ${jsonString(accepted)}
`
}

export function emitTsTabTypes(t, tabEnumValues) {
  const rows = tabEnumValues
    .map(name => `  [${TabTypeKey(name)}]: ${jsonString(t.tabTypes[name].wireToken)},`)
    .join('\n')
  return `${TS_HEADER('tab-types.json')}
// The TabType wire vocabulary, generated from contracts/tab-types.json (the Go
// twin reads the same table). The browser writes these tokens to data-tab-type,
// which the E2E locators address rows by, and keys the shortcut context on them.
// Parsing stays with the Go twin, which owns the command line.
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'

/** enum -> the token every surface spells it with. UNSPECIFIED is the empty string. */
export const TAB_TYPE_WIRE_TOKEN: Readonly<Record<TabType, string>> = {
${rows}
}
`
}

function TabTypeKey(name) {
  // TAB_TYPE_FILE -> TabType.FILE
  return `TabType.${name.replace(/^TAB_TYPE_/, '')}`
}

// ---------------------------------------------------------------------------
// scopes: the OAuth scope vocabulary
// ---------------------------------------------------------------------------

export function checkScopes(s, scopeEnumValues) {
  const grantable = Object.keys(s.scopes)
  const nonGrantable = s.nonGrantable
  const union = [...grantable, ...nonGrantable]
  for (const name of union) {
    // The TS emitter interpolates the prefix-stripped name into Scope.<suffix>;
    // a digit-leading suffix would emit invalid TypeScript while Go compiles.
    mustBe(/^[A-Z][A-Z0-9_]*$/.test(name.replace(/^SCOPE_/, '')), 'scopes.json', `enum value ${name} strips to a suffix that is not a valid TS member name -- protobuf enum values must stay letter-leading after the SCOPE_ prefix`)
  }
  for (const name of scopeEnumValues) {
    mustBe(union.includes(name), 'scopes.json', `proto enum value ${name} is in neither scopes nor nonGrantable -- the partition must be exact`)
  }
  for (const name of union) {
    mustBe(scopeEnumValues.includes(name), 'scopes.json', `${name} matches no Scope enum value (removed from the proto?)`)
  }
  mustBe(new Set(union).size === union.length, 'scopes.json', 'a scope appears in both scopes and nonGrantable')

  const tokens = grantable.map(n => s.scopes[n].token)
  mustBe(new Set(tokens).size === tokens.length, 'scopes.json', 'two scopes share one wire token')

  for (const [name, implies] of Object.entries(s.impliedBy)) {
    mustBe(s.scopes[name] != null, 'scopes.json', `impliedBy key ${name} is not a grantable scope`)
    mustBe(implies.length >= 1, 'scopes.json', `impliedBy[${name}] is empty -- drop the key instead`)
    for (const target of implies) {
      mustBe(s.scopes[target] != null, 'scopes.json', `impliedBy[${name}] refers to ${target}, which is not a grantable scope`)
    }
  }
  // Acyclicity: a cycle would make ScopeSet.Close loop forever on both sides.
  const state = new Map()
  const visit = (name) => {
    const st = state.get(name)
    if (st === 2)
      return
    if (st === 1)
      throw new ContractError('scopes.json', `impliedBy has a cycle through ${name}`)
    state.set(name, 1)
    for (const target of s.impliedBy[name] ?? [])
      visit(target)
    state.set(name, 2)
  }
  for (const name of grantable)
    visit(name)

  const seen = new Map()
  for (const cat of s.categories) {
    for (const name of cat.scopes) {
      const owner = seen.get(name)
      mustBe(owner === undefined, 'scopes.json', `scope ${name} appears in both the ${owner} and ${cat.label} categories`)
      mustBe(s.scopes[name] != null, 'scopes.json', `category ${cat.label} refers to non-grantable scope ${name}`)
      seen.set(name, cat.label)
    }
  }
  for (const name of grantable) {
    mustBe(seen.has(name), 'scopes.json', `grantable scope ${name} appears in no category -- both the consent screen and the Preferences catalogue render from categories`)
  }
  return {}
}

export function emitGoScopes(s, scopeEnumValues) {
  const goEnum = name => `leapmuxv1.Scope_${name}`
  const grantableOrder = scopeEnumValues.filter(n => s.scopes[n] != null)
  const entries = grantableOrder
  const tokens = goMapBlock(entries.map(n => ({ key: `${goEnum(n)}:`, value: jsonString(s.scopes[n].token) })))
  const byToken = goMapBlock([...entries].sort((a, b) => s.scopes[a].token < s.scopes[b].token ? -1 : s.scopes[a].token > s.scopes[b].token ? 1 : 0)
    .map(n => ({ key: `${jsonString(s.scopes[n].token)}:`, value: goEnum(n) })))
  const sentences = goMapBlock(entries.map(n => ({ key: `${goEnum(n)}:`, value: jsonString(s.scopes[n].consentSentence) })))
  const implied = entries
    .filter(n => s.impliedBy[n])
    .map(n => ({ key: `${goEnum(n)}:`, value: `[]leapmuxv1.Scope{${s.impliedBy[n].map(i => goEnum(i)).join(', ')}}` }))
  const impliedBlock = goMapBlock(implied)
  const cats = s.categories.map(c => `\t{${jsonString(c.label)}, []leapmuxv1.Scope{${c.scopes.map(i => goEnum(i)).join(', ')}}},`).join('\n')
  const grantable = grantableOrder.map(n => `\t${goEnum(n)},`).join('\n')
  return `${GO_HEADER('scopes.json')}package contracts

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// The OAuth scope vocabulary from contracts/scopes.json: wire tokens, the
// consent-screen sentences, the family grouping both surfaces render, and the
// implied-by graph. authscope and the OAuth consent pages consume these;
// the browser's scopeCatalogue reads the TS twin.

// ScopeToken is grantable scope -> wire token ("account:read").
var ScopeToken = map[leapmuxv1.Scope]string{
${tokens}
}

// ScopeByToken is the reverse of ScopeToken.
var ScopeByToken = map[string]leapmuxv1.Scope{
${byToken}
}

// ScopeConsentSentence is the sentence the consent screen renders -- the
// whole vocabulary of that page.
var ScopeConsentSentence = map[leapmuxv1.Scope]string{
${sentences}
}

// ScopeImpliedBy maps a scope to the grantable scopes it expands to.
var ScopeImpliedBy = map[leapmuxv1.Scope][]leapmuxv1.Scope{
${impliedBlock}
}

// ScopeCategory is one consent/catalogue family.
type ScopeCategory struct {
\tLabel  string
\tScopes []leapmuxv1.Scope
}

// ScopeCategories groups every grantable scope, in render order.
var ScopeCategories = []ScopeCategory{
${cats}
}

// GrantableScopes lists every grantable scope in enum order.
var GrantableScopes = []leapmuxv1.Scope{
${grantable}
}
`
}

export function emitTsScopes(s) {
  const key = name => `Scope.${name.replace(/^SCOPE_/, '')}`
  const entries = Object.keys(s.scopes)
  const union = entries.map(n => `  | ${key(n)}`).join('\n')
  const tokens = entries.map(n => `  [${key(n)}]: ${jsonString(s.scopes[n].token)},`).join('\n')
  const descriptions = entries.map(n => `  [${key(n)}]: ${jsonString(s.scopes[n].description)},`).join('\n')
  const implied = entries.filter(n => s.impliedBy[n])
    .map(n => `  [${key(n)}]: [${s.impliedBy[n].map(i => key(i)).join(', ')}],`)
    .join('\n')
  const cats = s.categories.map(c => `  { label: ${jsonString(c.label)}, scopes: [${c.scopes.map(i => key(i)).join(', ')}] },`).join('\n')
  const nonGrantable = s.nonGrantable.map(n => `  ${key(n)},`).join('\n')
  return `${TS_HEADER('scopes.json')}
// The OAuth scope vocabulary, generated from contracts/scopes.json
// (authscope and the OAuth consent pages read the Go twin).
import { Scope } from '~/generated/proto/leapmux/v1/scope_pb'

/** Every scope the hub can grant (the partition's grantable half; the Go twin is contracts.GrantableScopes). */
export type GrantableScope =
${union}

/** Grantable scope -> wire token ("account:read"). Total by construction: the generator emits one token per grantable scope. */
export const SCOPE_TOKENS: Readonly<Record<GrantableScope, string>> = {
${tokens}
}

/** Grantable scope -> the Preferences-dialog description. */
export const SCOPE_DESCRIPTIONS: Readonly<Record<GrantableScope, string>> = {
${descriptions}
}

/** A scope -> the grantable scopes it expands to. Absent key = implies nothing. */
export const IMPLIED_BY: Readonly<Partial<Record<GrantableScope, readonly GrantableScope[]>>> = {
${implied}
}

/** The family grouping both surfaces render, in order. */
export const SCOPE_CATEGORIES: readonly { readonly label: string, readonly scopes: readonly GrantableScope[] }[] = [
${cats}
]

/** Narrows hub wire data (typed as the full enum) down to the grantable half. */
export function isGrantableScope(scope: Scope): scope is GrantableScope {
  return Object.prototype.hasOwnProperty.call(SCOPE_TOKENS, scope)
}

/** Enum values that can never be granted (partition's other half). */
export const NON_GRANTABLE: readonly Scope[] = [
${nonGrantable}
]
`
}

// ---------------------------------------------------------------------------
// theme-default: the default palette and the OAuth pages' subset
// ---------------------------------------------------------------------------

/** The palette token a page token reads from (renames applied). */
export function pageTokenSource(t, renames) {
  return renames[t] ?? t
}

export function checkTheme(t) {
  for (const variant of ['light', 'dark']) {
    for (const token of t.oauthPage.tokens) {
      const source = pageTokenSource(token, t.oauthPage.renames)
      mustBe(t[variant][source] != null, 'theme-default.json', `oauthPage token ${token} (palette ${source}) is missing from the ${variant} palette`)
    }
  }
  for (const [page, palette] of Object.entries(t.oauthPage.renames)) {
    mustBe(t.oauthPage.tokens.includes(page), 'theme-default.json', `rename key ${page} is not in oauthPage.tokens -- a rename for an unlisted token is dead data: the page never defines that CSS variable`)
    mustBe(t.light[palette] != null && t.dark[palette] != null, 'theme-default.json', `rename target ${palette} (page token ${page}) is missing from a palette`)
  }
  return {}
}

export function emitTsTheme(t) {
  const palette = (name) => {
    const entries = Object.entries(t[name])
      .map(([k, v]) => `  ${jsonString(k)}: ${jsonString(v)},`)
      .join('\n')
    return `export const ${name} = {\n${entries}\n} as const\n`
  }
  return `${TS_HEADER('theme-default.json')}
// The default palette's full token maps, generated from
// contracts/theme-default.json. PLAIN DATA with no imports: the themes
// directory imports this by relative path (see styles/themes/types.ts for
// why plain data matters -- generate-notice.mjs resolves the chain under
// bare bun, with no Vite and no alias).
${palette('light')}\n${palette('dark')}`
}

export function emitGoTheme(t) {
  const block = variant => t.oauthPage.tokens
    .map(token => `\t\t${token}: ${t[variant][pageTokenSource(token, t.oauthPage.renames)]};`)
    .join('\n')
  const css = `:root {
\tcolor-scheme: light dark;
${block('light')}
}
@media (prefers-color-scheme: dark) {
\t:root {
${block('dark')}
\t}
}`
  return `${GO_HEADER('theme-default.json')}package contracts

// OAuthPagePaletteCSS is the default palette's curated subset for the
// server-rendered OAuth pages (CSP default-src 'none' forbids linking the
// SPA stylesheet), light then dark under the system preference. The page
// tokens rename one palette token (--danger-subtle is the palette's
// --lm-danger-subtle: the page has no lm- namespace to keep).
const OAuthPagePaletteCSS = ${goRawString(css)}
`
}

/** A Go raw string literal, safe because palette values carry no backquotes. */
function goRawString(s) {
  if (s.includes('`'))
    throw new ContractError('theme-default.json', 'palette CSS contains a backquote')
  return `\`${s}\``
}

// ---------------------------------------------------------------------------
// validate: cross-language validation policy parameters
// ---------------------------------------------------------------------------

const hex4 = n => `0x${n.toString(16).padStart(4, '0').toUpperCase()}`

/** A JS regex character-class source: \uXXXX or \uXXXX-\uXXXX per range. */
export function tsClassSource(ranges) {
  return ranges.map(([lo, hi]) => lo === hi ? `\\u${lo.toString(16).padStart(4, '0')}` : `\\u${lo.toString(16).padStart(4, '0')}-\\u${hi.toString(16).padStart(4, '0')}`).join('')
}

function checkRanges(ranges, file, where) {
  let prevHi = -1
  for (const [lo, hi] of ranges) {
    mustBe(lo <= hi, file, `${where}: range [${lo}, ${hi}] is inverted`)
    mustBe(lo > prevHi, file, `${where}: ranges must be sorted and non-overlapping (${lo} follows ${prevHi})`)
    mustBe(hi <= 0xFFFF, file, `${where}: range [${lo}, ${hi}] leaves the BMP -- both emitters encode code points as 4-hex-digit forms (TS \\uXXXX class escapes, Go unicode.Range16 with uint16 fields)`)
    prevHi = hi
  }
}

export function checkValidate(v) {
  checkRanges(v.name.invisibleFormat, 'validate.json', 'name.invisibleFormat')
  checkRanges(v.name.whitespaceFold, 'validate.json', 'name.whitespaceFold')
  checkRanges(v.session.invisibleFormat, 'validate.json', 'session.invisibleFormat')
  checkRanges(v.session.refusedControl, 'validate.json', 'session.refusedControl')
  checkRanges(v.branch.refusedControl, 'validate.json', 'branch.refusedControl')
  // refusedAscii passes through the same checkRanges as every named table:
  // the TS emitter encodes each entry as 4-hex-digit \uXXXX escapes, so an
  // astral hi end would silently truncate to a different character class
  // than the Go rune list, and an inverted or unsorted entry would build a
  // class the browser cannot compile.
  checkRanges(v.session.refusedAscii, 'validate.json', 'session.refusedAscii')
  checkRanges(v.branch.refusedAscii, 'validate.json', 'branch.refusedAscii')
  // The session rule is FROZEN to the name rule: session.invisibleFormat
  // repeats name.invisibleFormat so a name-rule change cannot move what a
  // token may hold. Enforce the repetition here, next to the data, instead
  // of in a consumer's mirror test.
  mustBe(JSON.stringify(v.name.invisibleFormat) === JSON.stringify(v.session.invisibleFormat), 'validate.json', 'session.invisibleFormat must repeat name.invisibleFormat exactly -- the session rule is FROZEN; a human must decide whether both lists move')
  // A resume handle comes in two shapes and each has its own cap. The token
  // cap was applied to a session FILE PATH once and refused every real one --
  // a path holds a directory prefix a token never does. Keeping the file-path
  // cap strictly larger states that relation in the data, so a later edit
  // cannot reintroduce the refusal by lowering one number.
  mustBe(v.session.filePathByteLimit > v.session.byteLimit, 'validate.json', 'session.filePathByteLimit must be > session.byteLimit -- a session file path carries a directory prefix that a token does not')
  mustBe(v.password.minLength <= v.password.maxLength, 'validate.json', 'password.minLength must be <= maxLength')
  mustBe(v.password.printableAsciiMin <= v.password.printableAsciiMax, 'validate.json', 'password printable ASCII range is inverted')
  const system = Object.keys(v.usernames.systemReserved)
  const publicOnly = Object.keys(v.usernames.publicReserved)
  for (const name of publicOnly) {
    mustBe(!system.includes(name), 'validate.json', `username ${name} is in both systemReserved and publicReserved -- a system reservation already covers every path`)
  }
  // The Go emitter builds the const identifier Username<Name> from each
  // reserved username by case-mangling alone; a name the mangle cannot turn
  // into a valid identifier must fail generation, not the Go build.
  for (const name of [...system, ...publicOnly]) {
    const ident = `Username${name[0].toUpperCase()}${name.slice(1)}`
    mustBe(/^[A-Z][A-Za-z0-9]*$/.test(ident), 'validate.json', `reserved username ${JSON.stringify(name)} mangles to ${ident}, which is not a valid Go identifier -- give the emitter a name table entry for this username`)
  }
  return {}
}

function goRangeTable(ranges) {
  // The annotation (third tuple element) becomes a trailing comment; every
  // literal is the same width (hex4 pads to 4), so consecutive comments stay
  // aligned the way gofmt aligns them without needing a reformat pass.
  const rows = ranges.map(([lo, hi, name]) => {
    const lit = `\t\t{Lo: ${hex4(lo)}, Hi: ${hex4(hi)}, Stride: 1},`
    return name ? `${lit} // ${name}` : lit
  }).join('\n')
  const latinOffset = ranges.filter(([, hi]) => hi <= 0xFF).length
  return `&unicode.RangeTable{
\tR16: []unicode.Range16{
${rows}
\t},
\tLatinOffset: ${latinOffset},
}`
}

/** Every code point a refused-ASCII list bans, as hex4 literals in order. */
function refusedRunes(ranges) {
  return ranges.flatMap(([lo, hi]) => {
    const out = []
    for (let c = lo; c <= hi; c++)
      out.push(hex4(c))
    return out
  }).join(', ')
}

/** The refused-ASCII list's names, for the trailing comment beside the runes. */
function refusedNames(ranges) {
  return ranges.map(([, , name]) => name).join(', ')
}

export function emitGoValidate(v) {
  return `${GO_HEADER('validate.json')}package contracts

import "unicode"

// Validation policy parameters shared with the browser (frontend
// src/lib/validate.ts is generated from the same contracts/validate.json).
// The scanning/cleaning ALGORITHMS stay per language; these are the tables
// and limits each algorithm enforces, so the two cannot disagree about WHAT
// is stripped, folded, or refused -- only about how it is worded.

const (
${goConstBlock([
  { name: 'NameByteLimit', value: String(v.name.byteLimit) },
  { name: 'SessionIDByteLimit', value: String(v.session.byteLimit) },
  { name: 'SessionFilePathByteLimit', value: String(v.session.filePathByteLimit) },
  { name: 'BranchByteLimit', value: String(v.branch.byteLimit) },
  { name: 'MinPasswordLength', value: String(v.password.minLength) },
  { name: 'MaxPasswordLength', value: String(v.password.maxLength) },
  { name: 'MinPrintableASCII', value: String(v.password.printableAsciiMin) },
  { name: 'MaxPrintableASCII', value: String(v.password.printableAsciiMax) },
])}
)

// NameInvisibleFormat is the invisible format characters a name loses.
var NameInvisibleFormat = ${goRangeTable(v.name.invisibleFormat)}

// NameWhitespaceFold is the characters a name rule folds to one space.
var NameWhitespaceFold = ${goRangeTable(v.name.whitespaceFold)}

// SessionInvisibleFormat is the session rule's own format-character class.
var SessionInvisibleFormat = ${goRangeTable(v.session.invisibleFormat)}

// SessionRefusedControl is the control ranges a session ID refuses -- the
// twin of the browser's SESSION_FORBIDDEN_CLASS control half.
var SessionRefusedControl = ${goRangeTable(v.session.refusedControl)}

// SessionRefusedASCII holds the printable ASCII a session ID may not carry,
// named from the contract so the list cannot go stale against the JSON.
var SessionRefusedASCII = []rune{${refusedRunes(v.session.refusedAscii)}} // ${refusedNames(v.session.refusedAscii)}

// BranchForbiddenASCII holds the printable ASCII git refuses in a ref name
// (excluding the controls, which BranchRefusedControl covers), named from
// the contract.
var BranchForbiddenASCII = []rune{${refusedRunes(v.branch.refusedAscii)}} // ${refusedNames(v.branch.refusedAscii)}

// BranchRefusedControl is the control ranges git refuses in a ref name.
var BranchRefusedControl = ${goRangeTable(v.branch.refusedControl)}

${goReservedUsernames(v)}
`
}

/** Reserved usernames: named consts plus lookup sets for the predicates. */
function goReservedUsernames(v) {
  // The schema admits a HYPHEN in a username, and Go accepts none in an
  // identifier -- `UsernameRead-only` is a syntax error in a generated file
  // nobody reads before the compiler does. Each hyphenated segment is
  // capitalized instead, which is Go's own spelling for the same words.
  const goConst = name => `Username${name.split('-').map(part => part[0].toUpperCase() + part.slice(1)).join('')}`
  const block = (kind, label) => {
    const names = Object.keys(v.usernames[kind])
    const consts = goConstBlock(names.map(name => ({ name: goConst(name), value: jsonString(name) })))
    const map = goMapBlock(names.map(name => ({ key: `${goConst(name)}:`, value: 'true' })))
    return `// ${label}
const (
${consts}
)

var Usernames${kind[0].toUpperCase()}${kind.slice(1)} = map[string]bool{
${map}
}`
  }
  return `${block('systemReserved', 'Reserved in EVERY creation path (system accounts).')}

${block('publicReserved', 'Reserved in anonymous public signup only (claimable by the first admin).')}

// UsernamesSystemReserved / UsernamesPublicReserved are the lookup sets the
// reserved predicates read; the consts above are the canonical account names.`
}

/** Subtract `gaps` from `ranges`; all sorted, non-overlapping. */
export function subtractRanges(ranges, gaps) {
  const out = []
  let gi = 0
  for (const [lo, hi] of ranges) {
    let start = lo
    while (gi < gaps.length && gaps[gi][1] < start)
      gi++
    for (const [glo, ghi] of gaps.slice(gi)) {
      if (glo > hi)
        break
      if (glo > start)
        out.push([start, glo - 1])
      start = Math.max(start, ghi + 1)
      if (start > hi)
        break
    }
    if (start <= hi)
      out.push([start, hi])
  }
  return out
}

/** The full strip class: Cc minus the whitespace folds, plus the format set. */
export function nameStripClass(v) {
  const cc = [[0, 0x1F], [0x7F, 0x9F]]
  const foldInCc = v.name.whitespaceFold.filter(([lo]) => lo <= 0x9F)
  return [...subtractRanges(cc, foldInCc), ...v.name.invisibleFormat]
}

/** One annotation line per named range: `\\uXXXX-\\uYYYY  NAME`. */
function tsRangeLine([lo, hi, name]) {
  const esc = n => `\\u${n.toString(16).padStart(4, '0')}`
  return ` *   ${esc(lo)}${hi !== lo ? `-${esc(hi)}` : ''}  ${name}`
}

/** A companion comment block naming a class's ranges, from the contract. */
function tsAnnotation(lines) {
  return `/**
 * Range names, from contracts/validate.json:
${lines.map(tsRangeLine).join('\n')}
 */`
}

/**
 * The derived control half of the strip class carries no contract name (it is
 * computed, not listed), so each computed range gets the one label that states
 * what it is.
 */
function nameStripControlAnnotation(v) {
  return nameStripClass(v)
    .filter(r => r[2] === undefined)
    .map(([lo, hi]) => [lo, hi, 'Cc minus the whitespace folds (derived)'])
}

export function emitTsValidate(v) {
  const printable = tsClassSource([[v.password.printableAsciiMin, v.password.printableAsciiMax]])
  const branchClass = tsClassSource([...v.branch.refusedControl, ...v.branch.refusedAscii])
  const sessionClass = tsClassSource([...v.session.refusedControl, ...v.session.refusedAscii, ...v.session.invisibleFormat])
  return `${TS_HEADER('validate.json')}
// Validation policy parameters, generated from contracts/validate.json (the
// Go validators read tables generated from the same file). Class sources are
// regex character-class BODIES: build with new RegExp(\`[\${X}]\`).
// NAME_INVISIBLE_CLASS is the DERIVED full strip class (Cc minus the
// whitespace folds, plus the format set), computed here so neither language
// re-derives it. The companion comment above each class names its ranges.

export const NAME_BYTE_LIMIT = ${v.name.byteLimit} as const
export const SESSION_ID_BYTE_LIMIT = ${v.session.byteLimit} as const
export const SESSION_FILE_PATH_BYTE_LIMIT = ${v.session.filePathByteLimit} as const
export const BRANCH_NAME_BYTE_LIMIT = ${v.branch.byteLimit} as const
export const MIN_PASSWORD_LENGTH = ${v.password.minLength} as const
export const MAX_PASSWORD_LENGTH = ${v.password.maxLength} as const

${tsAnnotation([...nameStripControlAnnotation(v), ...v.name.invisibleFormat])}
/** Everything a title loses: Cc minus the folds, plus invisible format characters. */
export const NAME_INVISIBLE_CLASS = ${jsonString(tsClassSource(nameStripClass(v)))} as const

${tsAnnotation(v.name.whitespaceFold)}
/** Characters a title rule folds to one space. */
export const NAME_WHITESPACE_CLASS = ${jsonString(tsClassSource(v.name.whitespaceFold))} as const

/** Every fold character except the plain space, computed here so no consumer subtracts by string surgery on the class source. */
export const NAME_WHITESPACE_MINUS_SPACE_CLASS = ${jsonString(tsClassSource(subtractRanges(v.name.whitespaceFold, [[32, 32]])))} as const

${tsAnnotation([...v.session.refusedControl, ...v.session.refusedAscii, ...v.session.invisibleFormat])}
/** Everything a session ID may not contain (controls, refused ASCII, format characters). */
export const SESSION_FORBIDDEN_CLASS = ${jsonString(sessionClass)} as const

${tsAnnotation(v.session.invisibleFormat)}
/**
 * The invisible-format half of the session class, on its own.
 *
 * The PATH shape of a resume handle cannot use the whole class: it carries a
 * backslash as a separator, and may carry a dollar or a percent sign, all of
 * which the token class bans. It still has to refuse THESE, because the
 * worker's SanitizePath drops a control character and trims edge whitespace
 * but leaves a format character alone -- U+200B is Cf, not Cc -- so one would
 * reach the agent inside a filename and name a session that does not exist.
 * The Go twin is contracts.SessionInvisibleFormat.
 */
export const SESSION_INVISIBLE_CLASS = ${jsonString(tsClassSource(v.session.invisibleFormat))} as const

/** Printable ASCII, the whole character set a password may hold. */
export const PRINTABLE_ASCII_CLASS = ${jsonString(printable)} as const

${tsAnnotation([...v.branch.refusedControl, ...v.branch.refusedAscii])}
/** Everything a git branch name may not contain (controls + refused ASCII). */
export const BRANCH_FORBIDDEN_CLASS = ${jsonString(branchClass)} as const

/** Reserved in EVERY creation path (system accounts). */
export const SYSTEM_RESERVED_USERNAMES: readonly string[] = [${Object.keys(v.usernames.systemReserved).map(jsonString).join(', ')}]

/** Reserved in anonymous public signup only (claimable by the first admin). */
export const PUBLIC_RESERVED_USERNAMES: readonly string[] = [${Object.keys(v.usernames.publicReserved).map(jsonString).join(', ')}]

${tsUsernameConsts(v.usernames)}`
}

/**
 * The reserved usernames as NAMED constants, mirroring the Go emitter's
 * `UsernameSolo`.
 *
 * The two arrays above answer "is this name reserved". A caller that has to
 * WRITE one needs the name itself -- the sign-in form on a solo hub pre-fills
 * its single account -- and reading it out of an array by index would depend
 * on an order the contract does not promise.
 */
export function tsUsernameConsts(usernames) {
  const lines = []
  for (const kind of ['systemReserved', 'publicReserved']) {
    for (const [name, doc] of Object.entries(usernames[kind])) {
      lines.push(`/** ${doc} */`)
      lines.push(`export const ${usernameConstName(name)} = ${jsonString(name)} as const`)
      lines.push('')
    }
  }
  return lines.join('\n')
}

/**
 * The constant name for one reserved username.
 *
 * The schema admits a HYPHEN in a username (`^[a-z][a-z0-9-]*$`), and neither
 * language accepts one in an identifier -- `USERNAME_READ-ONLY` and
 * `UsernameRead-only` are both syntax errors, in generated files nobody reads
 * before the compiler does. It folds the hyphen rather than refusing the name,
 * because the name is the contract and the identifier is this emitter's own
 * problem.
 */
export function usernameConstName(name) {
  return `USERNAME_${name.toUpperCase().replaceAll('-', '_')}`
}

// ---------------------------------------------------------------------------
// desktop: the shell's cross-language vocabulary (Rust <-> Go sidecar <-> webview)
// ---------------------------------------------------------------------------

export const DESKTOP_GO_ENV_NAMES = {
  devEndpoint: 'EnvDevEndpoint',
  binaryHash: 'EnvBinaryHash',
  devFrontend: 'EnvDevFrontend',
}

export const DESKTOP_RS_ENV_NAMES = {
  devEndpoint: 'ENV_DEV_ENDPOINT',
  binaryHash: 'ENV_BINARY_HASH',
  devFrontend: 'ENV_DEV_FRONTEND',
}

export const DESKTOP_RS_EVENT_NAMES = {
  channelMessage: 'EVENT_CHANNEL_MESSAGE',
  channelClose: 'EVENT_CHANNEL_CLOSE',
  userEventsMessage: 'EVENT_USER_EVENTS_MESSAGE',
  userEventsClose: 'EVENT_USER_EVENTS_CLOSE',
  sidecarLog: 'EVENT_SIDECAR_LOG',
  menuShowAbout: 'EVENT_MENU_SHOW_ABOUT',
  menuShowPreferences: 'EVENT_MENU_SHOW_PREFERENCES',
}

// Tauri events whose only Rust emission sites sit inside
// #[cfg(target_os = "macos")] code (the native app menu; Linux and Windows
// render the menu in the webview). Their consts carry a non-macOS
// dead_code allow so `cargo clippy -D warnings` stays green off macOS.
export const DESKTOP_RS_MACOS_ONLY_EVENTS = new Set(['menuShowAbout', 'menuShowPreferences'])

export const DESKTOP_TS_EVENT_NAMES = {
  channelMessage: 'TAURI_EVENT_CHANNEL_MESSAGE',
  channelClose: 'TAURI_EVENT_CHANNEL_CLOSE',
  userEventsMessage: 'TAURI_EVENT_USER_EVENTS_MESSAGE',
  userEventsClose: 'TAURI_EVENT_USER_EVENTS_CLOSE',
  sidecarLog: 'TAURI_EVENT_SIDECAR_LOG',
  menuShowAbout: 'TAURI_EVENT_MENU_SHOW_ABOUT',
  menuShowPreferences: 'TAURI_EVENT_MENU_SHOW_PREFERENCES',
}

// The Desktop account settings' enum tokens, in the three languages that spell
// them: the hub declares and validates them (usersettings/keys.go), the webview
// parses its device tier with them, and the Rust shell matches them out of the
// set_desktop_behavior payload. The other account settings' tokens stay in Go
// alone, because only Go and the webview read those and the webview reads them
// off the wire.
export const DESKTOP_GO_BEHAVIOR_NAMES = {
  trayOnCloseTray: 'TrayOnCloseTray',
  trayOnCloseQuit: 'TrayOnCloseQuit',
  trayOnMinimizeTray: 'TrayOnMinimizeTray',
  trayOnMinimizeTaskbar: 'TrayOnMinimizeTaskbar',
  startMinimizedWindow: 'StartMinimizedWindow',
  startMinimizedMinimized: 'StartMinimizedMinimized',
}

export const DESKTOP_RS_BEHAVIOR_NAMES = {
  trayOnCloseTray: 'TRAY_ON_CLOSE_TRAY',
  trayOnCloseQuit: 'TRAY_ON_CLOSE_QUIT',
  trayOnMinimizeTray: 'TRAY_ON_MINIMIZE_TRAY',
  trayOnMinimizeTaskbar: 'TRAY_ON_MINIMIZE_TASKBAR',
  startMinimizedWindow: 'START_MINIMIZED_WINDOW',
  startMinimizedMinimized: 'START_MINIMIZED_MINIMIZED',
}

export const DESKTOP_TS_BEHAVIOR_NAMES = {
  trayOnCloseTray: 'TRAY_ON_CLOSE_TRAY',
  trayOnCloseQuit: 'TRAY_ON_CLOSE_QUIT',
  trayOnMinimizeTray: 'TRAY_ON_MINIMIZE_TRAY',
  trayOnMinimizeTaskbar: 'TRAY_ON_MINIMIZE_TASKBAR',
  startMinimizedWindow: 'START_MINIMIZED_WINDOW',
  startMinimizedMinimized: 'START_MINIMIZED_MINIMIZED',
}

/**
 * Flatten `windowBehavior` to the `<setting><Value>` keys the name tables use.
 *
 * The contract NESTS one object per setting, so the grouping is data the schema
 * enforces and the per-setting uniqueness rule reads straight off it. The
 * emitted constant names stay flat, which is the same split `flattenWire` makes
 * for wire.json's nested blocks.
 */
function flattenBehavior(b) {
  // DERIVED, not a fixed list of the six keys: a setting added to the contract
  // must reach the coverage check below as an unknown key, so the generator
  // refuses it until the three name tables carry it. A hardcoded list would
  // drop the new setting here and emit nothing for it, in silence.
  return Object.fromEntries(
    Object.entries(b).flatMap(([setting, tokens]) =>
      Object.entries(tokens).map(([value, token]) =>
        [`${setting}${value[0].toUpperCase()}${value.slice(1)}`, token])),
  )
}

// The launch-visibility tokens. Rust and TS only, like the Tauri events: the
// shell reports one through get_startup_info and the webview parses it. Unlike
// windowBehavior this block is ONE setting, so all three tokens must differ.
export const DESKTOP_RS_LAUNCH_NAMES = {
  normal: 'LAUNCH_VISIBILITY_NORMAL',
  minimized: 'LAUNCH_VISIBILITY_MINIMIZED',
  hidden: 'LAUNCH_VISIBILITY_HIDDEN',
}

export const DESKTOP_TS_LAUNCH_NAMES = {
  normal: 'LAUNCH_VISIBILITY_NORMAL',
  minimized: 'LAUNCH_VISIBILITY_MINIMIZED',
  hidden: 'LAUNCH_VISIBILITY_HIDDEN',
}

// The saved window-mode tokens, in all THREE languages: the Go config persists
// one, the Rust shell matches it at launch, and the webview reads and writes it
// through save_window_geometry.
export const DESKTOP_GO_WINDOW_MODE_NAMES = {
  normal: 'WindowModeNormal',
  maximized: 'WindowModeMaximized',
  fullscreen: 'WindowModeFullscreen',
}

export const DESKTOP_RS_WINDOW_MODE_NAMES = {
  normal: 'WINDOW_MODE_NORMAL',
  maximized: 'WINDOW_MODE_MAXIMIZED',
  fullscreen: 'WINDOW_MODE_FULLSCREEN',
}

export const DESKTOP_TS_WINDOW_MODE_NAMES = {
  normal: 'WINDOW_MODE_NORMAL',
  maximized: 'WINDOW_MODE_MAXIMIZED',
  fullscreen: 'WINDOW_MODE_FULLSCREEN',
}

export function checkDesktop(d) {
  const envNames = Object.values(d.envVars)
  mustBe(new Set(envNames).size === envNames.length, 'desktop.json', 'two env vars share one name')
  const events = Object.values(d.tauriEvents)
  mustBe(new Set(events).size === events.length, 'desktop.json', 'two Tauri events share one name')
  checkTableCoverage('desktop.json', 'envVars', Object.keys(d.envVars), [
    ['DESKTOP_GO_ENV_NAMES', DESKTOP_GO_ENV_NAMES],
    ['DESKTOP_RS_ENV_NAMES', DESKTOP_RS_ENV_NAMES],
  ])
  mustBe(
    [...DESKTOP_RS_MACOS_ONLY_EVENTS].every(k => k in DESKTOP_RS_EVENT_NAMES),
    'desktop.json',
    'DESKTOP_RS_MACOS_ONLY_EVENTS names a key missing from DESKTOP_RS_EVENT_NAMES',
  )
  checkTableCoverage('desktop.json', 'tauriEvents', Object.keys(d.tauriEvents), [
    ['DESKTOP_RS_EVENT_NAMES', DESKTOP_RS_EVENT_NAMES],
    ['DESKTOP_TS_EVENT_NAMES', DESKTOP_TS_EVENT_NAMES],
  ])
  // One rule, applied to each block of tokens that is ONE choice: a setting
  // whose two values are the same string offers no choice at all. Never across
  // `windowBehavior` as a whole, because `tray` is deliberately the token of
  // both close-to-tray and minimize-to-tray.
  //
  // Before the coverage check below, so a setting the name tables do not know
  // yet is still checked -- which is the order a real change arrives in.
  const checkTokenBlock = (name, tokens) => mustBe(
    new Set(Object.values(tokens)).size === Object.keys(tokens).length,
    'desktop.json',
    `${name} declares one token twice, so it offers one choice`,
  )
  for (const [setting, tokens] of Object.entries(d.windowBehavior))
    checkTokenBlock(`windowBehavior.${setting}`, tokens)
  checkTokenBlock('launchVisibility', d.launchVisibility)
  checkTokenBlock('windowMode', d.windowMode)

  checkTableCoverage('desktop.json', 'windowBehavior', Object.keys(flattenBehavior(d.windowBehavior)), [
    ['DESKTOP_GO_BEHAVIOR_NAMES', DESKTOP_GO_BEHAVIOR_NAMES],
    ['DESKTOP_RS_BEHAVIOR_NAMES', DESKTOP_RS_BEHAVIOR_NAMES],
    ['DESKTOP_TS_BEHAVIOR_NAMES', DESKTOP_TS_BEHAVIOR_NAMES],
  ])
  checkTableCoverage('desktop.json', 'launchVisibility', Object.keys(d.launchVisibility), [
    ['DESKTOP_RS_LAUNCH_NAMES', DESKTOP_RS_LAUNCH_NAMES],
    ['DESKTOP_TS_LAUNCH_NAMES', DESKTOP_TS_LAUNCH_NAMES],
  ])
  checkTableCoverage('desktop.json', 'windowMode', Object.keys(d.windowMode), [
    ['DESKTOP_GO_WINDOW_MODE_NAMES', DESKTOP_GO_WINDOW_MODE_NAMES],
    ['DESKTOP_RS_WINDOW_MODE_NAMES', DESKTOP_RS_WINDOW_MODE_NAMES],
    ['DESKTOP_TS_WINDOW_MODE_NAMES', DESKTOP_TS_WINDOW_MODE_NAMES],
  ])
  mustBe(
    typeof d.devFrontendUrl === 'string' && d.devFrontendUrl.length > 0,
    'desktop.json',
    'devFrontendUrl must be a non-empty URL string',
  )
  return {}
}

export function emitGoDesktop(d) {
  return `${GO_HEADER('desktop.json')}package contracts

// The desktop shell's cross-language vocabulary: the env vars the Rust
// shell passes when spawning the Go sidecar (the sidecar reads them in
// main.go), and the frame cap both programs enforce on the sidecar IPC
// wire. The Tauri event names are Rust<->webview only and ride in the
// Rust/TS outputs.
const (
${goConstBlock(Object.keys(DESKTOP_GO_ENV_NAMES).map(k => ({ name: DESKTOP_GO_ENV_NAMES[k], value: jsonString(d.envVars[k]) })))}
)

// MaxFrameSizeBytes caps a single desktop RPC frame. It must exceed the
// largest payload the sidecar relays -- a userevents UserMaterialized
// bootstrap up to channelwire.UserEventsReadLimit -- plus its Frame/Event
// proto envelope; the Rust shell enforces the same cap on read.
const MaxFrameSizeBytes = ${d.maxFrameSizeBytes}

// DevFrontendURL is the Vite/Bun DEV origin the Rust debug spawn writes into
// LEAPMUX_HUB_DEV_FRONTEND. It must match tauri.conf.json build.devUrl.
const DevFrontendURL = ${jsonString(d.devFrontendUrl)}

// The enum tokens of the Desktop account settings. usersettings/keys.go builds
// each key's enum catalogue from these, and validateEnum derives the write-path
// rule from that same catalogue, so a token is stated once for the hub, the
// webview and the Rust shell together.
const (
${goConstBlock(Object.entries(flattenBehavior(d.windowBehavior)).map(([k, v]) => ({ name: DESKTOP_GO_BEHAVIOR_NAMES[k], value: jsonString(v) })))}
)

// The saved display state of the main window. DesktopConfig persists one of
// these tokens verbatim, and the Rust shell and the webview match the same
// three, so the wire carries no second spelling of them.
const (
${goConstBlock(Object.keys(DESKTOP_GO_WINDOW_MODE_NAMES).map(k => ({ name: DESKTOP_GO_WINDOW_MODE_NAMES[k], value: jsonString(d.windowMode[k]) })))}
)
`
}

export function emitTsDesktop(d) {
  const lines = Object.keys(DESKTOP_TS_EVENT_NAMES)
    .map(k => `export const ${DESKTOP_TS_EVENT_NAMES[k]} = ${jsonString(d.tauriEvents[k])} as const\n`)
    .join('')
  const behavior = Object.entries(flattenBehavior(d.windowBehavior))
    .map(([k, v]) => `export const ${DESKTOP_TS_BEHAVIOR_NAMES[k]} = ${jsonString(v)} as const\n`)
    .join('')
  const launch = Object.keys(DESKTOP_TS_LAUNCH_NAMES)
    .map(k => `export const ${DESKTOP_TS_LAUNCH_NAMES[k]} = ${jsonString(d.launchVisibility[k])} as const\n`)
    .join('')
  const windowMode = Object.keys(DESKTOP_TS_WINDOW_MODE_NAMES)
    .map(k => `export const ${DESKTOP_TS_WINDOW_MODE_NAMES[k]} = ${jsonString(d.windowMode[k])} as const\n`)
    .join('')
  return `${TS_HEADER('desktop.json')}
// Tauri events the desktop shell emits and the webview listens for,
// generated from contracts/desktop.json (the Rust shell reads the same
// names from its generated module). The env vars are Rust<->Go only and
// ride in those outputs.
${lines}
// Enum tokens of the Desktop account settings. \`as const\` is what lets the
// preference types derive (\`typeof TRAY_ON_CLOSE_TRAY | typeof
// TRAY_ON_CLOSE_QUIT\`) rather than restate the union, so a token renamed in
// the contract fails the type check instead of narrowing to a value the hub
// never sends.
${behavior}
// The window state the shell reports at launch, which \`parseLaunchVisibility\`
// narrows. That parse answers the first token for anything it does not know, so
// without the contract a renamed token would show a window on every login
// launch that asked to start in the tray, and nothing would fail.
${launch}
// The saved display state of the main window. \`WindowMode\` derives from these,
// so the union cannot drift from the token the Go config persists and the Rust
// shell matches.
${windowMode}`
}

export function emitRsDesktop(d) {
  const env = Object.keys(DESKTOP_RS_ENV_NAMES)
    .map(k => `pub const ${DESKTOP_RS_ENV_NAMES[k]}: &str = ${rustString(d.envVars[k])};`)
    .join('\n')
  const events = Object.keys(DESKTOP_RS_EVENT_NAMES)
    .map((k) => {
      const attr = DESKTOP_RS_MACOS_ONLY_EVENTS.has(k)
        ? '#[cfg_attr(not(target_os = "macos"), allow(dead_code))]\n'
        : ''
      return `${attr}pub const ${DESKTOP_RS_EVENT_NAMES[k]}: &str = ${rustString(d.tauriEvents[k])};`
    })
    .join('\n')
  const behavior = Object.entries(flattenBehavior(d.windowBehavior))
    .map(([k, v]) => `pub const ${DESKTOP_RS_BEHAVIOR_NAMES[k]}: &str = ${rustString(v)};`)
    .join('\n')
  const launch = Object.keys(DESKTOP_RS_LAUNCH_NAMES)
    .map(k => `pub const ${DESKTOP_RS_LAUNCH_NAMES[k]}: &str = ${rustString(d.launchVisibility[k])};`)
    .join('\n')
  const windowMode = Object.keys(DESKTOP_RS_WINDOW_MODE_NAMES)
    .map(k => `pub const ${DESKTOP_RS_WINDOW_MODE_NAMES[k]}: &str = ${rustString(d.windowMode[k])};`)
    .join('\n')
  return `// Code generated by scripts/generate-contracts.mjs from contracts/desktop.json. DO NOT EDIT.

// The desktop shell's cross-language vocabulary. This module is included
// from main.rs via include!; regenerate with \`task generate-contracts\`.

/// Env vars handed to the Go sidecar at spawn (the Go twin reads the same
/// names from its generated contracts package).
${env}

/// Tauri events this shell emits and the webview subscribes to.
${events}

/// Frame cap the shell enforces on the sidecar IPC wire (the Go twin is
/// contracts.MaxFrameSizeBytes).
pub const MAX_FRAME_SIZE_BYTES: u64 = ${d.maxFrameSizeBytes};

/// Vite/Bun DEV origin for the debug webview and the sidecar DevProxy
/// (the Go twin is contracts.DevFrontendURL). Must match tauri.conf.json
/// build.devUrl.
pub const DEV_FRONTEND_URL: &str = ${rustString(d.devFrontendUrl)};

/// Enum tokens of the Desktop account settings, as the webview sends them in
/// the \`set_desktop_behavior\` payload (the Go twin is the
/// contracts.TrayOnClose*/TrayOnMinimize*/StartMinimized* family).
${behavior}

/// The window state this shell reports through \`get_startup_info\`, which the
/// webview narrows in \`parseLaunchVisibility\`.
${launch}

/// The saved display state of the main window (the Go twin is the
/// contracts.WindowMode* family). The sidecar persists one of these tokens
/// verbatim, so the wire carries no second spelling of them.
${windowMode}
`
}

/** A Rust string literal (no raw strings; the values carry no quotes/backslashes). */
function rustString(str) {
  if (str.includes('"') || str.includes('\\'))
    throw new ContractError('desktop.json', `value ${JSON.stringify(str)} needs Rust escaping the emitter does not do`)
  return `"${str}"`
}

// ---------------------------------------------------------------------------
// proto descriptor cross-checks (enum-keyed domains)
// ---------------------------------------------------------------------------

/**
 * Enum value names from a buf build FileDescriptorSet, excluding nothing --
 * callers decide how UNSPECIFIED/reserved values are handled (reserved values
 * never appear in a descriptor at all).
 */
export function enumValues(descriptorSet, protoFile, enumName) {
  const file = descriptorSet.file.find(f => f.name === protoFile)
  if (!file)
    throw new ContractError(protoFile, 'not found in the buf build descriptor set')
  const en = file.enumType.find(e => e.name === enumName)
  if (!en)
    throw new ContractError(protoFile, `enum ${enumName} not found`)
  return en.value.map(v => v.name)
}

/** Runs `buf build` and returns the parsed FileDescriptorSet. */
export function bufDescriptor(root) {
  const out = `${root}/.buf-descriptor-contracts.json`
  try {
    execFileSync('buf', ['build', '-o', `${out}#format=json`], { cwd: root, stdio: 'pipe' })
    return JSON.parse(readFileSync(out, 'utf8'))
  }
  catch (err) {
    throw new ContractError('buf', `buf build failed (is buf on PATH? version pinned in versions.env): ${err.message}`)
  }
  finally {
    rmSync(out, { force: true })
  }
}

// ---------------------------------------------------------------------------
// codex bypass settings
// ---------------------------------------------------------------------------

export function checkCodexBypass(c) {
  const ids = c.settings.map(setting => setting.id)
  mustBe(new Set(ids).size === ids.length, 'codex-bypass.json', 'two settings share one option id')
  mustBe(ids.includes('permissionMode'), 'codex-bypass.json', 'permissionMode is required')
  mustBe(c.settings.some(setting => setting.planOption), 'codex-bypass.json', 'at least one plan option is required')
}

export function emitGoCodexBypass(c) {
  const indent = '\t'
  const rows = c.settings
    .filter(setting => setting.planOption)
    .map(setting => `\t${jsonString(setting.id)}: ${jsonString(setting.value)},`)
    .join('\n')
  return `${GO_HEADER('codex-bypass.json')}package contracts

// CodexPlanBypassOptions returns the provider options that accompany a
// plan-mode bypass approval. The permission mode travels in its own field.
func CodexPlanBypassOptions() map[string]string {
${indent}return map[string]string{
${rows}
${indent}}
}
`
}

export function emitTsCodexBypass(c) {
  const rows = c.settings
    .map(setting => `    ${jsonString(setting.id)}: ${jsonString(setting.value)},`)
    .join('\n')
  return `${TS_HEADER('codex-bypass.json')}
// The complete Codex settings change that disables permission prompts.
export const CODEX_BYPASS_SETTINGS = {
  sets: {
${rows}
  },
} as const
`
}

// ---------------------------------------------------------------------------
// provider protocols: a coding agent's own wire vocabulary (zcode, claude, goose, copilot, pi)
// ---------------------------------------------------------------------------

/**
 * The provider-protocol domains. Each one is a set of NAME TABLES: a map from an
 * identifier both languages spell to the literal the agent's process sends.
 *
 * They share one emitter because they pose one problem. A provider's envelope `type`
 * and payload `kind` are dispatch keys on BOTH sides -- the Go worker classifies the
 * row, the TS plugin renders it -- so the two copies must agree exactly, and a
 * one-character drift silently stops rendering rather than failing a build. The agent's
 * vendor usually owns the values, and neither language does. A domain where LeapMux owns
 * part of the vocabulary (copilot-permissions) states that in its own `preamble`, which
 * replaces the default sentence in both emitted headers -- a generated comment that
 * claims the wrong owner is worse than none.
 *
 * `goPrefix` and `tsPrefix` build the emitted identifiers, so a table needs no
 * per-constant name entry: the Go constant is `<goPrefix><Table><Key>` and the TS key
 * is the bare `Key` inside a `<TS_PREFIX>_<TABLE>` object.
 */
const PROVIDER_PROTOCOLS = [
  {
    name: 'zcode-protocol',
    goPrefix: 'ZCode',
    tsPrefix: 'ZCODE',
    title: 'ZCode',
    // goTable/tsTable name the emitted symbol per table; the key set is the contract's.
    tables: [
      { key: 'events', goTable: 'Event', tsTable: 'EVENT', tsType: 'ZCodeEvent', doc: 'session event types -- the envelope `type`' },
      { key: 'toolKinds', goTable: 'ToolKind', tsTable: 'TOOL_KIND', tsType: 'ZCodeToolKind', doc: '`tool.updated` kinds -- the tool-call lifecycle' },
      { key: 'toolNames', goTable: 'ToolName', tsTable: 'TOOL', tsType: 'ZCodeTool', doc: 'tool names both sides dispatch on' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'ZCodeMode', doc: 'session modes, carried on LeapMux\'s permission-mode axis' },
      { key: 'resultTypes', goTable: 'Result', tsTable: 'RESULT', tsType: 'ZCodeResult', doc: '`turn.completed.resultType`' },
      { key: 'decisions', goTable: 'Decision', tsTable: 'DECISION', tsType: 'ZCodeDecision', doc: '`permission.resolved.decision`' },
    ],
  },
  {
    name: 'goose-protocol',
    goPrefix: 'Goose',
    tsPrefix: 'GOOSE',
    title: 'Goose',
    tables: [
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'GooseMode', doc: 'permission modes' },
    ],
  },
  {
    name: 'claude-protocol',
    goPrefix: 'Claude',
    tsPrefix: 'CLAUDE',
    title: 'Claude Code',
    tables: [
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'ClaudeMode', doc: 'permission modes' },
    ],
  },
  {
    name: 'copilot-permissions',
    goPrefix: 'CopilotPermission',
    tsPrefix: 'COPILOT_PERMISSION',
    title: 'GitHub Copilot',
    preamble: [
      'Copilot\'s permission vocabulary. GitHub owns `allow_all` and the two values;',
      'LeapMux owns `copilot_assisted_approval`, the axis its launch flags drive. Both',
      'sides read them -- the Go worker builds the option group, the browser plugin builds',
      'the preset -- so they are generated from one file rather than hand-copied into two.',
    ].join('\n// '),
    tables: [
      { key: 'groups', goTable: 'Group', tsTable: 'GROUP', tsType: 'CopilotPermissionGroup', doc: 'permission option-group ids' },
      { key: 'values', goTable: 'Value', tsTable: 'VALUE', tsType: 'CopilotPermissionValue', doc: 'permission values, shared by both axes' },
    ],
  },
  {
    name: 'pi-protocol',
    goPrefix: 'Pi',
    tsPrefix: 'PI',
    title: 'Pi',
    tables: [
      { key: 'events', goTable: 'Event', tsTable: 'EVENT', tsType: 'PiEvent', doc: 'RPC envelope `type` values' },
      { key: 'assistantEvents', goTable: 'AssistantEvent', tsTable: 'ASSISTANT_EVENT', tsType: 'PiAssistantEvent', doc: 'assistant message-update sub-types' },
      { key: 'dialogMethods', goTable: 'DialogMethod', tsTable: 'DIALOG_METHOD', tsType: 'PiDialogMethod', doc: 'extension_ui_request methods that BLOCK on a response' },
      { key: 'extensionMethods', goTable: 'ExtensionMethod', tsTable: 'EXTENSION_METHOD', tsType: 'PiExtensionMethod', doc: 'fire-and-forget extension_ui_request methods' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'PiTool', doc: 'tool names the renderers dispatch on' },
    ],
  },
]

/**
 * A provider protocol is valid when every declared table is present and non-empty,
 * every table the FILE carries is declared (so a table added to the JSON cannot sit
 * unemitted), and no two keys inside one table share a literal -- a duplicate would
 * make two dispatch branches indistinguishable on the wire.
 */
export function checkProviderProtocol(spec, p) {
  const file = `${spec.name}.json`
  const declared = spec.tables.map(t => t.key)
  const present = Object.keys(p).filter(k => !k.startsWith('_') && typeof p[k] === 'object')
  for (const key of declared)
    mustBe(p[key] != null && Object.keys(p[key]).length > 0, file, `${key} is missing or empty`)
  for (const key of present)
    mustBe(declared.includes(key), file, `table ${key} is not declared in PROVIDER_PROTOCOLS -- a new table must be registered in the same change, or it is never emitted`)
  for (const key of declared) {
    const seen = new Map()
    for (const [name, literal] of Object.entries(p[key])) {
      mustBe(!seen.has(literal), file, `${key}.${name} repeats the literal ${JSON.stringify(literal)} already used by ${key}.${seen.get(literal)} -- two dispatch branches would be indistinguishable on the wire`)
      seen.set(literal, name)
    }
  }
  // A key-valued pointer must name a member of the table it points into, or the
  // emitted default names a value the enum does not carry.
  if (p.defaultMode != null)
    mustBe(p.modes[p.defaultMode] != null, file, `defaultMode ${JSON.stringify(p.defaultMode)} is not a key of modes`)
  return {}
}

export function emitGoProviderProtocol(spec, p) {
  const blocks = spec.tables.map((t) => {
    const decls = Object.entries(p[t.key])
      .map(([name, literal]) => ({ name: `${spec.goPrefix}${t.goTable}${name}`, value: jsonString(literal) }))
    return `// ${spec.goPrefix}${t.goTable}* are ${t.doc}.\nconst (\n${goConstBlock(decls)}\n)`
  })
  const extra = p.defaultMode == null
    ? ''
    : `\n// ${spec.goPrefix}DefaultMode is the mode a fresh session runs on.\nconst ${spec.goPrefix}DefaultMode = ${spec.goPrefix}Mode${p.defaultMode}\n`
  const preamble = spec.preamble ?? `${spec.title}'s wire vocabulary. These literals are dispatch keys on BOTH sides --
// the Go worker classifies each row, the browser plugin renders it -- so they are
// generated from one file rather than hand-copied into two. ${spec.title}'s vendor owns
// the values; LeapMux follows them.`
  return `${GO_HEADER(`${spec.name}.json`)}package contracts

// ${preamble}

${blocks.join('\n\n')}
${extra}`
}

export function emitTsProviderProtocol(spec, p) {
  const blocks = spec.tables.map((t) => {
    const rows = Object.entries(p[t.key]).map(([name, literal]) => `  ${name}: ${jsonString(literal)},`).join('\n')
    const symbol = `${spec.tsPrefix}_${t.tsTable}`
    return `/** ${spec.title} ${t.doc}. */\nexport const ${symbol} = {\n${rows}\n} as const\nexport type ${t.tsType} = typeof ${symbol}[keyof typeof ${symbol}]`
  })
  const extra = p.defaultMode == null
    ? ''
    : `\n/** The mode a fresh ${spec.title} session runs on. */\nexport const ${spec.tsPrefix}_DEFAULT_MODE = ${spec.tsPrefix}_MODE.${p.defaultMode}\n`
  const preamble = spec.preamble ?? `${spec.title}'s wire vocabulary, generated from contracts/${spec.name}.json. The Go
// provider reads the same tables, so the two can no longer drift by a character.`
  return `${TS_HEADER(`${spec.name}.json`)}
// ${preamble}

${blocks.join('\n\n')}
${extra}`
}

// ---------------------------------------------------------------------------
// emission
// ---------------------------------------------------------------------------

function GO_HEADER(file) {
  return `// Code generated by scripts/generate-contracts.mjs from contracts/${file}. DO NOT EDIT.\n`
}

function TS_HEADER(file) {
  return `// Code generated by scripts/generate-contracts.mjs from contracts/${file}. DO NOT EDIT.\n`
}

/** gofmt-stable alignment: pads names so the `=` lines up per block. */
function goConstBlock(decls) {
  const width = Math.max(...decls.map(d => d.name.length))
  return decls.map(d => `\t${d.name.padEnd(width)} = ${d.value}`).join('\n')
}

function goDurationMs(ms) {
  return `time.Duration(${ms}) * time.Millisecond`
}

/** String literals for Go and TS are both JSON-escaped: double quotes. */
function jsonString(s) {
  return JSON.stringify(s)
}

/** Codepoint sort: locale-independent, so output is deterministic everywhere. */
function byFirstString([a], [b]) {
  return a < b ? -1 : a > b ? 1 : 0
}

/** gofmt-stable alignment for map literals: pads keys so the values line up. */
function goMapBlock(rows) {
  const width = Math.max(...rows.map(r => r.key.length))
  return rows.map(r => `\t${r.key.padEnd(width)} ${r.value},`).join('\n')
}

export function emitGoWire(w, d) {
  const flat = flattenWire(w, d)
  const sizeKeys = [
    'noiseAeadTagSizeBytes',
    'maxCiphertextForChunkBytes',
    'maxPlaintextPerChunkBytes',
    'maxMessageSizeBytes',
    'innerEnvelopeHeadroomBytes',
    'maxReassembledMessageSizeBytes',
    'maxConfigurableMessageSizeBytes',
    'maxIncompleteChunked',
  ]
  const closeKeys = [
    'closeReasonTooManyConnections',
    'closeReasonSnapshotTooLarge',
    'closeReasonForbidden',
    'closeReasonControlFlood',
  ]
  return `${GO_HEADER('wire.json')}package contracts

import "time"

// Chunking and reassembly limits for the Noise-transport channel wire. Both
// ends frame, chunk, and reassemble the same encrypted messages; the
// derivations (plaintext per chunk, reassembled ceiling) are computed by the
// generator, not by each language again.
const (
${goConstBlock([
  ...sizeKeys.map(k => ({ name: WIRE_GO_NAMES[k], value: String(flat[k]) })),
])}
)

// PingMethod is the inner-RPC method token both ends must agree on.
const ${WIRE_GO_NAMES.pingMethod} = ${jsonString(flat.pingMethod)}

// ProtocolVersion is the ChannelMessage envelope version every sender stamps:
// NewChannelMessage, the senders that bypass it, and the browser's
// channelSession all read this constant.
const ${WIRE_GO_NAMES.protocolVersion} = ${flat.protocolVersion}

// Session-key rotation timing. RejectRetryAfter spaces rekey refusals;
// the hard ceiling outlives the max age by hardCeilingOverrunMs. The verify
// timeout caps the open-time Ping round trip; the idle interval spaces the
// background rekey poll.
const (
${goConstBlock([
  { name: WIRE_GO_NAMES.sessionKeyMaxAgeMs, value: goDurationMs(flat.sessionKeyMaxAgeMs) },
  { name: WIRE_GO_NAMES.sessionKeyMinRekeyIntervalMs, value: goDurationMs(flat.sessionKeyMinRekeyIntervalMs) },
  { name: WIRE_GO_NAMES.sessionKeyHardCeilingMs, value: goDurationMs(flat.sessionKeyHardCeilingMs) },
  { name: WIRE_GO_NAMES.sessionKeyRejectBackoffMs, value: goDurationMs(flat.sessionKeyRejectBackoffMs) },
  { name: WIRE_GO_NAMES.sessionKeyVerifyTimeoutMs, value: goDurationMs(flat.sessionKeyVerifyTimeoutMs) },
  { name: WIRE_GO_NAMES.sessionKeyIdleRekeyIntervalMs, value: goDurationMs(flat.sessionKeyIdleRekeyIntervalMs) },
])}
)

// WebSocket close-reason tokens. The browser BRANCHES on these (which advice
// to show), so a drift is behavioral, not cosmetic.
const (
${goConstBlock(closeKeys.map(k => ({ name: WIRE_GO_NAMES[k], value: jsonString(flat[k]) })))}
)

// WebSocket routes, their query-parameter names, and the subprotocols the hub
// accepts and every dialer (CLI, tunnel, desktop sidecar, browser) requests.
// The browser builds the /ws/userevents URL itself, so the vocabulary is a
// wire contract, not an internal spelling.
const (
${goConstBlock([
  { name: WIRE_GO_NAMES.wsRouteUserEvents, value: jsonString(flat.wsRouteUserEvents) },
  { name: WIRE_GO_NAMES.wsRouteChannel, value: jsonString(flat.wsRouteChannel) },
  { name: WIRE_GO_NAMES.wsParamWorkspaceIds, value: jsonString(flat.wsParamWorkspaceIds) },
  { name: WIRE_GO_NAMES.wsParamResumeAfterHlc, value: jsonString(flat.wsParamResumeAfterHlc) },
  { name: WIRE_GO_NAMES.wsParamResumeEpoch, value: jsonString(flat.wsParamResumeEpoch) },
  { name: WIRE_GO_NAMES.wsSubprotocolUserEventsRelay, value: jsonString(flat.wsSubprotocolUserEventsRelay) },
  { name: WIRE_GO_NAMES.wsSubprotocolChannelRelay, value: jsonString(flat.wsSubprotocolChannelRelay) },
])}
)

// SoftNonceLimit is the nonce count past which a Noise session should rekey
// (the counter is uint32; the soft limit leaves headroom under the wrap
// bound, HardNonceLimit).
const ${WIRE_GO_NAMES.softNonceLimit} = uint64(${flat.softNonceLimit})

// HardNonceLimit is the uint32 wrap bound: the last nonce value the counter
// may hold. Past it the counter would silently reuse nonce 0, so both Noise
// implementations refuse to encrypt beyond it.
const ${WIRE_GO_NAMES.hardNonceLimit} = uint64(${flat.hardNonceLimit})

// LengthPrefixBytes is the big-endian length prefix on every multiplexed
// channel and user-events WebSocket frame.
const ${WIRE_GO_NAMES.lengthPrefixBytes} = ${flat.lengthPrefixBytes}
`
}

export function emitTsWire(w, d) {
  const flat = flattenWire(w, d)
  const line = (k, extra = '') =>
    `export const ${WIRE_TS_NAMES[k]} = ${typeof flat[k] === 'number' ? flat[k] : jsonString(flat[k])} as const${extra}\n`
  return `${TS_HEADER('wire.json')}
// Chunking and reassembly limits for the Noise-transport channel wire.
// Derived values (MAX_CHUNK_SIZE, DEFAULT_MAX_REASSEMBLED_MESSAGE_SIZE,
// SESSION_KEY_HARD_CEILING_MS) are computed by the generator.
${line('maxPlaintextPerChunkBytes')}
${line('maxMessageSizeBytes')}
${line('innerEnvelopeHeadroomBytes')}
${line('maxReassembledMessageSizeBytes')}
${line('maxConfigurableMessageSizeBytes')}
${line('maxIncompleteChunked')}
${line('pingMethod')}
${line('protocolVersion')}

// Session-key rotation timing, in milliseconds.
${line('sessionKeyMaxAgeMs')}
${line('sessionKeyMinRekeyIntervalMs')}
${line('sessionKeyHardCeilingMs')}
${line('sessionKeyRejectBackoffMs')}
${line('sessionKeyVerifyTimeoutMs')}
${line('sessionKeyIdleRekeyIntervalMs')}

// WebSocket close-reason tokens. The UI branches on these (which advice to
// show), so a drift is behavioral, not cosmetic.
${line('closeReasonTooManyConnections')}
${line('closeReasonSnapshotTooLarge')}
${line('closeReasonForbidden')}
${line('closeReasonControlFlood')}

// WebSocket routes, query-parameter names, and subprotocols. The browser
// builds the /ws/userevents URL itself; the hub, CLI, tunnel, and sidecar
// spell the same vocabulary.
${line('wsRouteUserEvents')}
${line('wsRouteChannel')}
${line('wsParamWorkspaceIds')}
${line('wsParamResumeAfterHlc')}
${line('wsParamResumeEpoch')}
${line('wsSubprotocolUserEventsRelay')}
${line('wsSubprotocolChannelRelay')}

// Noise session rekey triggers (nonce count; the counter is uint32, and the
// hard limit is the wrap bound itself).
${line('softNonceLimit')}
${line('hardNonceLimit')}

// Big-endian length prefix on every multiplexed WebSocket frame.
${line('lengthPrefixBytes')}
`
}

export function emitGoHeaders(h) {
  return `${GO_HEADER('headers.json')}package contracts

// HTTP headers the hub sets and the CLI and browser read. Wire contract
// between separately-upgradable programs -- the generated constant replaces
// the hand copies without creating an import edge on any internal package.
const (
${goConstBlock(Object.keys(HEADERS_GO_NAMES).map(k => ({ name: HEADERS_GO_NAMES[k], value: jsonString(h[k]) })))}
)
`
}

export function emitTsHeaders(h) {
  const lines = Object.keys(HEADERS_TS_NAMES)
    .map(k => `export const ${HEADERS_TS_NAMES[k]} = ${jsonString(h[k])} as const\n`)
    .join('')
  return `${TS_HEADER('headers.json')}\n// HTTP headers the hub sets and the browser reads (fetch lowercases them).\n${lines}`
}

export function emitGoListen(l) {
  const sources = Object.entries(l.addressSources).map(([token, doc]) =>
    `\t// ${LISTEN_SOURCE_GO_NAMES[token]} is ${doc}.\n\t${LISTEN_SOURCE_GO_NAMES[token]} = ${jsonString(token)}`)
  const vocabulary = [
    '\t// ListenAnyHost is the canonical wildcard host: every interface, on one',
    '\t// port. The address parser renders it and the panel\'s picker stores it.',
    `\tListenAnyHost = ${jsonString(l.anyHost)}`,
    '\t// MaxExtraListenAddresses caps the stored extra address list. Every entry',
    '\t// costs a listener, a serve goroutine and a file descriptor for the life',
    '\t// of the process. A machine with more interfaces to publish on wants the',
    '\t// wildcard, which is one entry.',
    `\tMaxExtraListenAddresses = ${l.maxExtraAddresses}`,
  ].join('\n')
  return `${GO_HEADER('listen.json')}package contracts

// The listen-address vocabulary the hub and the browser both spell.
const (
${vocabulary}
)

// Why the hub serves an address, as the administration surface reports it.
const (
${sources.join('\n')}
)
`
}

export function emitTsListen(l) {
  const sources = Object.entries(l.addressSources).map(([token, doc]) =>
    `/** ${doc} */\nexport const ${LISTEN_SOURCE_TS_NAMES[token]} = ${jsonString(token)} as const`)
  return `${TS_HEADER('listen.json')}
/** The canonical wildcard host: every interface, on one port. */
export const LISTEN_ANY_HOST = ${jsonString(l.anyHost)} as const

/** How many extra listen addresses one hub may store. */
export const MAX_EXTRA_LISTEN_ADDRESSES = ${l.maxExtraAddresses}

${sources.join('\n\n')}
`
}

export function emitGoTrustedProxies(v) {
  const entries = Object.values(v.providers).map(provider =>
    `\t{Token: ${jsonString(provider.token)}, Label: ${jsonString(provider.label)}, Help: ${jsonString(provider.help)}},`).join('\n')
  return `${GO_HEADER('trusted-proxies.json')}package contracts

// TrustedProxyProvider describes one symbolic provider selector.
type TrustedProxyProvider struct {
\tToken string
\tLabel string
\tHelp  string
}

// MaxTrustedProxySelectors caps configured selectors. A provider token counts
// once, independent of the number of bundled ranges it expands to.
const MaxTrustedProxySelectors = ${v.maxSelectors}

const (
\tTrustedProxyProviderCloudflare = ${jsonString(v.providers.cloudflare.token)}
\tTrustedProxyProviderCloudFront = ${jsonString(v.providers.cloudfront.token)}
)

// TrustedProxyProviders is the built-in provider catalogue.
var TrustedProxyProviders = []TrustedProxyProvider{
${entries}
}
`
}

export function emitTsTrustedProxies(v) {
  const entries = Object.values(v.providers).map(provider =>
    `  { token: ${jsonString(provider.token)}, label: ${jsonString(provider.label)}, help: ${jsonString(provider.help)} },`).join('\n')
  return `${TS_HEADER('trusted-proxies.json')}
/** A built-in trusted reverse-proxy provider. */
export interface TrustedProxyProvider {
  token: string
  label: string
  help: string
}

/** The most configured selectors. A provider token counts once. */
export const MAX_TRUSTED_PROXY_SELECTORS = ${v.maxSelectors} as const

/** The built-in provider catalogue. */
export const TRUSTED_PROXY_PROVIDERS: readonly TrustedProxyProvider[] = [
${entries}
]
`
}

export function emitGoRetry(r) {
  const blocks = Object.entries(r.policies).map(([name, p]) => {
    const prefix = RETRY_GO_NAMES[name]
    return `// ${name}: mirrored on both sides of the events stream.
const (
${goConstBlock([
  { name: `${prefix}Initial`, value: goDurationMs(p.initialMs) },
  { name: `${prefix}MaxInterval`, value: goDurationMs(p.maxMs) },
  { name: `${prefix}Multiplier`, value: String(p.multiplier) },
  { name: `${prefix}Jitter`, value: String(p.jitterFactor) },
  { name: `${prefix}MaxAttempts`, value: String(p.maxAttempts) },
])}
)`
  })
  return `${GO_HEADER('retry.json')}package contracts

import "time"

${blocks.join('\n\n')}
`
}

export function emitTsRetry(r) {
  const blocks = Object.entries(r.policies).map(([name, p]) => {
    const constName = RETRY_TS_NAMES[name]
    return `// ${name}: mirrored on both sides of the events stream.
export const ${constName} = {
  initialMs: ${p.initialMs},
  maxMs: ${p.maxMs},
  multiplier: ${p.multiplier},
  jitterFactor: ${p.jitterFactor},
  maxAttempts: ${p.maxAttempts},
} as const
`
  })
  return `${TS_HEADER('retry.json')}\n${blocks.join('\n')}`
}

// ---------------------------------------------------------------------------
// orchestration
// ---------------------------------------------------------------------------

/** Reads the contracts; generate() requires every registered domain's file. */
export function loadContracts(contractsDir) {
  const read = name => JSON.parse(readFileSync(posix.join(contractsDir, `${name}.json`), 'utf8'))
  const has = (name) => {
    try {
      readFileSync(posix.join(contractsDir, `${name}.json`))
      return true
    }
    catch (err) {
      // ENOENT returns false and generate() turns it into a hard error; any
      // other failure (unreadable) throws here, keeping its path attached.
      if (err?.code === 'ENOENT')
        return false
      throw err
    }
  }
  return { read, has }
}

/**
 * One contracts/<name>.json domain: what it emits. Adding a domain is one
 * entry here plus its check and emit functions. `requiresDescriptor` marks
 * the enum-keyed domains whose checks read a buf build FileDescriptorSet;
 * the registry is also where main() looks, so the precondition has one home.
 */
const DOMAINS = [
  {
    name: 'wire',
    emit(out, read) {
      const w = read('wire')
      const d = checkWire(w)
      out['backend/generated/contracts/wire.go'] = emitGoWire(w, d)
      out['frontend/src/generated/contracts/wire.ts'] = emitTsWire(w, d)
    },
  },
  {
    name: 'headers',
    emit(out, read) {
      const h = read('headers')
      checkHeaders(h)
      out['backend/generated/contracts/headers.go'] = emitGoHeaders(h)
      out['frontend/src/generated/contracts/headers.ts'] = emitTsHeaders(h)
    },
  },
  {
    name: 'listen',
    emit(out, read) {
      const l = read('listen')
      checkListen(l)
      out['backend/generated/contracts/listen.go'] = emitGoListen(l)
      out['frontend/src/generated/contracts/listen.ts'] = emitTsListen(l)
    },
  },
  {
    name: 'trusted-proxies',
    emit(out, read) {
      const v = read('trusted-proxies')
      checkTrustedProxies(v)
      out['backend/generated/contracts/trusted-proxies.go'] = emitGoTrustedProxies(v)
      out['frontend/src/generated/contracts/trusted-proxies.ts'] = emitTsTrustedProxies(v)
    },
  },
  {
    name: 'retry',
    emit(out, read) {
      const r = read('retry')
      checkRetry(r)
      out['backend/generated/contracts/retry.go'] = emitGoRetry(r)
      out['frontend/src/generated/contracts/retry.ts'] = emitTsRetry(r)
    },
  },
  {
    name: 'session-info',
    emit(out, read) {
      const s = read('session-info')
      checkSessionInfo(s)
      out['backend/generated/contracts/session-info.go'] = emitGoSessionInfo(s)
      out['frontend/src/generated/contracts/session-info.ts'] = emitTsSessionInfo(s)
    },
  },
  {
    name: 'worker-vocab',
    emit(out, read) {
      const v = read('worker-vocab')
      checkWorkerVocab(v)
      out['backend/generated/contracts/worker-vocab.go'] = emitGoWorkerVocab(v)
      out['frontend/src/generated/contracts/worker-vocab.ts'] = emitTsWorkerVocab(v)
    },
  },
  {
    name: 'tab-names',
    emit(out, read) {
      const t = read('tab-names')
      checkTabNames(t)
      out['backend/generated/contracts/tab-names.go'] = emitGoTabNames(t)
      out['frontend/src/generated/contracts/tab-names.ts'] = emitTsTabNames(t)
    },
  },
  {
    name: 'captcha',
    emit(out, read) {
      const c = read('captcha')
      checkCaptcha(c)
      out['backend/generated/contracts/captcha.go'] = emitGoCaptcha(c)
      out['frontend/src/generated/contracts/captcha.ts'] = emitTsCaptcha(c)
    },
  },
  {
    name: 'desktop',
    emit(out, read) {
      const d = read('desktop')
      checkDesktop(d)
      out['backend/generated/contracts/desktop.go'] = emitGoDesktop(d)
      out['frontend/src/generated/contracts/desktop.ts'] = emitTsDesktop(d)
      out['desktop/rust/src/generated/contracts.rs'] = emitRsDesktop(d)
    },
  },
  {
    name: 'codex-bypass',
    emit(out, read) {
      const c = read('codex-bypass')
      checkCodexBypass(c)
      out['backend/generated/contracts/codex-bypass.go'] = emitGoCodexBypass(c)
      out['frontend/src/generated/contracts/codex-bypass.ts'] = emitTsCodexBypass(c)
    },
  },
  {
    name: 'providers',
    requiresDescriptor: true,
    emit(out, read, descriptorSet) {
      const agentEnumValues = enumValues(descriptorSet, 'leapmux/v1/agent.proto', 'AgentProvider')
      const p = read('providers')
      checkProviders(p, agentEnumValues)
      out['backend/generated/contracts/providers.go'] = emitGoProviders(p, agentEnumValues)
      out['frontend/src/generated/contracts/providers.ts'] = emitTsProviders(p, agentEnumValues)
    },
  },
  {
    name: 'tab-types',
    requiresDescriptor: true,
    emit(out, read, descriptorSet) {
      const tabEnumValues = enumValues(descriptorSet, 'leapmux/v1/workspace.proto', 'TabType')
      const t = read('tab-types')
      checkTabTypes(t, tabEnumValues)
      out['backend/generated/contracts/tab-types.go'] = emitGoTabTypes(t, tabEnumValues)
      out['frontend/src/generated/contracts/tab-types.ts'] = emitTsTabTypes(t, tabEnumValues)
    },
  },
  {
    name: 'scopes',
    requiresDescriptor: true,
    emit(out, read, descriptorSet) {
      const scopeEnumValues = enumValues(descriptorSet, 'leapmux/v1/scope.proto', 'Scope')
      const s = read('scopes')
      checkScopes(s, scopeEnumValues)
      out['backend/generated/contracts/scopes.go'] = emitGoScopes(s, scopeEnumValues)
      out['frontend/src/generated/contracts/scopes.ts'] = emitTsScopes(s)
    },
  },
  {
    name: 'theme-default',
    emit(out, read) {
      const t = read('theme-default')
      checkTheme(t)
      out['backend/generated/contracts/theme.go'] = emitGoTheme(t)
      out['frontend/src/generated/contracts/theme-default.ts'] = emitTsTheme(t)
    },
  },
  {
    name: 'validate',
    emit(out, read) {
      const v = read('validate')
      checkValidate(v)
      out['backend/generated/contracts/validate.go'] = emitGoValidate(v)
      out['frontend/src/generated/contracts/validate.ts'] = emitTsValidate(v)
    },
  },
  ...PROVIDER_PROTOCOLS.map(spec => ({
    name: spec.name,
    emit(out, read) {
      const p = read(spec.name)
      checkProviderProtocol(spec, p)
      out[`backend/generated/contracts/${spec.name}.go`] = emitGoProviderProtocol(spec, p)
      out[`frontend/src/generated/contracts/${spec.name}.ts`] = emitTsProviderProtocol(spec, p)
    },
  })),
]

/**
 * Runs every present domain end to end. Returns a map of output path (repo
 * relative) -> file content. Pure: writes nothing. `descriptorSet` (a buf
 * build FileDescriptorSet) is required by the enum-keyed domains for their
 * cross-checks.
 */
export function generate(contractsDir, descriptorSet = null) {
  const { read, has } = loadContracts(contractsDir)
  const out = {}
  for (const domain of DOMAINS) {
    mustBe(has(domain.name), `${domain.name}.json`, 'is missing -- every registered domain ships its contract; retire a domain by removing its DOMAINS entry in the same change that deletes the file')
    if (domain.requiresDescriptor) {
      mustBe(descriptorSet != null, `${domain.name}.json`, 'requires a buf descriptor (run via task generate-contracts)')
    }
    domain.emit(out, read, descriptorSet)
  }
  return out
}

if (import.meta.main) {
  const arg = (name) => {
    const i = argv.indexOf(name)
    return i !== -1 ? argv[i + 1] : undefined
  }
  const staging = arg('--staging')
  if (!staging) {
    console.error('generate-contracts: --staging <dir> is required (sync-generated.mjs passes it)')
    exit(2)
  }
  const root = arg('--root') ?? '.'
  const contractsDir = posix.join(root, 'contracts')

  const { failures } = validateSchemalessDir(contractsDir)
  if (failures.length > 0) {
    for (const line of formatFailureLines(failures, 'contracts/'))
      console.error(`generate-contracts: ${line}`)
    exit(1)
  }

  let files
  try {
    const { has } = loadContracts(contractsDir)
    // The enum-keyed domains cross-check their keys against the proto enums;
    // buf build is local-only (no remote plugins), same precondition as
    // generate-proto.
    const descriptorSet = DOMAINS.some(d => d.requiresDescriptor && has(d.name))
      ? bufDescriptor(root)
      : null
    files = generate(contractsDir, descriptorSet)
  }
  catch (err) {
    if (err instanceof ContractError) {
      console.error(`generate-contracts: ${err.message}`)
      exit(1)
    }
    throw err
  }

  for (const [rel, content] of Object.entries(files)) {
    const abs = posix.join(staging, rel)
    mkdirSync(posix.dirname(abs), { recursive: true })
    writeFileSync(abs, content)
  }
  console.log(`generate-contracts: ${Object.keys(files).length} files staged from ${contractsDir}`)
}
