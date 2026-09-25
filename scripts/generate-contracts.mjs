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
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
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
// user-settings: the account-setting vocabulary the hub and the browser share
// ---------------------------------------------------------------------------

/** The Go identifier for one setting name: quakeSizePercent -> QuakeSizePercent. */
const settingPascal = name => name[0].toUpperCase() + name.slice(1)

/** The TS identifier for one setting name: quakeSizePercent -> QUAKE_SIZE_PERCENT. */
const settingScreaming = name => name.replace(/([a-z0-9])([A-Z])/g, '$1_$2').toUpperCase()

/**
 * The semantic rules the JSON Schema cannot state, because each one needs a
 * sibling value or the sibling contract.
 *
 * A `desktopEnum` is checked against desktop.json rather than against a list
 * here, and that asymmetry is the point: a THIRD language spells those tokens
 * (the Rust shell matches them out of the set_desktop_behavior payload), so
 * they live in desktop.json and this file states only which block a setting
 * draws from. A copy here would be the second source this contract removes.
 */
export function checkUserSettings(u, desktop) {
  const protoKeys = new Set()
  for (const [name, s] of Object.entries(u.settings)) {
    const where = `settings.${name}`
    mustBe(!protoKeys.has(s.protoKey), 'user-settings.json', `${where}: two settings share the proto key ${s.protoKey}`)
    protoKeys.add(s.protoKey)
    if (s.kind === 'enum') {
      mustBe(s.values.includes(s.default), 'user-settings.json', `${where}: the default ${jsonString(s.default)} is not one of its values`)
    }
    else if (s.kind === 'int' || s.kind === 'float') {
      mustBe(s.min < s.max, 'user-settings.json', `${where}: min must be below max`)
      mustBe(s.default >= s.min && s.default <= s.max, 'user-settings.json', `${where}: the default ${s.default} is outside its limits`)
      if (s.kind === 'int') {
        for (const [field, value] of [['default', s.default], ['min', s.min], ['max', s.max]])
          mustBe(Number.isSafeInteger(value), 'user-settings.json', `${where}: ${field} must be a safe integer`)
      }
    }
    else if (s.kind === 'desktopEnum') {
      const block = desktop.windowBehavior[s.desktopSetting]
      mustBe(block !== undefined, 'user-settings.json', `${where}: desktop.json windowBehavior has no ${s.desktopSetting} block`)
      mustBe(Object.values(block).includes(s.default), 'user-settings.json', `${where}: the default ${jsonString(s.default)} is not a windowBehavior token`)
    }
  }
  return {}
}

/** The settings of one or more kinds, in file order, for the emitters to walk. */
function settingsOfKind(u, ...kinds) {
  return Object.entries(u.settings).filter(([, s]) => kinds.includes(s.kind))
}

/** Every setting that carries a scalar default, with the literal Go writes for it. */
function settingDefaults(u) {
  return Object.entries(u.settings)
    .filter(([, s]) => s.kind !== 'opaque')
    .map(([name, s]) => ({
      name: `Setting${settingPascal(name)}Default`,
      value: typeof s.default === 'string' ? jsonString(s.default) : String(s.default),
    }))
}

export function emitGoUserSettings(u) {
  const keyDecls = Object.entries(u.settings)
    .map(([name, s]) => ({ name: `SettingKey${settingPascal(name)}`, value: jsonString(s.protoKey) }))
  const bounds = settingsOfKind(u, 'int', 'float').flatMap(([name, s]) => [
    { name: `Setting${settingPascal(name)}Min`, value: String(s.min) },
    { name: `Setting${settingPascal(name)}Max`, value: String(s.max) },
  ])
  const enums = settingsOfKind(u, 'enum')
    .map(([name, s]) => `// Setting${settingPascal(name)}Values is the whole vocabulary of ${s.protoKey}.\nvar Setting${settingPascal(name)}Values = []string{${s.values.map(jsonString).join(', ')}}`)
    .join('\n\n')
  return `${GO_HEADER('user-settings.json')}package contracts

// The proto key of every account setting. The hub identifies its descriptor with
// one of these, and the browser addresses that descriptor by the same string, so a
// rename reaches both sides at once.
const (
${goConstBlock(keyDecls)}
)

// The DEFAULT of every setting whose value is a scalar. The hub answers an
// unset key with it, and the browser falls back to it when no value arrives at
// all -- so a disagreement makes one client show a setting the others do not.
// UNTYPED, so each caller binds it to the type its own key declares.
const (
${goConstBlock(settingDefaults(u))}
)

// The inclusive limits of every numeric setting. The hub refuses a value
// outside them, and the browser discards one.
const (
${goConstBlock(bounds)}
)

${enums}
`
}

export function emitTsUserSettings(u) {
  const keys = Object.entries(u.settings)
    .map(([name, s]) => `export const SETTING_KEY_${settingScreaming(name)} = ${jsonString(s.protoKey)} as const`)
    .join('\n')
  const rest = Object.entries(u.settings).flatMap(([name, s]) => {
    if (s.kind === 'opaque')
      return []
    const id = settingScreaming(name)
    const lines = []
    if (s.kind === 'enum') {
      lines.push(`export const SETTING_${id}_VALUES = [${s.values.map(jsonString).join(', ')}] as const`)
      lines.push(`export type Setting${settingPascal(name)} = typeof SETTING_${id}_VALUES[number]`)
      lines.push(`export const SETTING_${id}_DEFAULT: Setting${settingPascal(name)} = ${jsonString(s.default)}`)
    }
    else if (s.kind === 'int' || s.kind === 'float') {
      lines.push(`export const SETTING_${id}_DEFAULT = ${s.default} as const`)
      lines.push(`export const SETTING_${id}_MIN = ${s.min} as const`)
      lines.push(`export const SETTING_${id}_MAX = ${s.max} as const`)
    }
    else {
      lines.push(`export const SETTING_${id}_DEFAULT = ${typeof s.default === 'string' ? jsonString(s.default) : s.default} as const`)
    }
    return [lines.join('\n')]
  }).join('\n\n')
  return `${TS_HEADER('user-settings.json')}
/**
 * The proto key of every account setting. The browser addresses a descriptor by
 * the same string the hub identifies it with.
 */
${keys}

${rest}
`
}

// ---------------------------------------------------------------------------
// chat-history: cross-language page and catch-up limits
// ---------------------------------------------------------------------------

export function checkChatHistory(v) {
  // Safe integers only: the TS emitter writes the raw value into a bigint
  // literal, and an unsafe value such as 1e21 stringifies as `1e+21n`, which
  // is not a valid TypeScript literal.
  mustBe(Number.isSafeInteger(v.messagePageLimit) && v.messagePageLimit > 0, 'chat-history.json', 'messagePageLimit must be a positive safe integer')
  mustBe(Number.isSafeInteger(v.catchUpGapLimit) && v.catchUpGapLimit >= v.messagePageLimit, 'chat-history.json', 'catchUpGapLimit must be a safe integer >= messagePageLimit')
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
  { json: 'rateLimitUpdateFields', goPrefix: 'RateLimitUpdateField', ts: 'RATE_LIMIT_UPDATE_FIELD', tsType: 'RateLimitUpdateField', what: 'fields of the rate_limits update envelope' },
  { json: 'rateLimitUpdateModes', goPrefix: 'RateLimitUpdateMode', ts: 'RATE_LIMIT_UPDATE_MODE', tsType: 'RateLimitUpdateMode', what: 'rate_limits update operations' },
  { json: 'runningToolFields', goPrefix: 'RunningToolField', ts: 'RUNNING_TOOL_FIELD', tsType: 'RunningToolField', what: 'fields of the running_tool object' },
  { json: 'runningToolRetryFields', goPrefix: 'RunningToolRetryField', ts: 'RUNNING_TOOL_RETRY_FIELD', tsType: 'RunningToolRetryField', what: 'fields of running_tool.retry' },
  { json: 'goalProgressFields', goPrefix: 'GoalProgressField', ts: 'GOAL_PROGRESS_FIELD', tsType: 'GoalProgressField', what: 'fields of the goal_progress object' },
]

export function checkSessionInfo(v) {
  // Biject the JSON's own tables with SESSION_INFO_TABLES. Without this, a table
  // added to session-info.json and to its schema emits no Go and no TS, and says
  // nothing: the emitters below iterate the descriptor list alone, so the first
  // report is an undefined-constant build failure that never identifies the contract.
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
  // providers/claude/output.go decodes the same field through a struct tag, which must be a
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

function assembledMessageEntries(v) {
  return [
    ...Object.entries(v.fields).map(([key, token]) => [`Field${key}`, token]),
    ['Type', v.types.Assembled],
    ...Object.entries(v.kinds).map(([key, token]) => [`Kind${key}`, token]),
    ...Object.entries(v.completions).map(([key, token]) => [`Completion${key}`, token]),
  ]
}

function toolOutcomeEntries(v) {
  return [
    ...Object.entries(v.fields).map(([key, token]) => [`Field${key}`, token]),
    ...Object.entries(v.sources).map(([key, token]) => [`Source${key}`, token]),
    ...Object.entries(v.outcomes).map(([key, token]) => [`Outcome${key}`, token]),
  ]
}

export function checkWorkerVocab(v) {
  const entries = Object.entries(v.notificationTypes)
  const tokens = entries.map(([, token]) => token)
  mustBe(new Set(tokens).size === tokens.length, 'worker-vocab.json', 'two notification types share one wire token')
  mustBe(!tokens.includes(v.notificationThreadWrapperType), 'worker-vocab.json', `notificationThreadWrapperType ${JSON.stringify(v.notificationThreadWrapperType)} collides with a notificationTypes token -- the browser's thread probe routes on that exact value, so a colliding envelope would be misrouted`)
  for (const key of v.workerWrittenNotificationTypes) {
    mustBe(v.notificationTypes[key] != null, 'worker-vocab.json', `workerWrittenNotificationTypes specifies ${key}, which is not a notificationTypes key`)
  }
  mustBe(v.modelSentinels.accountDefaultModel !== v.modelSentinels.effortAuto, 'worker-vocab.json', 'the model sentinels must be distinct values')
  // These tokens are the goal_updated PAYLOAD vocabulary, not the storage
  // format: agents.goal_status stores an AgentGoalStatus ordinal, and its CHECK
  // lists no token. A duplicate here still makes two statuses read back as one
  // on the wire.
  const statusTokens = Object.values(v.goalStatusTokens)
  mustBe(new Set(statusTokens).size === statusTokens.length, 'worker-vocab.json', 'two goal statuses share one wire token')
  mustBe(v.goalStatusTokens.None === '', 'worker-vocab.json', 'goalStatusTokens.None must be the empty token -- the goal_updated payload carries "" for "no goal", and every reader tests for it')
  const transitions = Object.values(v.goalTransitions)
  mustBe(new Set(transitions).size === transitions.length, 'worker-vocab.json', 'two goal transitions share one wire token')
  const metadataFields = Object.values(v.messageMetadataFields)
  mustBe(new Set(metadataFields).size === metadataFields.length, 'worker-vocab.json', 'two message metadata fields share one wire token')
  const notificationFields = Object.values(v.notificationFields)
  mustBe(new Set(notificationFields).size === notificationFields.length, 'worker-vocab.json', 'two notification fields share one wire token')
  const supplementFields = Object.values(v.messageSupplementFields)
  mustBe(new Set(supplementFields).size === supplementFields.length, 'worker-vocab.json', 'two message supplement fields share one wire token')
  for (const [group, values] of Object.entries(v.assembledMessage ?? {})) {
    const tokens = Object.values(values)
    mustBe(tokens.length > 0, 'worker-vocab.json', `assembledMessage.${group} must hold at least one entry`)
    mustBe(new Set(tokens).size === tokens.length, 'worker-vocab.json', `two assembled-message ${group} entries share one wire token`)
  }
  for (const [group, values] of Object.entries(v.toolOutcome ?? {})) {
    const tokens = Object.values(values)
    mustBe(tokens.length > 0, 'worker-vocab.json', `toolOutcome.${group} must hold at least one entry`)
    mustBe(new Set(tokens).size === tokens.length, 'worker-vocab.json', `two tool-outcome ${group} entries share one wire token`)
  }
  // The outcome note is a metadata FIELD, so its key must exist there. Without it the
  // worker would write a note under a name the browser never reads.
  mustBe(v.messageMetadataFields.ToolOutcome != null, 'worker-vocab.json', 'messageMetadataFields must hold ToolOutcome, the field the tool-outcome note is stored under')
  return {}
}

export function emitGoWorkerVocab(v) {
  const notif = goConstBlock(Object.entries(v.notificationTypes)
    .map(([key, token]) => ({ name: `NotificationType${key}`, value: jsonString(token) })))
  const goalStatusBlock = goConstBlock(Object.entries(v.goalStatusTokens)
    .map(([key, token]) => ({ name: `GoalStatusToken${key}`, value: jsonString(token) })))
  const goalTransitionBlock = goConstBlock(Object.entries(v.goalTransitions)
    .map(([key, token]) => ({ name: `GoalTransition${key}`, value: jsonString(token) })))
  const assembledMessageBlock = goConstBlock(assembledMessageEntries(v.assembledMessage)
    .map(([key, token]) => ({ name: `AssembledMessage${key}`, value: jsonString(token) })))
  const toolOutcomeBlock = goConstBlock(toolOutcomeEntries(v.toolOutcome)
    .map(([key, token]) => ({ name: `ToolOutcome${key}`, value: jsonString(token) })))
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

// RPCMethod* identifies worker methods that share a generated wire token.
const (
${goConstBlock(Object.entries(v.rpcMethods).map(([key, token]) => ({ name: `RPCMethod${key}`, value: jsonString(token) })))}
)

// MessageMetadataField* identifies fields calculated by the worker for display.
const (
${goConstBlock(Object.entries(v.messageMetadataFields).map(([key, token]) => ({ name: `MessageMetadataField${key}`, value: jsonString(token) })))}
)

// MessageSupplementField* separates provider data from worker metadata in storage.
const (
${goConstBlock(Object.entries(v.messageSupplementFields).map(([key, token]) => ({ name: `MessageSupplementField${key}`, value: jsonString(token) })))}
)

// NotificationField* are payload keys the worker writes and the browser reads back.
const (
${goConstBlock(Object.entries(v.notificationFields).map(([key, token]) => ({ name: `NotificationField${key}`, value: jsonString(token) })))}
)

// NotificationThreadWrapperType is the wrapper discriminator the worker's
// wrapNotifContent stamps on every notification-thread row.
const NotificationThreadWrapperType = ${jsonString(v.notificationThreadWrapperType)}

// CodexRateLimitReachedTimeWindow is the one Codex rateLimitReachedType that
// lifts on the rolling-window timer (the others are billing/usage caps).
const CodexRateLimitReachedTimeWindow = ${jsonString(v.codexRateLimitReachedTimeWindow)}

// CodexRateLimitAccountBlockKey is the stable session-info member that carries
// a billing or workspace block independently from the rolling windows.
const CodexRateLimitAccountBlockKey = ${jsonString(v.codexRateLimitAccountBlockKey)}

// GoalStatusToken* are the tokens the goal_updated notification payload
// carries. They are NOT the storage format: agents.goal_status stores an
// AgentGoalStatus ordinal, and agent.GoalStatusWire maps one onto the other.
const (
${goalStatusBlock}
)

// GoalTransition* name what a goal change DID. The applier holds the row from
// before the write, so it is the only place that can tell a resume from a fresh
// set -- both end with the status "active".
const (
${goalTransitionBlock}
)

// AssembledMessage* are the fields and values of Worker-assembled text rows.
const (
${assembledMessageBlock}
)

// ToolOutcome* are the fields and values of the note the worker attaches to a tool
// row whose own result the agent never sent. The note is LeapMux's CALCULATION, so
// it lives in message metadata and never in the provider's own bytes.
const (
${toolOutcomeBlock}
)

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
  const written = v.workerWrittenNotificationTypes
    .map(key => `  ${jsonString(v.notificationTypes[key])},`)
    .join('\n')
  const goalStatusEntries = Object.entries(v.goalStatusTokens)
    .map(([key, token]) => `  ${key}: ${jsonString(token)},`)
    .join('\n')
  const goalTransitionEntries = Object.entries(v.goalTransitions)
    .map(([key, token]) => `  ${key}: ${jsonString(token)},`)
    .join('\n')
  const assembledEntries = assembledMessageEntries(v.assembledMessage)
    .map(([key, token]) => `  ${key}: ${jsonString(token)},`)
    .join('\n')
  const toolOutcomeTsEntries = toolOutcomeEntries(v.toolOutcome)
    .map(([key, token]) => `  ${key}: ${jsonString(token)},`)
    .join('\n')
  return `${TS_HEADER('worker-vocab.json')}
// The worker's wire vocabulary, generated from contracts/worker-vocab.json
// (the Go agent package's NotificationType* constants and friends read the
// same tables).

/** Notification envelope "type" tokens. */
export const NOTIFICATION_TYPE = {
${notif}
} as const

/** Worker methods with generated wire tokens. */
export const WORKER_RPC_METHOD = {
${Object.entries(v.rpcMethods).map(([key, token]) => `  ${key}: ${jsonString(token)},`).join('\n')}
} as const

/** Fields calculated by the worker for display. */
export const MESSAGE_METADATA_FIELD = {
${Object.entries(v.messageMetadataFields).map(([key, token]) => `  ${key}: ${jsonString(token)},`).join('\n')}
} as const

/** Separate provider data from worker metadata in storage. */
export const MESSAGE_SUPPLEMENT_FIELD = {
${Object.entries(v.messageSupplementFields).map(([key, token]) => `  ${key}: ${jsonString(token)},`).join('\n')}
} as const

/** Payload keys the worker writes onto a notification and the browser reads back. */
export const NOTIFICATION_FIELD = {
${Object.entries(v.notificationFields).map(([key, token]) => `  ${key}: ${jsonString(token)},`).join('\n')}
} as const

export type NotificationType = typeof NOTIFICATION_TYPE[keyof typeof NOTIFICATION_TYPE]

/** The types the WORKER is the sole writer of (standalone rows, no plugin). */
export const WORKER_WRITTEN_NOTIFICATION_TYPES = [
${written}
] as const

/** Wrapper discriminator on every notification-thread row (wrapNotifContent). */
export const NOTIFICATION_THREAD_TYPE = ${jsonString(v.notificationThreadWrapperType)} as const

/** The one Codex rateLimitReachedType that lifts on the rolling-window timer. */
export const CODEX_RATE_LIMIT_REACHED_TIME_WINDOW = ${jsonString(v.codexRateLimitReachedTimeWindow)} as const

/** Stable session-info member for a Codex billing or workspace block. */
export const CODEX_RATE_LIMIT_ACCOUNT_BLOCK_KEY = ${jsonString(v.codexRateLimitAccountBlockKey)} as const

/**
 * The tokens the worker ships in the goal_updated payload. The empty token
 * means "no goal". The agents row stores an ordinal, not one of these.
 */
export const GOAL_STATUS_TOKEN = {
${goalStatusEntries}
} as const

export type GoalStatusToken = typeof GOAL_STATUS_TOKEN[keyof typeof GOAL_STATUS_TOKEN]

/** What a goal change DID, decided by the worker from the row before the write. */
export const GOAL_TRANSITION = {
${goalTransitionEntries}
} as const

export type GoalTransitionToken = typeof GOAL_TRANSITION[keyof typeof GOAL_TRANSITION]

/** Fields and values of Worker-assembled text rows. */
export const ASSEMBLED_MESSAGE = {
${assembledEntries}
} as const

/**
 * Fields and values of the note the worker attaches to a tool row whose own result
 * the agent never sent. The note is LeapMux's CALCULATION, so it arrives in message
 * metadata and never inside the provider's own bytes.
 */
export const TOOL_OUTCOME = {
${toolOutcomeTsEntries}
} as const

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
  { name: 'GoalObjectiveByteLimit', value: String(v.goal.objectiveByteLimit) },
  { name: 'GoalStatusDetailByteLimit', value: String(v.goal.statusDetailByteLimit) },
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
// re-derives it. The companion comment above each class lists its ranges.

export const NAME_BYTE_LIMIT = ${v.name.byteLimit} as const
export const SESSION_ID_BYTE_LIMIT = ${v.session.byteLimit} as const
export const SESSION_FILE_PATH_BYTE_LIMIT = ${v.session.filePathByteLimit} as const
export const BRANCH_NAME_BYTE_LIMIT = ${v.branch.byteLimit} as const
export const GOAL_OBJECTIVE_BYTE_LIMIT = ${v.goal.objectiveByteLimit} as const
export const GOAL_STATUS_DETAIL_BYTE_LIMIT = ${v.goal.statusDetailByteLimit} as const
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
  agentHelper: 'EnvAgentHelper',
}

export const DESKTOP_RS_ENV_NAMES = {
  devEndpoint: 'ENV_DEV_ENDPOINT',
  binaryHash: 'ENV_BINARY_HASH',
  devFrontend: 'ENV_DEV_FRONTEND',
  agentHelper: 'ENV_AGENT_HELPER',
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
    'DESKTOP_RS_MACOS_ONLY_EVENTS lists a key missing from DESKTOP_RS_EVENT_NAMES',
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
// shell sets or removes when spawning the Go sidecar (the sidecar reads them
// in main.go and worker.RunAgentHelper), and the frame cap both programs
// enforce on the sidecar IPC wire. The Tauri event names are Rust<->webview
// only and ride in the Rust/TS outputs.
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
  mustBe(ids.some(id => id !== 'permissionMode'), 'codex-bypass.json', 'at least one additional option is required')
}

export function emitGoCodexBypass(c) {
  const indent = '\t'
  const rows = c.settings
    .map(setting => `\t${jsonString(setting.id)}: ${jsonString(setting.value)},`)
    .join('\n')
  return `${GO_HEADER('codex-bypass.json')}package contracts

// CodexBypassOptions returns the complete preset that disables permission prompts.
func CodexBypassOptions() map[string]string {
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
// provider protocols: the wire vocabulary of each coding agent, one domain per protocol
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
 * part of the vocabulary (copilot-protocol) states that in its own `preamble`, which
 * replaces the default sentence in both emitted headers -- a generated comment that
 * claims the wrong owner is worse than none.
 *
 * `goPrefix` and `tsPrefix` build the emitted identifiers, so a table needs no
 * per-constant name entry: the Go constant is `<goPrefix><Table><Key>` and the TS key
 * is the bare `Key` inside a `<TS_PREFIX>_<TABLE>` object.
 *
 * `frameKind` marks a table whose literals identify the kind of a frame: an event, a
 * method, a notification, an update, or the type of a line, an item or a request.
 * `'name'` states that each literal is a whole kind, and `'prefix'` states that each
 * literal starts a family of kinds. A frame kind is the key that a reader dispatches
 * on, so shared browser code that spells one decides by the provider.
 * `emitTsProviderFrameKinds` collects every marked literal, and the `no-provider-decision`
 * rule in `frontend/eslint/chatPipelinePlugin.ts` rejects each one in shared chat code,
 * which is every file under `components/chat/` outside `providers/`. A field name or a
 * status word is not a frame kind, so its table stays unmarked.
 */
export const PROVIDER_PROTOCOLS = [
  {
    name: 'acp-protocol',
    goPrefix: 'ACP',
    tsPrefix: 'ACP',
    title: 'Agent Client Protocol',
    preamble: [
      'The Agent Client Protocol defines these session updates and message roles. LeapMux owns',
      'the SET of keys in the three supplement tables, not their spellings: a tool row keeps the',
      'fields the protocol delivered LATE, or that no protocol frame carries at all, beside the',
      'agent\'s original bytes. `protocol` and `terminals` are the two names LeapMux chose, and',
      '`rawOutput` is the protocol\'s own tool-call field, which the worker reuses as a key. The',
      'worker writes that envelope and the browser plugin reads it back, so every key in it is a',
      'dispatch key on both sides. Every provider that speaks ACP shares the tables.',
    ].join('\n// '),
    tables: [
      { key: 'updates', frameKind: 'name', goTable: 'Update', tsTable: 'UPDATE', tsType: 'ACPUpdate', doc: 'session update identifiers' },
      { key: 'roles', goTable: 'Role', tsTable: 'ROLE', tsType: 'ACPRole', doc: 'message `role` values' },
      { key: 'toolKinds', goTable: 'ToolKind', tsTable: 'TOOL_KIND', tsType: 'ACPToolKindWord', readers: ['ts'], readersWhy: 'the worker stores a tool frame whole and never branches on its kind; the browser picks the renderer from it', doc: 'tool-call `kind` words, the behavioural set the protocol groups every tool into' },
      { key: 'supplementIdentity', goTable: 'SupplementIdentity', tsTable: 'SUPPLEMENT_IDENTITY', tsType: 'ACPSupplementIdentityField', goSlice: true, owner: 'LeapMux checks these', doc: 'protocol fields before a supplement can reach a row' },
      { key: 'supplementRequest', goTable: 'SupplementRequest', tsTable: 'SUPPLEMENT_REQUEST', tsType: 'ACPSupplementRequestField', goSlice: true, doc: 'request fields a later tool_call_update can revise' },
      // No blanket `owner`: LeapMux chose `protocol` and `terminals`, and `rawOutput` is
      // the protocol's own tool-call field, which providers/acp/base.go reads off the wire. The
      // doc therefore carries the split, because one owner word cannot.
      { key: 'supplement', goTable: 'Supplement', tsTable: 'SUPPLEMENT', tsType: 'ACPSupplementField', doc: 'payloads a tool row keeps beside a frame -- LeapMux chose `protocol` and `terminals`, and `rawOutput` is the protocol\'s own field' },
      { key: 'terminalResult', goTable: 'TerminalResult', tsTable: 'TERMINAL_RESULT', tsType: 'ACPTerminalResultField', doc: 'fields of one terminal\'s stored output, inside the `terminals` payload' },
      { key: 'contentBlock', goTable: 'ContentBlock', tsTable: 'CONTENT_BLOCK', tsType: 'ACPContentBlockField', doc: 'fields of a tool call\'s own content array, which lists the terminals it refers to' },
      { key: 'blockTypes', goTable: 'BlockType', tsTable: 'BLOCK_TYPE', tsType: 'ACPBlockType', doc: 'content block `type` values both sides dispatch on' },
      { key: 'permissionOutcomes', goTable: 'PermissionOutcome', tsTable: 'PERMISSION_OUTCOME', tsType: 'ACPPermissionOutcome', doc: 'the `outcome` of a reply to session/request_permission' },
    ],
  },
  {
    name: 'kimi-protocol',
    goPrefix: 'Kimi',
    tsPrefix: 'KIMI',
    title: 'Kimi Code',
    preamble: [
      'Moonshot AI owns the kap-server event, origin, tool, display, decision and mode names.',
      'LeapMux owns the `plan` value of the permission-mode axis and the two marks in `reply`',
      '(`dismiss` and `permission_mode`). Both sides read them -- the Go worker dispatches',
      'the events and answers the approvals and questions, the browser plugin classifies the',
      'same persisted payloads and writes the answers.',
    ].join('\n// '),
    tables: [
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'KimiEvent', doc: 'WebSocket event types -- the payload `type`' },
      { key: 'origins', goTable: 'Origin', tsTable: 'ORIGIN', tsType: 'KimiOrigin', doc: '`turn.started.origin.kind` values, which say who started a turn' },
      { key: 'tools', goTable: 'Tool', tsTable: 'TOOL', tsType: 'KimiTool', doc: 'tool names both sides dispatch on' },
      { key: 'displayKinds', goTable: 'Display', tsTable: 'DISPLAY', tsType: 'KimiDisplayKind', doc: '`display.kind` values of a tool call or an approval' },
      { key: 'decisions', goTable: 'Decision', tsTable: 'DECISION', tsType: 'KimiDecision', doc: 'approval decisions' },
      { key: 'approvalScopes', goTable: 'ApprovalScope', tsTable: 'APPROVAL_SCOPE', tsType: 'KimiApprovalScope', doc: 'how long one approval lasts' },
      { key: 'planLabels', goTable: 'PlanLabel', tsTable: 'PLAN_LABEL', tsType: 'KimiPlanLabel', doc: 'the `selected_label` values that refuse a plan' },
      { key: 'answerKinds', goTable: 'AnswerKind', tsTable: 'ANSWER_KIND', tsType: 'KimiAnswerKind', doc: 'the kinds of one question answer' },
      { key: 'goalModes', goTable: 'GoalMode', tsTable: 'GOAL_MODE', tsType: 'KimiGoalMode', doc: 'the `selected_label` values that approve a goal start in a permission mode' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'KimiMode', doc: 'permission modes on LeapMux\'s permission-mode axis, `plan` included' },
      { key: 'reply', goTable: 'Reply', tsTable: 'REPLY', tsType: 'KimiReplyField', doc: 'fields of a stored approval or question answer' },
      { key: 'todoStatuses', goTable: 'TodoStatus', tsTable: 'TODO_STATUS', tsType: 'KimiTodoStatus', doc: '`TodoList` item statuses' },
      { key: 'turnEndReasons', goTable: 'TurnEnd', tsTable: 'TURN_END', tsType: 'KimiTurnEndReason', doc: '`turn.ended.reason` words' },
    ],
  },
  {
    name: 'zcode-protocol',
    goPrefix: 'ZCode',
    tsPrefix: 'ZCODE',
    title: 'ZCode',
    // goTable/tsTable name the emitted symbol per table; the key set is the contract's.
    tables: [
      { key: 'methods', frameKind: 'name', goTable: 'Method', tsTable: 'METHOD', tsType: 'ZCodeMethod', doc: 'interaction request methods' },
      { key: 'actions', goTable: 'Action', tsTable: 'ACTION', tsType: 'ZCodeAction', doc: 'native input response actions' },
      { key: 'replyFields', goTable: 'ReplyField', tsTable: 'REPLY_FIELD', tsType: 'ZCodeReplyField', doc: 'native input response fields' },
      { key: 'answerFields', goTable: 'AnswerField', tsTable: 'ANSWER_FIELD', tsType: 'ZCodeAnswerField', doc: 'native answer fields' },
      { key: 'planControls', goTable: 'PlanControl', tsTable: 'PLAN_CONTROL', tsType: 'ZCodePlanControl', doc: 'native plan approval values' },
      { key: 'interactions', frameKind: 'name', goTable: 'Interaction', tsTable: 'INTERACTION', tsType: 'ZCodeInteraction', doc: 'control interaction types' },
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'ZCodeEvent', doc: 'session event types -- the envelope `type`' },
      { key: 'toolPrefixes', goTable: 'ToolPrefix', tsTable: 'TOOL_PREFIX', tsType: 'ZCodeToolPrefix', doc: 'prefixes for projected tool-call IDs' },
      { key: 'toolKinds', goTable: 'ToolKind', tsTable: 'TOOL_KIND', tsType: 'ZCodeToolKind', doc: '`tool.updated` kinds -- the tool-call lifecycle' },
      { key: 'toolNames', goTable: 'ToolName', tsTable: 'TOOL', tsType: 'ZCodeTool', doc: 'tool names both sides dispatch on' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'ZCodeMode', doc: 'session modes, carried on LeapMux\'s permission-mode axis' },
      { key: 'resultTypes', goTable: 'Result', tsTable: 'RESULT', tsType: 'ZCodeResult', readers: ['ts'], readersWhy: 'the worker carries resultType through as an opaque string (providers/zcode/output.go) and compares none of them; the browser words the turn-end row from it', doc: '`turn.completed.resultType`' },
      { key: 'decisions', goTable: 'Decision', tsTable: 'DECISION', tsType: 'ZCodeDecision', doc: '`permission.resolved.decision`' },
      { key: 'supplement', goTable: 'Supplement', tsTable: 'SUPPLEMENT', tsType: 'ZCodeSupplementField', doc: 'keys of the envelope a retained tool row keeps -- the last two are LeapMux\'s own payload names' },
      { key: 'supplementPayload', goTable: 'SupplementPayload', tsTable: 'SUPPLEMENT_PAYLOAD', tsType: 'ZCodeSupplementPayloadField', doc: 'fields of that envelope\'s payload, which identifies the call it belongs to' },
      { key: 'storedTool', goTable: 'StoredTool', tsTable: 'STORED_TOOL', tsType: 'ZCodeStoredToolField', doc: 'fields of the tool record ZCode\'s own store holds' },
      { key: 'storedPart', goTable: 'StoredPart', tsTable: 'STORED_PART', tsType: 'ZCodeStoredPartField', doc: 'fields of that record\'s `data` part' },
      { key: 'storedPartTypes', goTable: 'StoredPartType', tsTable: 'STORED_PART_TYPE', tsType: 'ZCodeStoredPartType', doc: 'the `type` a stored part must state to be a tool call' },
      { key: 'storedPartStatuses', goTable: 'StoredPartStatus', tsTable: 'STORED_PART_STATUS', tsType: 'ZCodeStoredPartStatus', doc: 'the two `state.status` words a finished part states' },
      // This table covers THREE records, so the doc must be true of all of them.
      // `Attachments` is a field of `storedPart.state`, `ArtifactURI` is a field of the
      // nested `metadata`, and the rest are the attachment record's own. Splitting it
      // would rename ZCODE_STORED_ATTACHMENT.* at every browser call site, which buys
      // nothing the wording does not.
      { key: 'storedAttachment', goTable: 'StoredAttachment', tsTable: 'STORED_ATTACHMENT', tsType: 'ZCodeStoredAttachmentField', doc: 'keys of one stored attachment at all three levels: the list on the part state, the record itself, and its nested metadata' },
      { key: 'storedAttachmentTypes', goTable: 'StoredAttachmentType', tsTable: 'STORED_ATTACHMENT_TYPE', tsType: 'ZCodeStoredAttachmentType', doc: 'the `type` an attachment must state to carry a file' },
    ],
  },
  {
    name: 'goose-protocol',
    goPrefix: 'Goose',
    tsPrefix: 'GOOSE',
    title: 'Goose',
    tables: [
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'GooseMode', doc: 'permission modes' },
      { key: 'subagent', goTable: 'Subagent', tsTable: 'SUBAGENT', tsType: 'GooseSubagent', doc: 'the extension and tool a subagent spawn rides' },
      { key: 'subagentRequest', goTable: 'SubagentRequest', tsTable: 'SUBAGENT_REQUEST', tsType: 'GooseSubagentRequestField', doc: 'fields of the subagent tool request, which rides inside logging metadata' },
      { key: 'configIds', goTable: 'Config', tsTable: 'CONFIG', tsType: 'GooseConfigId', doc: 'config-option ids of the axes both sides address by id' },
    ],
  },
  {
    name: 'mimo-protocol',
    goPrefix: 'MiMo',
    tsPrefix: 'MIMO',
    title: 'MiMo Code',
    preamble: [
      'MiMo owns the event, part, tool, operation, status and agent words. LeapMux owns the',
      'permission policies, their option id and the control-payload field, because MiMo has no such enumeration.',
    ].join('\n// '),
    tables: [
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'MiMoEvent', doc: 'event types the worker persists verbatim and the browser reads' },
      { key: 'statusTypes', goTable: 'StatusType', tsTable: 'STATUS_TYPE', tsType: 'MiMoStatusType', doc: '`status.type` words of `session.status`' },
      { key: 'partTypes', goTable: 'PartType', tsTable: 'PART_TYPE', tsType: 'MiMoPartType', doc: 'message part types the browser draws' },
      { key: 'toolStatuses', goTable: 'ToolStatus', tsTable: 'TOOL_STATUS', tsType: 'MiMoToolStatus', doc: '`state.status` words of a tool part' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'MiMoTool', doc: 'tool ids' },
      { key: 'actorActions', goTable: 'ActorAction', tsTable: 'ACTOR_ACTION', tsType: 'MiMoActorAction', doc: 'operations of the `actor` tool' },
      { key: 'actorStatuses', goTable: 'ActorStatus', tsTable: 'ACTOR_STATUS', tsType: 'MiMoActorStatus', doc: 'the status of a subagent (an actor)' },
      { key: 'actorOutcomes', goTable: 'ActorOutcome', tsTable: 'ACTOR_OUTCOME', tsType: 'MiMoActorOutcome', doc: 'the outcome of a subagent\'s last turn, `lastOutcome`' },
      { key: 'taskActions', goTable: 'TaskAction', tsTable: 'TASK_ACTION', tsType: 'MiMoTaskAction', doc: 'operations of the `task` tool' },
      { key: 'taskStatuses', goTable: 'TaskStatus', tsTable: 'TASK_STATUS', tsType: 'MiMoTaskStatus', doc: 'statuses of one work item' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'MiMoMode', doc: 'primary agents, carried on LeapMux\'s permission-mode axis' },
      { key: 'options', goTable: 'Option', tsTable: 'OPTION', tsType: 'MiMoOption', owner: 'LeapMux chose these', doc: 'option-group ids for the MiMo axes both sides address' },
      { key: 'permissionPolicies', goTable: 'PermissionPolicy', tsTable: 'PERMISSION_POLICY', tsType: 'MiMoPermissionPolicy', owner: 'LeapMux chose these', doc: 'permission policies, each a pair of MiMo runtime switches' },
      { key: 'permissionReplies', goTable: 'PermissionReply', tsTable: 'PERMISSION_REPLY', tsType: 'MiMoPermissionReply', doc: 'answers of a permission reply' },
      { key: 'controlFields', goTable: 'ControlField', tsTable: 'CONTROL_FIELD', tsType: 'MiMoControlField', owner: 'LeapMux chose these', doc: 'fields for a stored control payload' },
    ],
  },
  {
    name: 'opencode-protocol',
    goPrefix: 'OpenCode',
    tsPrefix: 'OPENCODE',
    title: 'OpenCode, Kilo and MiMo Code',
    preamble: 'The OpenCode family states a question on the daemon\'s own event stream, which its Agent Client Protocol adapter does not forward. MiMo Code sends the same question shapes on its native event stream.',
    tables: [
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'OpenCodeEvent', doc: 'question lifecycle events on the daemon event stream' },
      { key: 'answerFields', goTable: 'AnswerField', tsTable: 'ANSWER_FIELD', tsType: 'OpenCodeAnswerField', goTagPin: 'backend/internal/worker/agent/providers/opencode/questions_contract_test.go', doc: 'fields of the answer envelope the browser writes and the worker reads' },
    ],
  },
  {
    name: 'codex-protocol',
    goPrefix: 'Codex',
    tsPrefix: 'CODEX',
    title: 'Codex',
    preamble: [
      'Codex streams a command\'s output as a run of `outputDelta` events and fills',
      '`aggregatedOutput` only on the COMPLETED item, so a turn that ends first leaves an item',
      'with no output at all. LeapMux joins the deltas and stores the join beside the frame.',
      'Both sides run the same resolve -- the worker for its semantic extractors, the browser',
      'for the row it draws -- so a drift put a retained command row on screen with its output',
      'missing and nothing to say so. OpenAI owns the item field names; LeapMux owns the',
      'supplement keys.',
    ].join('\n// '),
    tables: [
      { key: 'supplement', goTable: 'Supplement', tsTable: 'SUPPLEMENT', tsType: 'CodexSupplementField', owner: 'LeapMux chose these', doc: 'keys of the envelope that carries a Codex call\'s joined output' },
      { key: 'item', goTable: 'Item', tsTable: 'ITEM_FIELD', tsType: 'CodexItemField', doc: 'fields of the item frame the join lands on' },
      { key: 'collabItem', goTable: 'CollabItem', tsTable: 'COLLAB_ITEM', tsType: 'CodexCollabItemField', goTagPin: 'backend/internal/worker/agent/providers/codex/supplement_tags_test.go', doc: 'fields of a collab tool call that list the subagents it created' },
      { key: 'itemTypes', frameKind: 'name', goTable: 'ItemType', tsTable: 'ITEM', tsType: 'CodexItemType', doc: '`item.type` discriminators both sides dispatch on' },
      { key: 'methods', frameKind: 'name', goTable: 'Method', tsTable: 'METHOD', tsType: 'CodexMethod', doc: 'JSON-RPC method names both sides dispatch on' },
      { key: 'options', goTable: 'Option', tsTable: 'OPTION', tsType: 'CodexOption', owner: 'LeapMux chose these', doc: 'option-group ids for the Codex axes both sides address' },
      { key: 'optionDefaults', goTable: 'OptionDefault', tsTable: 'OPTION_DEFAULT', tsType: 'CodexOptionDefault', defaultsFor: 'options', doc: 'the value each of those axes takes when the agent row stores none' },
    ],
  },
  {
    name: 'claude-protocol',
    goPrefix: 'Claude',
    tsPrefix: 'CLAUDE',
    title: 'Claude Code',
    tables: [
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'ClaudeMode', doc: 'permission modes' },
      // `status` is also a `system` subtype that both sides read, but it stays out: the
      // shared `isFinalCompactingStatus` in frontend/src/components/chat/messageUtils.ts
      // reads that shape for the Claude, Codex and ACP plugins.
      { key: 'systemSubtypes', frameKind: 'name', goTable: 'SystemSubtype', tsTable: 'SYSTEM_SUBTYPE', tsType: 'ClaudeSystemSubtype', doc: '`subtype` values of a `system` line that states an API retry or a compaction boundary' },
    ],
  },
  {
    name: 'copilot-protocol',
    goPrefix: 'Copilot',
    tsPrefix: 'COPILOT',
    title: 'GitHub Copilot',
    preamble: [
      'GitHub owns the native method, event, tool, mode and permission names. LeapMux owns the',
      'session-mode option-group id. Both sides read them --',
      'the Go worker dispatches the native events and builds the option groups, the browser',
      'plugin classifies the same rows and builds the presets.',
    ].join('\n// '),
    tables: [
      { key: 'methods', frameKind: 'name', goTable: 'Method', tsTable: 'METHOD', tsType: 'CopilotMethod', doc: 'JSON-RPC methods both sides dispatch on' },
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'CopilotEvent', doc: 'native event types' },
      // `goSlice`, because the MEMBERSHIP crosses the boundary. The browser hides all
      // six families; the worker filtered on two hand-listed ones and PERSISTED the
      // other four, so a session that used canvas, Fusion or a factory run wrote one
      // message row per experiment event that the browser then always hid.
      { key: 'eventPrefixes', frameKind: 'prefix', goTable: 'EventPrefix', tsTable: 'EVENT_PREFIX', tsType: 'CopilotEventPrefix', goSlice: true, doc: 'prefixes that identify a whole event family' },
      { key: 'tools', goTable: 'Tool', tsTable: 'TOOL', tsType: 'CopilotTool', doc: 'native tool names' },
      { key: 'options', goTable: 'Option', tsTable: 'OPTION', tsType: 'CopilotOption', doc: 'LeapMux option-group ids for Copilot axes' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'CopilotMode', doc: 'native session modes' },
      { key: 'permissionModes', goTable: 'PermissionMode', tsTable: 'PERMISSION_MODE', tsType: 'CopilotPermissionMode', doc: 'native permission modes' },
      { key: 'approvalScopes', goTable: 'ApprovalScope', tsTable: 'APPROVAL_SCOPE', tsType: 'CopilotApprovalScope', doc: 'how long one approved permission lasts' },
      { key: 'decisions', goTable: 'Decision', tsTable: 'DECISION', tsType: 'CopilotDecision', doc: 'the words a permission answer carries to the runtime' },
      { key: 'permissionOutcomes', goTable: 'PermissionOutcome', tsTable: 'PERMISSION_OUTCOME', tsType: 'CopilotPermissionOutcome', readers: ['ts'], readersWhy: 'the worker persists permission.completed whole; the browser words the refusal, which is the only place a self-refused permission is ever stated', doc: 'how one permission ended, which `permission.completed` states' },
      { key: 'permissionDecisionSources', goTable: 'PermissionDecisionSource', tsTable: 'PERMISSION_DECISION_SOURCE', tsType: 'CopilotPermissionDecisionSource', readers: ['ts'], readersWhy: 'the worker persists the completion whole; the browser is what must tell an approval a PERSON gave from one a judge, a policy or a replayed record produced, because all of them carry the same result', doc: 'who decided one permission, which `permission.completed` states beside the outcome' },
    ],
  },
  {
    name: 'cursor-protocol',
    goPrefix: 'Cursor',
    tsPrefix: 'CURSOR',
    title: 'Cursor',
    tables: [
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'CursorTool', doc: 'ACP tool identifiers' },
      { key: 'methods', frameKind: 'name', goTable: 'Method', tsTable: 'METHOD', tsType: 'CursorMethod', doc: 'JSON-RPC methods both sides dispatch on' },
      { key: 'supplement', goTable: 'Supplement', tsTable: 'SUPPLEMENT', tsType: 'CursorSupplementField', owner: 'LeapMux chose these', doc: 'supplemental content fields on a Cursor tool row' },
      { key: 'storedTool', goTable: 'StoredTool', tsTable: 'STORED_TOOL', tsType: 'CursorStoredToolField', owner: 'LeapMux chose these', doc: 'fields of the record the worker builds from a Cursor transcript under the shared ACP `rawOutput` key' },
      { key: 'storedBlock', goTable: 'StoredBlock', tsTable: 'STORED_BLOCK', tsType: 'CursorStoredBlockField', doc: 'fields of one block in Cursor\'s own transcript' },
      { key: 'extensionFrame', goTable: 'ExtensionFrame', tsTable: 'EXTENSION_FRAME', tsType: 'CursorExtensionFrameField', owner: 'LeapMux chose these', doc: 'fields of the frame stored under the `cursorExtension` key' },
      { key: 'blockTypes', goTable: 'BlockType', tsTable: 'BLOCK_TYPE', tsType: 'CursorBlockType', doc: 'the `type` values that identify a call and its result' },
    ],
  },
  {
    name: 'pi-protocol',
    goPrefix: 'Pi',
    tsPrefix: 'PI',
    title: 'Pi',
    tables: [
      { key: 'mcpApprovalChoices', goTable: 'MCPApprovalChoice', tsTable: 'MCP_APPROVAL_CHOICE', tsType: 'PiMcpApprovalChoice', doc: 'MCP approval response values' },
      { key: 'mcpApprovalText', goTable: 'MCPApprovalText', tsTable: 'MCP_APPROVAL_TEXT', tsType: 'PiMcpApprovalText', doc: 'MCP approval dialog delimiters' },
      { key: 'planDialogs', goTable: 'PlanDialog', tsTable: 'PLAN_DIALOG', tsType: 'PiPlanDialog', readers: ['ts'], readersWhy: 'the browser detects the plan-approval dialog by title; the worker answers only the FRESH-implementation dialog, whose two titles stay hand-written in providers/pi/protocol.go because no browser code reads them', doc: 'plan approval dialog titles' },
      { key: 'planActions', goTable: 'PlanAction', tsTable: 'PLAN_ACTION', tsType: 'PiPlanAction', doc: 'plan approval response values' },
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'PiEvent', doc: 'RPC envelope `type` values' },
      { key: 'assistantEvents', frameKind: 'name', goTable: 'AssistantEvent', tsTable: 'ASSISTANT_EVENT', tsType: 'PiAssistantEvent', readers: ['go'], readersWhy: 'the worker JOINS a run of these deltas into one assembled-message row, so no delta ever reaches the browser and no browser code spells one', doc: 'assistant message-update sub-types' },
      { key: 'customTypes', frameKind: 'name', goTable: 'CustomType', tsTable: 'CUSTOM_TYPE', tsType: 'PiCustomType', doc: 'custom message types from Pi extensions' },
      { key: 'dialogMethods', frameKind: 'name', goTable: 'DialogMethod', tsTable: 'DIALOG_METHOD', tsType: 'PiDialogMethod', doc: 'extension_ui_request methods that BLOCK on a response' },
      { key: 'extensionMethods', frameKind: 'name', goTable: 'ExtensionMethod', tsTable: 'EXTENSION_METHOD', tsType: 'PiExtensionMethod', doc: 'fire-and-forget extension_ui_request methods' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'PiTool', doc: 'tool names the renderers dispatch on' },
      { key: 'supplement', goTable: 'Supplement', tsTable: 'SUPPLEMENT', tsType: 'PiSupplementField', owner: 'LeapMux chose these', doc: 'keys of the envelope a retained or truncated Pi call keeps beside its frame' },
      { key: 'artifact', goTable: 'Artifact', tsTable: 'ARTIFACT', tsType: 'PiArtifactField', owner: 'LeapMux chose these', doc: 'fields of one stored artifact inside that envelope' },
      { key: 'resultFields', goTable: 'ResultField', tsTable: 'RESULT_FIELD', tsType: 'PiResultField', doc: 'fields of a tool-execution frame, and of the result inside it, that both sides read to place an artifact' },
      { key: 'contentBlock', goTable: 'ContentBlock', tsTable: 'CONTENT_BLOCK', tsType: 'PiContentBlockField', doc: 'fields of the content block a recovered output is written into' },
      { key: 'blockTypes', goTable: 'BlockType', tsTable: 'BLOCK_TYPE', tsType: 'PiBlockType', doc: 'content block `type` values both sides dispatch on' },
    ],
  },
  {
    name: 'mcp-elicitation',
    goPrefix: 'MCPElicitation',
    tsPrefix: 'MCP_ELICITATION',
    title: 'MCP elicitation',
    tables: [
      { key: 'methods', goTable: 'Method', tsTable: 'METHOD', tsType: 'McpElicitationMethod', doc: 'request methods' },
      { key: 'subtypes', goTable: 'Subtype', tsTable: 'SUBTYPE', tsType: 'McpElicitationSubtype', doc: 'control request subtypes' },
      { key: 'actions', goTable: 'Action', tsTable: 'ACTION', tsType: 'McpElicitationAction', doc: 'response actions' },
      { key: 'approvalKinds', goTable: 'ApprovalKind', tsTable: 'APPROVAL_KIND', tsType: 'McpElicitationApprovalKind', doc: 'approval request kinds' },
      { key: 'approvalScopes', goTable: 'ApprovalScope', tsTable: 'APPROVAL_SCOPE', tsType: 'McpElicitationApprovalScope', doc: 'approval duration values' },
    ],
  },
  {
    name: 'grok-protocol',
    goPrefix: 'Grok',
    tsPrefix: 'GROK',
    title: 'Grok Build',
    preamble: [
      'xAI owns the extension methods, notifications, tool, mode and reply words of Grok Build.',
      'LeapMux owns the option id of the approval mode, which Grok never reports back. Both sides',
      'read them -- the worker publishes the extension requests, rewrites the answers into Grok\'s',
      'replies and dispatches the notifications; the browser plugin draws the same requests and',
      'reads the same turn-end notification into a divider.',
    ].join('\n// '),
    tables: [
      { key: 'methods', frameKind: 'name', goTable: 'Method', tsTable: 'METHOD', tsType: 'GrokMethod', doc: 'extension methods both sides dispatch on, each with its leading underscore' },
      { key: 'notifications', frameKind: 'name', goTable: 'Notification', tsTable: 'NOTIFICATION', tsType: 'GrokNotification', doc: '`sessionUpdate` words of a session notification both sides read' },
      { key: 'meta', goTable: 'Meta', tsTable: 'META', tsType: 'GrokMetaKey', doc: '`_meta` keys of a tool call both sides read' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'GrokTool', doc: 'tool names both sides dispatch on' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'GrokMode', doc: 'session modes, carried on LeapMux\'s permission-mode axis' },
      { key: 'options', goTable: 'Option', tsTable: 'OPTION', tsType: 'GrokOption', owner: 'LeapMux chose these', doc: 'option-group ids of the axes Grok does not report' },
      { key: 'approvalModes', goTable: 'ApprovalMode', tsTable: 'APPROVAL_MODE', tsType: 'GrokApprovalMode', doc: 'approval modes of the approval-mode option' },
      { key: 'planOutcomes', goTable: 'PlanOutcome', tsTable: 'PLAN_OUTCOME', tsType: 'GrokPlanOutcome', doc: 'the outcomes a plan-approval reply states' },
      { key: 'trustOutcomes', goTable: 'TrustOutcome', tsTable: 'TRUST_OUTCOME', tsType: 'GrokTrustOutcome', doc: 'the outcomes a folder-trust reply states' },
      { key: 'questionOutcomes', goTable: 'QuestionOutcome', tsTable: 'QUESTION_OUTCOME', tsType: 'GrokQuestionOutcome', doc: 'the outcomes a question reply states' },
      { key: 'replyFields', goTable: 'ReplyField', tsTable: 'REPLY_FIELD', tsType: 'GrokReplyField', doc: 'fields of the replies both sides build or read' },
      { key: 'configIds', goTable: 'Config', tsTable: 'CONFIG', tsType: 'GrokConfigId', doc: 'config-option ids of the axes both sides address by id' },
    ],
  },
  {
    name: 'kiro-protocol',
    goPrefix: 'Kiro',
    tsPrefix: 'KIRO',
    title: 'Kiro CLI',
    preamble: [
      'Amazon Web Services owns the extension methods, `_meta` keys, kinds, tool titles, modes,',
      'permission options, consent scopes and question actions of Kiro CLI. LeapMux owns the',
      'option id of the policy preset, which Kiro never reports back, and the ids of the two',
      'consent-scoped permission options. Both sides read them. The worker publishes the',
      'extension requests and rewrites the answers into Kiro\'s replies. The browser plugin',
      'draws the same requests and reads the same turn end into a divider.',
    ].join('\n// '),
    tables: [
      { key: 'methods', frameKind: 'name', goTable: 'Method', tsTable: 'METHOD', tsType: 'KiroMethod', doc: 'agent-to-client extension requests both sides dispatch on' },
      { key: 'meta', goTable: 'Meta', tsTable: 'META', tsType: 'KiroMetaKey', doc: '`_meta` namespace and the keys under it both sides read' },
      { key: 'metaKinds', frameKind: 'name', goTable: 'Kind', tsTable: 'KIND', tsType: 'KiroKind', doc: '`_meta.kiro.kind` values both sides dispatch on: a subagent spawn, and the end of a turn' },
      { key: 'toolTitles', goTable: 'ToolTitle', tsTable: 'TOOL_TITLE', tsType: 'KiroToolTitle', doc: 'tool-call titles both sides dispatch on, because Kiro states no tool name' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'KiroMode', doc: 'session modes, carried on LeapMux\'s permission-mode axis' },
      { key: 'options', goTable: 'Option', tsTable: 'OPTION', tsType: 'KiroOption', owner: 'LeapMux chose these', doc: 'option-group ids of the axes Kiro does not report' },
      { key: 'policyPresets', goTable: 'PolicyPreset', tsTable: 'POLICY_PRESET', tsType: 'KiroPolicyPreset', doc: 'policy preset ids both sides spell' },
      { key: 'permissionOptions', goTable: 'PermissionOption', tsTable: 'PERMISSION_OPTION', tsType: 'KiroPermissionOption', doc: 'option ids of a permission request both sides read' },
      { key: 'scopedPermissionOptions', goTable: 'ScopedPermissionOption', tsTable: 'SCOPED_PERMISSION_OPTION', tsType: 'KiroScopedPermissionOption', owner: 'LeapMux chose these', doc: 'permission option ids that the browser states for an always-allow or an always-deny at a wider consent scope' },
      { key: 'consentScopes', goTable: 'ConsentScope', tsTable: 'CONSENT_SCOPE', tsType: 'KiroConsentScope', doc: 'consent scopes of an always-allow or an always-deny reply beyond the default session scope' },
      { key: 'userInputActions', goTable: 'UserInputAction', tsTable: 'USER_INPUT_ACTION', tsType: 'KiroUserInputAction', doc: 'question reply actions both sides spell' },
      { key: 'configIds', goTable: 'Config', tsTable: 'CONFIG', tsType: 'KiroConfigId', doc: 'config-option ids of the axes both sides address by id' },
    ],
  },
  {
    name: 'qwen-protocol',
    goPrefix: 'Qwen',
    tsPrefix: 'QWEN',
    title: 'Qwen Code',
    preamble: [
      'Qwen owns the extension method, the `_meta` keys, and the tool, mode and permission-option',
      'names below. Both sides read them -- the worker routes the subagent updates, resolves the',
      'plan approval and ends the turns Qwen starts; the browser plugin draws the same tool calls',
      'and dialogs and reads the same turn end.',
    ].join('\n// '),
    tables: [
      { key: 'methods', frameKind: 'name', goTable: 'Method', tsTable: 'METHOD', tsType: 'QwenMethod', doc: 'extension methods both sides dispatch on' },
      { key: 'meta', goTable: 'Meta', tsTable: 'META', tsType: 'QwenMetaKey', doc: '`_meta` keys of an update or a tool call both sides read' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'QwenTool', doc: 'tool names both sides dispatch on' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'QwenMode', doc: 'approval modes, carried on LeapMux\'s permission-mode axis' },
      { key: 'permissionOptions', goTable: 'PermissionOption', tsTable: 'PERMISSION_OPTION', tsType: 'QwenPermissionOption', doc: 'option ids of the permission requests both sides answer' },
      { key: 'configIds', goTable: 'Config', tsTable: 'CONFIG', tsType: 'QwenConfigId', doc: 'config-option ids of the axes both sides address by id' },
    ],
  },
  {
    name: 'reasonix-protocol',
    goPrefix: 'Reasonix',
    tsPrefix: 'REASONIX',
    title: 'Reasonix',
    preamble: [
      'Reasonix owns the tool, capability, mode and approval names, and the field names of',
      'the tool record it writes into its own transcript. LeapMux owns the envelope key that',
      'wraps that record. Both sides read them -- the worker matches the stored record against',
      'the protocol result, the browser plugin reads the same record back out of the supplement.',
    ].join('\n// '),
    tables: [
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'ReasonixMode', doc: 'session modes' },
      { key: 'configIds', goTable: 'Config', tsTable: 'CONFIG', tsType: 'ReasonixConfig', doc: 'config option identifiers' },
      { key: 'approvalValues', goTable: 'Approval', tsTable: 'APPROVAL', tsType: 'ReasonixApproval', readers: ['ts'], readersWhy: 'the worker forwards the tool_approval option value without reading it; the browser spells Yolo alone, to build the bypass preset, and Reasonix labels the three choices itself on the option group it sends', doc: 'tool approval values' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'ReasonixTool', doc: 'native tool names' },
      { key: 'capabilityActions', goTable: 'CapabilityAction', tsTable: 'CAPABILITY_ACTION', tsType: 'ReasonixCapabilityAction', doc: 'capability actions' },
      { key: 'capabilityPrefixes', goTable: 'CapabilityPrefix', tsTable: 'CAPABILITY_PREFIX', tsType: 'ReasonixCapabilityPrefix', doc: 'capability identifier prefixes' },
      { key: 'toolRecord', goTable: 'ToolRecord', tsTable: 'TOOL_RECORD', tsType: 'ReasonixToolRecordKey', doc: 'the supplement envelope key and the stored tool record\'s own fields' },
    ],
  },
  {
    name: 'codewhale-protocol',
    goPrefix: 'Codewhale',
    tsPrefix: 'CODEWHALE',
    title: 'Codewhale',
    preamble: [
      'Codewhale owns the event names, the item kinds, the turn statuses, the modes, the',
      'postures, the tool names and the runtime field names. LeapMux owns the option-group id,',
      'the control payload key, the reply frame names, the decline answer and the two LeapMux',
      'keys of a reply frame. Both sides read them. The worker dispatches the runtime\'s events',
      'and writes the control and reply frames. The browser plugin classifies the same rows and',
      'reads the saved answers back.',
    ].join('\n// '),
    tables: [
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'CodewhaleEvent', doc: 'server-sent event names that the worker persists or publishes' },
      { key: 'itemKinds', frameKind: 'name', goTable: 'ItemKind', tsTable: 'ITEM_KIND', tsType: 'CodewhaleItemKind', doc: 'turn item `kind` values' },
      { key: 'turnStatuses', goTable: 'TurnStatus', tsTable: 'TURN_STATUS', tsType: 'CodewhaleTurnStatus', doc: 'final `status` words of a turn record' },
      { key: 'modes', goTable: 'Mode', tsTable: 'MODE', tsType: 'CodewhaleMode', doc: 'thread modes, carried on the LeapMux mode axis' },
      { key: 'postures', goTable: 'Posture', tsTable: 'POSTURE', tsType: 'CodewhalePosture', doc: 'thread permission postures, carried on the LeapMux permission-mode axis' },
      { key: 'options', goTable: 'Option', tsTable: 'OPTION', tsType: 'CodewhaleOption', owner: 'LeapMux chose these', doc: 'option-group ids for the Codewhale axes both sides address' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'CodewhaleTool', doc: 'tool names both sides dispatch on' },
      { key: 'agentActions', goTable: 'AgentAction', tsTable: 'AGENT_ACTION', tsType: 'CodewhaleAgentAction', doc: '`action` values of the `agent` tool that both sides read' },
      { key: 'agentInputFields', goTable: 'AgentInputField', tsTable: 'AGENT_INPUT_FIELD', tsType: 'CodewhaleAgentInputField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of the input of an `agent` call' },
      { key: 'envelopeFields', goTable: 'EnvelopeField', tsTable: 'ENVELOPE_FIELD', tsType: 'CodewhaleEnvelopeField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of one event envelope that both sides read' },
      { key: 'itemFields', goTable: 'ItemField', tsTable: 'ITEM_FIELD', tsType: 'CodewhaleItemField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of an item event payload and of the turn item under `payload.item`' },
      { key: 'toolFields', goTable: 'ToolField', tsTable: 'TOOL_FIELD', tsType: 'CodewhaleToolField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of the parsed call under `payload.tool` of an `item.started`' },
      { key: 'itemMetadata', goTable: 'ItemMetadata', tsTable: 'ITEM_METADATA', tsType: 'CodewhaleItemMetadataField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of an item `metadata` object that identify its tool call' },
      { key: 'resultFields', goTable: 'ResultField', tsTable: 'RESULT_FIELD', tsType: 'CodewhaleResultField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of the tool results the worker acts on: the `todo_write` checklist, the `agent` receipt and wait, and the `workflow` run summary' },
      { key: 'workflowStatuses', goTable: 'WorkflowStatus', tsTable: 'WORKFLOW_STATUS', tsType: 'CodewhaleWorkflowStatus', doc: '`status` words of a `workflow` run summary' },
      { key: 'turnFields', goTable: 'TurnField', tsTable: 'TURN_FIELD', tsType: 'CodewhaleTurnField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of the turn record that `turn.completed` carries' },
      { key: 'approvalFields', goTable: 'ApprovalField', tsTable: 'APPROVAL_FIELD', tsType: 'CodewhaleApprovalField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of an `approval.*` event payload' },
      { key: 'userInputFields', goTable: 'UserInputField', tsTable: 'USER_INPUT_FIELD', tsType: 'CodewhaleUserInputField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of a `user_input.*` event payload' },
      { key: 'questionFields', goTable: 'QuestionField', tsTable: 'QUESTION_FIELD', tsType: 'CodewhaleQuestionField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of the questions of a `user_input` request and of their options' },
      { key: 'controlPayload', goTable: 'ControlPayload', tsTable: 'CONTROL_PAYLOAD', tsType: 'CodewhaleControlPayloadField', owner: 'LeapMux chose these', doc: 'keys a stored control request carries beside the shared `request` header' },
      { key: 'replyFrames', frameKind: 'name', goTable: 'ReplyFrame', tsTable: 'REPLY_FRAME', tsType: 'CodewhaleReplyFrame', owner: 'LeapMux chose these', doc: '`frame` values of a reply the worker posts to the runtime' },
      { key: 'replyFields', goTable: 'ReplyField', tsTable: 'REPLY_FIELD', tsType: 'CodewhaleReplyField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of a reply frame -- `frame` and `declined` are LeapMux\'s, and the rest are the runtime\'s own body fields' },
      { key: 'decisions', goTable: 'Decision', tsTable: 'DECISION', tsType: 'CodewhaleDecision', doc: 'approval decisions' },
      { key: 'answerFields', goTable: 'AnswerField', tsTable: 'ANSWER_FIELD', tsType: 'CodewhaleAnswerField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of one question answer' },
      { key: 'answerLabels', goTable: 'AnswerLabel', tsTable: 'ANSWER_LABEL', tsType: 'CodewhaleAnswerLabel', doc: '`label` of a free-text answer' },
      { key: 'answerTexts', goTable: 'AnswerText', tsTable: 'ANSWER_TEXT', tsType: 'CodewhaleAnswerText', owner: 'LeapMux chose these', doc: 'free-text answers the worker sends for the reader' },
      { key: 'transcriptKinds', frameKind: 'name', goTable: 'TranscriptKind', tsTable: 'TRANSCRIPT_KIND', tsType: 'CodewhaleTranscriptKind', doc: '`kind` values of one record of a subagent transcript file' },
      { key: 'transcriptFields', goTable: 'TranscriptField', tsTable: 'TRANSCRIPT_FIELD', tsType: 'CodewhaleTranscriptField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of one subagent transcript record and of its message' },
      { key: 'transcriptRoles', goTable: 'TranscriptRole', tsTable: 'TRANSCRIPT_ROLE', tsType: 'CodewhaleTranscriptRole', doc: '`role` values of a subagent transcript message' },
      { key: 'blockTypes', goTable: 'BlockType', tsTable: 'BLOCK_TYPE', tsType: 'CodewhaleBlockType', doc: 'content block `type` values of a subagent transcript message' },
      { key: 'blockFields', goTable: 'BlockField', tsTable: 'BLOCK_FIELD', tsType: 'CodewhaleBlockField', goTagPin: 'backend/internal/worker/agent/providers/codewhale/contract_tags_test.go', doc: 'fields of one content block of a subagent transcript message' },
    ],
  },
  {
    name: 'ohmypi-protocol',
    goPrefix: 'OhMyPi',
    tsPrefix: 'OH_MY_PI',
    title: 'Oh My Pi',
    preamble: [
      'omp owns the event, role, stop-reason, dialog, tool, to-do status and question words.',
      'LeapMux owns the question-bridge envelope and the incomplete-tool supplement: the worker',
      'publishes one question request for a whole `ask` call and answers omp\'s dialog chain from',
      'the browser\'s one answer, and it keeps the partial result of a call that its turn outlived.',
    ].join('\n// '),
    tables: [
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'OhMyPiEvent', doc: 'RPC frame `type` values' },
      { key: 'assistantEvents', frameKind: 'name', goTable: 'AssistantEvent', tsTable: 'ASSISTANT_EVENT', tsType: 'OhMyPiAssistantEvent', readers: ['go'], readersWhy: 'the worker JOINS a run of these deltas into one assembled-message row, so no delta ever reaches the browser and no browser code spells one', doc: 'assistant message-update sub-types' },
      { key: 'messageRoles', goTable: 'Role', tsTable: 'ROLE', tsType: 'OhMyPiRole', doc: 'message `role` values' },
      { key: 'stopReasons', goTable: 'StopReason', tsTable: 'STOP_REASON', tsType: 'OhMyPiStopReason', doc: 'assistant message `stopReason` values' },
      { key: 'dialogMethods', frameKind: 'name', goTable: 'DialogMethod', tsTable: 'DIALOG_METHOD', tsType: 'OhMyPiDialogMethod', doc: 'extension_ui_request methods that BLOCK on a response' },
      { key: 'extensionMethods', frameKind: 'name', goTable: 'ExtensionMethod', tsTable: 'EXTENSION_METHOD', tsType: 'OhMyPiExtensionMethod', doc: 'fire-and-forget extension_ui_request methods, and the withdrawal of a dialog' },
      { key: 'toolNames', goTable: 'Tool', tsTable: 'TOOL', tsType: 'OhMyPiTool', doc: 'tool names the providers dispatch on' },
      { key: 'todoStatuses', goTable: 'TodoStatus', tsTable: 'TODO_STATUS', tsType: 'OhMyPiTodoStatus', doc: 'the status of one task of the `todo` tool' },
      { key: 'approvalModes', goTable: 'ApprovalMode', tsTable: 'APPROVAL_MODE', tsType: 'OhMyPiApprovalMode', doc: 'tool approval modes, carried on LeapMux\'s permission-mode axis' },
      { key: 'dialogResponse', goTable: 'DialogResponse', tsTable: 'DIALOG_RESPONSE', tsType: 'OhMyPiDialogResponseField', doc: 'fields of an extension_ui_response' },
      { key: 'approvalDialog', goTable: 'ApprovalDialog', tsTable: 'APPROVAL_DIALOG', tsType: 'OhMyPiApprovalDialogText', doc: 'the title prefix and the two options of a tool approval dialog' },
      { key: 'frameFields', goTable: 'FrameField', tsTable: 'FRAME_FIELD', tsType: 'OhMyPiFrameField', doc: 'fields of a tool-execution frame that the incomplete-tool resolve reads on both sides' },
      { key: 'askQuestion', goTable: 'AskQuestion', tsTable: 'ASK_QUESTION', tsType: 'OhMyPiAskQuestionField', doc: 'fields of one question of an `ask` call' },
      { key: 'askOption', goTable: 'AskOption', tsTable: 'ASK_OPTION', tsType: 'OhMyPiAskOptionField', doc: 'fields of one option of one question' },
      { key: 'askTypes', frameKind: 'name', goTable: 'AskType', tsTable: 'ASK_TYPE', tsType: 'OhMyPiAskType', owner: 'LeapMux chose these', doc: 'values of the `type` field of the question-bridge request and of its answer' },
      { key: 'askEnvelope', goTable: 'AskEnvelope', tsTable: 'ASK_ENVELOPE', tsType: 'OhMyPiAskEnvelopeField', owner: 'LeapMux chose these', doc: 'fields of the question-bridge request and of its answer' },
      { key: 'askAnswer', goTable: 'AskAnswer', tsTable: 'ASK_ANSWER', tsType: 'OhMyPiAskAnswerField', owner: 'LeapMux chose these', doc: 'fields of the answer to one question' },
      { key: 'supplement', goTable: 'Supplement', tsTable: 'SUPPLEMENT', tsType: 'OhMyPiSupplementField', owner: 'LeapMux chose these', doc: 'keys of the envelope a retained tool call keeps beside its start frame' },
      { key: 'customTypes', frameKind: 'name', goTable: 'CustomType', tsTable: 'CUSTOM_TYPE', tsType: 'OhMyPiCustomType', doc: 'the `customType` of a message that omp injects' },
      { key: 'commands', frameKind: 'name', goTable: 'Command', tsTable: 'COMMAND', tsType: 'OhMyPiCommand', doc: 'the commands that the worker writes to stdin and whose response the browser reads' },
      { key: 'asyncJobStates', goTable: 'AsyncJobState', tsTable: 'ASYNC_JOB_STATE', tsType: 'OhMyPiAsyncJobState', doc: 'the `state` of a background job in `details.async`' },
    ],
  },
  {
    name: 'amp-protocol',
    goPrefix: 'Amp',
    tsPrefix: 'AMP',
    title: 'Amp',
    preamble: [
      'Amp owns the line, block and result words and the subagent tool names. LeapMux owns the',
      'permission modes, the agent-mode option id and the permission-request envelope: Amp asks',
      'for no permission in stream-JSON mode, so the worker publishes one request for each call',
      'that Amp\'s delegate rule hands to the LeapMux helper, and the browser draws it.',
    ].join('\n// '),
    tables: [
      { key: 'lineTypes', frameKind: 'name', goTable: 'LineType', tsTable: 'LINE_TYPE', tsType: 'AmpLineType', doc: 'stdout line `type` values' },
      { key: 'blockTypes', goTable: 'BlockType', tsTable: 'BLOCK_TYPE', tsType: 'AmpBlockType', doc: 'content block `type` values of an assistant or user line' },
      { key: 'resultSubtypes', frameKind: 'name', goTable: 'ResultSubtype', tsTable: 'RESULT_SUBTYPE', tsType: 'AmpResultSubtype', doc: '`subtype` values of a `result` line' },
      { key: 'subagentTools', goTable: 'SubagentTool', tsTable: 'SUBAGENT_TOOL', tsType: 'AmpSubagentTool', doc: 'tool names that Amp runs as a subagent on its server' },
      { key: 'shellTools', goTable: 'ShellTool', tsTable: 'SHELL_TOOL', tsType: 'AmpShellTool', doc: 'names of the shell tools that start and follow a command, which can go on in the background' },
      { key: 'shellResultFields', goTable: 'ShellResultField', tsTable: 'SHELL_RESULT_FIELD', tsType: 'AmpShellResultField', doc: 'fields of the result record of the shell tools that both sides read' },
      { key: 'permissionModes', goTable: 'PermissionMode', tsTable: 'PERMISSION_MODE', tsType: 'AmpPermissionMode', owner: 'LeapMux chose these', doc: 'permission-mode values for Amp' },
      { key: 'options', goTable: 'Option', tsTable: 'OPTION', tsType: 'AmpOption', owner: 'LeapMux chose these', doc: 'option-group ids of the axes Amp does not report' },
      { key: 'permissionRequestTypes', frameKind: 'name', goTable: 'PermissionRequestType', tsTable: 'PERMISSION_REQUEST_TYPE', tsType: 'AmpPermissionRequestType', owner: 'LeapMux chose these', doc: 'values of the `type` field of the permission-request envelope' },
      { key: 'permissionRequestFields', goTable: 'PermissionRequestField', tsTable: 'PERMISSION_REQUEST_FIELD', tsType: 'AmpPermissionRequestField', owner: 'LeapMux chose these', doc: 'fields of the permission-request envelope' },
    ],
  },
  {
    name: 'cline-protocol',
    goPrefix: 'Cline',
    tsPrefix: 'CLINE',
    title: 'Cline',
    preamble: [
      'Cline owns the hub event names and fields, the tool names and prefixes, the capability names, the run',
      'reasons, the notice kinds and phases and the team lifecycle words. LeapMux owns the permission modes and the',
      'answer field of a question: the worker persists each transcript row as Cline\'s own hub',
      'event envelope and answers the hub\'s approvals and questions, and the browser draws both.',
    ].join('\n// '),
    tables: [
      { key: 'eventFields', goTable: 'EventField', tsTable: 'EVENT_FIELD', tsType: 'ClineEventField', doc: 'fields of a hub event envelope that both sides read' },
      { key: 'events', frameKind: 'name', goTable: 'Event', tsTable: 'EVENT', tsType: 'ClineEvent', doc: 'hub event names that reach the transcript or the control channel' },
      { key: 'tools', goTable: 'Tool', tsTable: 'TOOL', tsType: 'ClineTool', doc: 'tool names that both sides dispatch on' },
      { key: 'toolPrefixes', goTable: 'ToolPrefix', tsTable: 'TOOL_PREFIX', tsType: 'ClineToolPrefix', doc: 'prefixes of the tool names that both sides dispatch on' },
      { key: 'capabilities', frameKind: 'name', goTable: 'Capability', tsTable: 'CAPABILITY', tsType: 'ClineCapability', doc: 'capability names that the hub asks the client to answer' },
      { key: 'runReasons', goTable: 'RunReason', tsTable: 'RUN_REASON', tsType: 'ClineRunReason', doc: '`reason` values of the final event of a run' },
      { key: 'noticeKinds', goTable: 'NoticeKind', tsTable: 'NOTICE_KIND', tsType: 'ClineNoticeKind', doc: '`metadata.kind` values of a `session.notice` that both sides read' },
      { key: 'noticePhases', goTable: 'NoticePhase', tsTable: 'NOTICE_PHASE', tsType: 'ClineNoticePhase', doc: '`metadata.phase` values of a `session.notice`' },
      { key: 'teamRunEvents', frameKind: 'name', goTable: 'TeamRunEvent', tsTable: 'TEAM_RUN_EVENT', tsType: 'ClineTeamRunEvent', doc: '`lastEvent.eventType` values of a `team.progress` event for a teammate run' },
      { key: 'permissionModes', goTable: 'PermissionMode', tsTable: 'PERMISSION_MODE', tsType: 'ClinePermissionMode', owner: 'LeapMux chose these', doc: 'permission-mode values for Cline' },
      { key: 'questionAnswer', goTable: 'QuestionAnswer', tsTable: 'QUESTION_ANSWER', tsType: 'ClineQuestionAnswerField', owner: 'LeapMux chose these', doc: 'fields of the control response that answer a question' },
      { key: 'declineReasons', goTable: 'DeclineReason', tsTable: 'DECLINE_REASON', tsType: 'ClineDeclineReason', owner: 'LeapMux chose these', doc: 'reasons that the worker sends for a refusal that the user gave no words for' },
      { key: 'approvalReply', goTable: 'ApprovalReply', tsTable: 'APPROVAL_REPLY', tsType: 'ClineApprovalReplyField', doc: 'fields of the answer to a tool approval, as Cline\'s approval.respond takes it -- LeapMux adds `permissionMode`, which the worker strips' },
      { key: 'capabilityReply', goTable: 'CapabilityReply', tsTable: 'CAPABILITY_REPLY', tsType: 'ClineCapabilityReplyField', doc: 'fields of the answer to a capability request, as Cline\'s capability.respond takes it' },
    ],
  },
]

/**
 * The Go types a generated struct field may take, and what each one means.
 *
 * Deliberately small. A field whose type is not here belongs in a hand-written struct:
 * the generated ones live in `package contracts`, which the worker imports, so they
 * can refer to no type the worker owns. `#Name` refers to ANOTHER struct of the same
 * domain, which is how a nested record stays generated too.
 */
const GO_FIELD_TYPES = new Set([
  'string',
  'bool',
  'int',
  'float64',
  '*string',
  '*int',
  'json.RawMessage',
  '[]string',
  '[]json.RawMessage',
  'map[string]string',
  'map[string]json.RawMessage',
])

/** The other struct of the same domain a field type refers to, or null. */
function structReference(type) {
  return type.replace(/^(?:\[\]|map\[string\])?/, '').startsWith('#')
    ? type.replace(/^(?:\[\]|map\[string\])?#/, '')
    : null
}

/**
 * Whether a `#Name` field puts the struct it refers to behind a slice or a map.
 *
 * Go rejects a struct that contains ITSELF by value, and a `#Name` field emits by
 * value. A slice header and a map header are pointers, so either one breaks the
 * recursion and makes a self-reference or a longer cycle legal.
 */
function structReferenceIsIndirect(type) {
  return type.startsWith('[]') || type.startsWith('map[string]')
}

/** The Go spelling of a field type, with `#Name` resolved to the generated struct. */
function goFieldType(type) {
  return type.replace('#', '')
}

/**
 * The structs a domain generates, if any.
 *
 * A struct states its FIELD LIST once and takes every json tag from a table that the
 * browser already reads. A Go struct tag takes a LITERAL, so a hand-written one can
 * only be held to the contract by a reflection test that someone must remember to
 * extend -- and five Pi fields showed that nobody does. Generating the struct removes
 * the tag from the source entirely, so there is nothing left to drift.
 */
export function checkProviderStructs(spec, p) {
  const file = `${spec.name}.json`
  const structs = p.structs ?? {}
  const declared = new Set(spec.tables.map(t => t.key))
  // Every package-level name this domain emits beside the structs: one constant for
  // each table key, and the ordered key slice of each `goSlice` table. A struct that
  // takes one of those names redeclares it, and the compiler then reports the
  // GENERATED file, which states no contract and no domain.
  const constants = new Set(spec.tables.flatMap(t => Object.keys(p[t.key] ?? {}).map(key => `${spec.goPrefix}${t.goTable}${key}`)))
  for (const t of spec.tables) {
    if (t.goSlice)
      constants.add(`${spec.goPrefix}${t.goTable}Keys`)
  }
  for (const [name, definition] of Object.entries(structs)) {
    mustBe(/^[A-Z][A-Za-z0-9]*$/.test(name), file, `structs.${name} must be a PascalCase Go type name`)
    mustBe(!constants.has(name), file, `structs.${name} takes the name of a constant or key slice this domain already emits -- pick another, or the generated package will not compile`)
    mustBe(declared.has(definition.table), file, `structs.${name}.table ${JSON.stringify(definition.table)} is not a declared table`)
    const seen = new Set()
    for (const field of definition.fields) {
      const table = field.table ?? definition.table
      mustBe(declared.has(table), file, `structs.${name}.${field.key} specifies table ${JSON.stringify(table)}, which is not declared`)
      mustBe(p[table][field.key] != null, file, `structs.${name}.${field.key} is not a key of the ${table} table`)
      mustBe(!seen.has(field.key), file, `structs.${name} lists ${field.key} twice`)
      seen.add(field.key)
      const reference = structReference(field.type)
      if (reference != null) {
        mustBe(structs[reference] != null, file, `structs.${name}.${field.key} refers to ${reference}, which this domain does not declare`)
        continue
      }
      mustBe(GO_FIELD_TYPES.has(field.type), file, `structs.${name}.${field.key} has type ${JSON.stringify(field.type)}, which is not one a generated struct may take`)
    }
  }
  checkStructReferencesAreAcyclic(file, structs)
  return {}
}

/**
 * Refuse a cycle of BY-VALUE struct references.
 *
 * `emitGoStructs` emits a `#Name` field by value, so a struct that reaches itself
 * through one or more of those fields is an invalid recursive type. Go reports that
 * against the GENERATED file, which states no contract and no domain, and only at
 * `task build` -- `task generate` passes, because the existence check above proves
 * the reference resolves and asks nothing more. A slice or a map breaks the
 * recursion, so `[]#Name` and `map[string]#Name` are not edges of this graph.
 *
 * Every reference resolves by the time this runs, so the walk cannot leave the
 * declared set.
 */
function checkStructReferencesAreAcyclic(file, structs) {
  const edges = new Map(Object.entries(structs).map(([name, definition]) => [
    name,
    definition.fields
      .filter(field => structReference(field.type) != null && !structReferenceIsIndirect(field.type))
      .map(field => structReference(field.type)),
  ]))
  const acyclic = new Set()
  const chain = []
  const visit = (name) => {
    const opened = chain.indexOf(name)
    if (opened >= 0) {
      const cycle = [...chain.slice(opened), name].join(' -> ')
      mustBe(false, file, `structs.${cycle} is a cycle of by-value references, which Go rejects as an invalid recursive type -- put one step of it behind []#Name or map[string]#Name`)
    }
    if (acyclic.has(name))
      return
    chain.push(name)
    for (const next of edges.get(name) ?? [])
      visit(next)
    chain.pop()
    acyclic.add(name)
  }
  for (const name of edges.keys())
    visit(name)
}

/** The Go struct declarations of one domain, in the order the contract states them. */
function emitGoStructs(spec, p) {
  const structs = Object.entries(p.structs ?? {})
  if (structs.length === 0)
    return { blocks: [], needsJSON: false }
  let needsJSON = false
  const blocks = structs.map(([name, definition]) => {
    const rows = definition.fields.map((field) => {
      if (field.type.includes('json.RawMessage'))
        needsJSON = true
      const tag = p[field.table ?? definition.table][field.key] + (field.omitempty ? ',omitempty' : '')
      return { name: field.key, type: goFieldType(field.type), tag: `\`json:${jsonString(tag)}\`` }
    })
    const nameWidth = Math.max(...rows.map(r => r.name.length))
    const typeWidth = Math.max(...rows.map(r => r.type.length))
    const body = rows.map(r => `\t${r.name.padEnd(nameWidth)} ${r.type.padEnd(typeWidth)} ${r.tag}`).join('\n')
    const doc = (definition.doc ?? `${name} is one ${spec.title} record.`).split('\n').map(line => `// ${line}`.trimEnd()).join('\n')
    const tables = [...new Set(definition.fields.map(f => f.table ?? definition.table))]
    const from = tables.length === 1 ? `the \`${tables[0]}\` table` : `the ${tables.map(t => `\`${t}\``).join(' and ')} tables`
    return `${doc}\n//\n// Its json tags come from ${from}, so a rename in the contract\n// reaches this struct and the browser's reader in ONE change.\ntype ${name} struct {\n${body}\n}`
  })
  return { blocks, needsJSON }
}

/** The language sides a generated table can be read from. */
const READER_SIDES = new Set(['go', 'ts'])

/** The values of `frameKind`: a whole kind, or the start of a family of kinds. */
const FRAME_KIND_MATCHES = new Set(['name', 'prefix'])

/**
 * The sides that must import one table, and the reason a one-sided table is one-sided.
 *
 * A table is read from BOTH sides unless it says otherwise, because that is what earns
 * a value its place in a contract. A one-sided table states `readers` and `readersWhy`,
 * and `contractsAreConsumed.test.mjs` holds each side to what it declares here.
 */
export function tableReaders(t) {
  return t.readers ?? ['go', 'ts']
}

/**
 * A provider protocol is valid when every declared table is present and non-empty,
 * every table the FILE carries is declared (so a table added to the JSON cannot sit
 * unemitted), and no two keys inside one table share a literal -- a duplicate would
 * make two dispatch branches indistinguishable on the wire. Each table also states
 * its readers and its `frameKind` from the known values.
 */
export function checkProviderProtocol(spec, p) {
  const file = `${spec.name}.json`
  const declared = spec.tables.map(t => t.key)
  for (const t of spec.tables) {
    const readers = tableReaders(t)
    mustBe(readers.length > 0, file, `table ${t.key} declares no reader -- a table nothing reads is a contract for a value that does not cross the boundary, so delete it`)
    for (const side of readers)
      mustBe(READER_SIDES.has(side), file, `table ${t.key} specifies the reader ${JSON.stringify(side)}, which is not one of go, ts`)
    // Compare the SET, never the length. `readers: ['ts', 'ts']` has the length of
    // the full set, so a length test took it for a two-sided table and demanded no
    // readersWhy, while every per-element test above passed.
    const sides = new Set(readers)
    mustBe(sides.size === readers.length, file, `table ${t.key} lists a reader twice -- give each side once, because a repeat reads as a second side and hides that the table is one-sided`)
    mustBe(sides.size === READER_SIDES.size || typeof t.readersWhy === 'string', file, `table ${t.key} is read from ${readers.join(' alone, ')} alone and must say why in readersWhy -- the next reader has to know whether that is the design or a key somebody forgot to wire`)
    if (t.goTagPin != null) {
      mustBe(sides.has('go'), file, `table ${t.key} states goTagPin but does not list go as a reader -- the pin exists to explain a Go reader that is a test`)
      mustBe(typeof t.goTagPin === 'string' && t.goTagPin.endsWith('_test.go'), file, `table ${t.key} must give goTagPin as the path of the Go test that pins the hand-written struct tags to this table`)
    }
    // A misspelled mark would drop the table from the lint with no message, because
    // the collector reads only the two values it knows.
    if (t.frameKind != null)
      mustBe(FRAME_KIND_MATCHES.has(t.frameKind), file, `table ${t.key} states frameKind ${JSON.stringify(t.frameKind)}, which is not one of ${[...FRAME_KIND_MATCHES].join(', ')}`)
  }
  const present = Object.keys(p).filter(k => !k.startsWith('_') && k !== 'structs' && typeof p[k] === 'object')
  for (const key of declared)
    mustBe(p[key] != null && Object.keys(p[key]).length > 0, file, `${key} is missing or empty`)
  for (const key of present)
    mustBe(declared.includes(key), file, `table ${key} is not declared in PROVIDER_PROTOCOLS -- a new table must be registered in the same change, or it is never emitted`)
  // `emitGoProviderProtocol` emits `var <Prefix><Table>Keys` beside the constants of
  // a goSlice table, so a key called Keys emits a const and a var of one name.
  for (const t of spec.tables) {
    mustBe(!t.goSlice || p[t.key]?.Keys == null, file, `${t.key}.Keys collides with the ordered key slice ${spec.goPrefix}${t.goTable}Keys that this goSlice table emits -- rename the key, or the generated package will not compile`)
  }
  // A DEFAULTS table is the one table whose literals may repeat. Its keys are axes,
  // not dispatch branches, and two axes legitimately rest at the same word -- Codex
  // starts both its collaboration mode and its service tier at `default`. What it must
  // hold instead is one entry for each key of the table it supplies defaults for: a
  // default with no axis states a value nothing applies, and an axis with no default
  // sends the agent up with an empty option the other side then fills by hand.
  const defaultsTables = new Set(spec.tables.filter(t => t.defaultsFor != null).map(t => t.key))
  for (const t of spec.tables) {
    if (t.defaultsFor == null)
      continue
    mustBe(declared.includes(t.defaultsFor), file, `table ${t.key} states defaultsFor ${JSON.stringify(t.defaultsFor)}, which is not a declared table`)
    const axes = Object.keys(p[t.defaultsFor] ?? {})
    const defaults = new Set(Object.keys(p[t.key] ?? {}))
    for (const axis of axes)
      mustBe(defaults.has(axis), file, `${t.defaultsFor}.${axis} has no entry in ${t.key} -- every axis states the value it starts at, or one side invents one`)
    for (const key of defaults)
      mustBe(axes.includes(key), file, `${t.key}.${key} supplies a default for no key of ${t.defaultsFor} -- delete it, or add the axis it belongs to`)
  }
  for (const key of declared) {
    if (defaultsTables.has(key))
      continue
    const seen = new Map()
    for (const [name, literal] of Object.entries(p[key])) {
      mustBe(!seen.has(literal), file, `${key}.${name} repeats the literal ${JSON.stringify(literal)} already used by ${key}.${seen.get(literal)} -- two dispatch branches would be indistinguishable on the wire`)
      seen.set(literal, name)
    }
  }
  checkProviderStructs(spec, p)
  checkUnreadKeys(spec, p)
  // A key-valued pointer must specify a member of the table it points into, or the
  // emitted default states a value the enum does not carry.
  if (p.defaultMode != null)
    mustBe(p.modes[p.defaultMode] != null, file, `defaultMode ${JSON.stringify(p.defaultMode)} is not a key of modes`)
  return {}
}

/**
 * The per-key exemptions a domain states in its `_unread` block.
 *
 * `contractsAreConsumed.test.mjs` holds every KEY of a table to a reader, not only
 * the table as a whole. A key that one declared side genuinely never reads states
 * that here, once, with the reason -- `_unread.<table>.<Key>.sides` lists the sides
 * that skip it and `.why` says why. The key stays in the contract, because the other
 * side still reads it and a rename must still reach that side.
 *
 * The answer is a Map from `<table>.<Key>` to the Set of sides that skip it.
 */
export function unreadKeys(p) {
  const unread = new Map()
  for (const [table, keys] of Object.entries(p._unread ?? {})) {
    for (const [key, exemption] of Object.entries(keys))
      unread.set(`${table}.${key}`, new Set(exemption.sides))
  }
  return unread
}

/**
 * An exemption must point at a real key, and at a side that really reads the table.
 *
 * The shape is checked here; whether the exemption is still NEEDED is checked by
 * `contractsAreConsumed.test.mjs`, which is the half that can read the source tree.
 */
function checkUnreadKeys(spec, p) {
  const file = `${spec.name}.json`
  const tables = new Map(spec.tables.map(t => [t.key, t]))
  for (const [table, keys] of Object.entries(p._unread ?? {})) {
    const t = tables.get(table)
    mustBe(t != null, file, `_unread.${table} is not a declared table`)
    const readers = new Set(tableReaders(t))
    for (const [key, exemption] of Object.entries(keys)) {
      mustBe(p[table]?.[key] != null, file, `_unread.${table}.${key} is not a key of the ${table} table -- delete the exemption, or restore the key`)
      mustBe(Array.isArray(exemption.sides) && exemption.sides.length > 0, file, `_unread.${table}.${key}.sides must list at least one side that skips the key`)
      for (const side of exemption.sides)
        mustBe(readers.has(side), file, `_unread.${table}.${key} excuses the side ${JSON.stringify(side)}, which the ${table} table does not declare as a reader at all`)
      mustBe(new Set(exemption.sides).size === exemption.sides.length, file, `_unread.${table}.${key}.sides lists a side twice`)
      mustBe(typeof exemption.why === 'string' && exemption.why.length > 30, file, `_unread.${table}.${key}.why must say what reads the key instead, or the next reader cannot tell the design from a gap`)
    }
  }
}

export function emitGoProviderProtocol(spec, p) {
  const blocks = spec.tables.flatMap((t) => {
    const decls = Object.entries(p[t.key])
      .map(([name, literal]) => ({ name: `${spec.goPrefix}${t.goTable}${name}`, value: jsonString(literal) }))
    const block = `// ${spec.goPrefix}${t.goTable}* are ${t.doc}.\nconst (\n${goConstBlock(decls)}\n)`
    if (!t.goSlice)
      return [block]
    // The MEMBERSHIP of this table crosses the boundary, not only the spellings: the
    // browser derives the same set with Object.values(), so a Go copy listed by hand
    // checks a different number of keys once the table grows, and neither side fails.
    const rows = decls.map(d => `\t${d.name},`).join('\n')
    const list = `// ${spec.goPrefix}${t.goTable}Keys is every key of that table, in the contract's own order.\nvar ${spec.goPrefix}${t.goTable}Keys = []string{\n${rows}\n}`
    return [`${block}\n\n${list}`]
  })
  const extra = p.defaultMode == null
    ? ''
    : `\n// ${spec.goPrefix}DefaultMode is the mode a fresh session runs on.\nconst ${spec.goPrefix}DefaultMode = ${spec.goPrefix}Mode${p.defaultMode}\n`
  const preamble = spec.preamble ?? `${spec.title}'s wire vocabulary. These literals are dispatch keys on BOTH sides --
// the Go worker classifies each row, the browser plugin renders it -- so they are
// generated from one file rather than hand-copied into two. ${spec.title}'s vendor owns
// the values; LeapMux follows them.`
  const { blocks: structs, needsJSON } = emitGoStructs(spec, p)
  const imports = needsJSON ? '\nimport "encoding/json"\n' : ''
  const declarations = [...blocks, ...structs].join('\n\n')
  return `${GO_HEADER(`${spec.name}.json`)}package contracts
${imports}
// ${preamble}

${declarations}
${extra}`
}

export function emitTsProviderProtocol(spec, p) {
  const blocks = spec.tables.map((t) => {
    const rows = Object.entries(p[t.key]).map(([name, literal]) => `  ${name}: ${jsonString(literal)},`).join('\n')
    const symbol = `${spec.tsPrefix}_${t.tsTable}`
    // `owner` identifies who chose the literals when that is not the provider's
    // vendor. A generated comment that claims the wrong owner is worse than none, so
    // a table whose keys have TWO owners states the split in its doc and omits this.
    return `/** ${t.owner ?? spec.title} ${t.doc}. */\nexport const ${symbol} = {\n${rows}\n} as const\nexport type ${t.tsType} = typeof ${symbol}[keyof typeof ${symbol}]`
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

/**
 * Every literal of every table that states `frameKind`, across all the provider
 * protocols, for the lint that keeps frame kinds out of shared browser code.
 *
 * `protocols` holds one `{ spec, p }` for each domain, and `checkProviderProtocol`
 * must accept each one first. The entries keep the order of the domains, the tables
 * and the keys, so the output is stable. A literal that two tables share keeps one
 * entry for each, so the lint can state every owner.
 */
export function emitTsProviderFrameKinds(protocols) {
  const rows = protocols.flatMap(({ spec, p }) => spec.tables
    .filter(t => t.frameKind != null)
    .flatMap(t => Object.values(p[t.key]).map(literal =>
      `  { literal: ${jsonString(literal)}, match: ${jsonString(t.frameKind)}, source: ${jsonString(`${spec.name} ${t.key}`)} },`)))
  return `${TS_HEADER('*-protocol.json')}
// Every literal that identifies the kind of a frame in one provider protocol: each value
// of a table that states \`frameKind\` in PROVIDER_PROTOCOLS. A reader dispatches on a
// frame kind, so shared browser code that spells one decides by the provider. The
// \`no-provider-decision\` rule in frontend/eslint/chatPipelinePlugin.ts rejects it.

/** One frame kind, and the table that holds it. */
export interface ProviderFrameKind {
  /** The literal that the provider sends or receives. */
  readonly literal: string
  /** \`name\` for a whole kind, \`prefix\` for the start of a family of kinds. */
  readonly match: 'name' | 'prefix'
  /** The contract and the table, such as \`copilot-protocol events\`. */
  readonly source: string
}

export const PROVIDER_FRAME_KINDS: readonly ProviderFrameKind[] = [
${rows.map(row => `${row}\n`).join('')}]
`
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

export function emitGoChatHistory(v) {
  return `${GO_HEADER('chat-history.json')}package contracts

// Shared chat history limits. The worker caps pages at MessagePageLimit.
// The browser re-anchors when a catch-up gap exceeds CatchUpGapLimit.
const (
${goConstBlock([
  { name: 'MessagePageLimit', value: String(v.messagePageLimit) },
  { name: 'CatchUpGapLimit', value: String(v.catchUpGapLimit) },
])}
)
`
}

export function emitTsChatHistory(v) {
  return `${TS_HEADER('chat-history.json')}
/** The maximum number of messages in one history page. */
export const MESSAGE_PAGE_LIMIT = ${v.messagePageLimit} as const

/** The largest sequence gap that the browser drains before it re-anchors. */
export const CATCH_UP_GAP_LIMIT = ${v.catchUpGapLimit}n
`
}

/**
 * One queued agent input's caps. The browser pre-checks a composer draft and
 * the Worker enforces the same numbers, so both must measure ONE quantity:
 * the text bytes plus every attachment's bytes.
 */
export function checkAgentInput(a) {
  const { maxItemBytes, maxItems, maxAttachmentsPerItem } = a.limits
  // A single attachment cannot be larger than the whole item, and the queue
  // must hold at least one item; either would make the browser's pre-check
  // admit a draft the Worker refuses.
  if (maxAttachmentsPerItem > maxItemBytes)
    throw new Error(`agent-input.json: maxAttachmentsPerItem ${maxAttachmentsPerItem} exceeds maxItemBytes ${maxItemBytes}`)
  if (maxItems < 1)
    throw new Error(`agent-input.json: maxItems ${maxItems} must hold at least one item`)
}

export function emitGoAgentInput(a) {
  return `${GO_HEADER('agent-input.json')}package contracts

// Caps on one queued agent input. The browser refuses an over-size composer
// draft against the same numbers before it sends EnqueueAgentInput, so a
// draft that passes the client check cannot fail Store.Enqueue.
// MaxAgentInputItemBytes counts the text bytes PLUS every attachment's bytes.

const (
${goConstBlock([
  { name: 'MaxAgentInputItemBytes', value: String(a.limits.maxItemBytes) },
  { name: 'MaxAgentInputItems', value: String(a.limits.maxItems) },
  { name: 'MaxAgentInputAttachmentsPerItem', value: String(a.limits.maxAttachmentsPerItem) },
])}
)
`
}

export function emitTsAgentInput(a) {
  return `${TS_HEADER('agent-input.json')}
// Caps on one queued agent input, generated from contracts/agent-input.json
// (the Worker reads the same numbers). MAX_AGENT_INPUT_ITEM_BYTES counts the
// text bytes PLUS every attachment's bytes, which is what Store.Enqueue
// measures -- a client check over the attachments alone admits a draft the
// Worker refuses.

export const MAX_AGENT_INPUT_ITEM_BYTES = ${a.limits.maxItemBytes} as const
export const MAX_AGENT_INPUT_ITEMS = ${a.limits.maxItems} as const
export const MAX_AGENT_INPUT_ATTACHMENTS_PER_ITEM = ${a.limits.maxAttachmentsPerItem} as const
`
}

// ---------------------------------------------------------------------------
// external-apps: the applications the desktop sidecar opens a directory in
//
// The id vocabulary was hand-written twice -- the three Go spec tables and the
// browser's icon table -- paired by a comment that said "must match". The ids
// and the operating systems that carry them cross the boundary. Detection
// (which binary, which bundle, in what probe order) and the display names stay
// Go-only, because the sidecar reports the names at runtime and nothing else
// ever spells them.
//
// `kind` is BROWSER-ONLY, and it lives here rather than in the frontend because
// the one-file-manager-per-OS check below reads it beside `oses`. It used to
// ride the wire as a proto enum, which made a compile-time constant take a
// four-hop trip -- contract, Go table, proto enum, Rust i32 -- to answer one
// boolean the browser can read from a generated table instead.
// ---------------------------------------------------------------------------

/**
 * The operating systems a spec table can exist for, in emission order.
 *
 * The schema's `oses` enum states the same vocabulary, and `checkExternalApps`
 * asserts the two agree. Without that assertion a token the schema accepts but
 * this list omits passes generation silently: `emitGoExternalApps` iterates
 * only this list, so the new OS gets no `ExternalAppIDsByOS` entry and any app
 * exclusive to it disappears from the spec-table comparison altogether, while
 * the file-manager check below never looks at that platform.
 */
const EXTERNAL_APP_OSES = ['darwin', 'linux', 'windows']

const EXTERNAL_APP_FILE_MANAGER_KIND = 'EXTERNAL_APP_KIND_FILE_MANAGER'

export function checkExternalApps(a) {
  // No proto enum backs these any more, so the schema's name pattern is the
  // only shape rule and this is the one place that can reject the sentinel.
  for (const name of Object.keys(a.kinds))
    mustBe(!name.endsWith('_UNSPECIFIED'), 'external-apps.json', `${name} is the unset value, and no app may claim it`)

  const used = new Set()
  for (const [id, app] of Object.entries(a.apps)) {
    mustBe(a.kinds[app.kind] != null, 'external-apps.json', `app ${id} carries kind ${app.kind}, which is not a kinds entry`)
    used.add(app.kind)
    // The reverse direction of EXTERNAL_APP_OSES, which the emitters iterate.
    // An os token this generator does not know is silently dropped from every
    // table it writes, so the app vanishes from the sidecar's own spec-table
    // comparison and from the file-manager check below.
    for (const os of app.oses)
      mustBe(EXTERNAL_APP_OSES.includes(os), 'external-apps.json', `app ${id} lists os ${os}, which the generator does not emit a table for -- add it to EXTERNAL_APP_OSES in scripts/generate-contracts.mjs`)
  }
  for (const name of Object.keys(a.kinds))
    mustBe(used.has(name), 'external-apps.json', `kind ${name} is carried by no app -- a kind the menu can never show is dead metadata`)

  // Exactly one file manager for each operating system. The app menu renders
  // that kind as its own leading group, and the split button treats it as the
  // one app that is always available. Two would make the group a choice the
  // user must make, and none would empty the group on one platform only.
  for (const os of EXTERNAL_APP_OSES) {
    const managers = Object.entries(a.apps)
      .filter(([, m]) => m.kind === EXTERNAL_APP_FILE_MANAGER_KIND && m.oses.includes(os))
      .map(([id]) => id)
    mustBe(managers.length === 1, 'external-apps.json', `${os} must carry exactly one ${EXTERNAL_APP_FILE_MANAGER_KIND}; it carries ${managers.length}: ${managers.join(', ')}`)
  }
  return {}
}

export function emitGoExternalApps(a) {
  const ids = Object.keys(a.apps)
  const byOS = EXTERNAL_APP_OSES.map((os) => {
    const rows = ids.filter(id => a.apps[id].oses.includes(os)).map(id => `\t\t${jsonString(id)},`).join('\n')
    return `\t${jsonString(os)}: {\n${rows}\n\t},`
  }).join('\n')
  return `${GO_HEADER('external-apps.json')}package contracts

// The applications the desktop sidecar can open a directory in. The sidecar's
// per-OS spec tables own the detection and the display names; this table owns
// the vocabulary the browser shares with them.
//
// No kind table here. What an application IS is read by the browser alone, so
// it is generated for TypeScript only -- the sidecar used to stamp it on every
// app it reported and send it over the wire, which carried a compile-time
// constant through four languages to answer one boolean.

// ExternalAppIDsByOS lists the ids each operating system's spec table must
// carry, keyed by runtime.GOOS. The sidecar's table test compares its own
// specs against this. The hand-written "core set" it replaces named a handful
// of ids and trusted review for the rest.
var ExternalAppIDsByOS = map[string][]string{
${byOS}
}
`
}

export function emitTsExternalApps(a) {
  const ids = Object.keys(a.apps)
  const rows = ids.map(id => `  ${jsonString(id)},`).join('\n')
  const kindRows = ids.map(id => `  ${jsonString(id)}: ${jsonString(a.apps[id].kind)},`).join('\n')
  const kindUnion = Object.keys(a.kinds).map(jsonString).join(' | ')
  const docs = Object.entries(a.kinds).map(([name, doc]) => ` * - \`${name}\`: ${doc}.`).join('\n')
  return `${TS_HEADER('external-apps.json')}
// Every application id the desktop sidecar can report, on any operating
// system. The icon table satisfies Record<ExternalAppId, ...>, so an id that
// the contract adds without an icon is a type error rather than a blank menu
// row.

export const SUPPORTED_EXTERNAL_APP_IDS = [
${rows}
] as const

export type ExternalAppId = typeof SUPPORTED_EXTERNAL_APP_IDS[number]

export type ExternalAppKind = ${kindUnion}

/**
 * What each application IS, so the app menu groups without testing an id
 * literal:
${docs}
 *
 * Read from the CONTRACT, not from the wire. The sidecar reports only ids its
 * own spec table holds, and a Go table test compares that table against this
 * same contract in BOTH directions, so the two cannot name different sets.
 */
export const EXTERNAL_APP_KIND_BY_ID: Record<ExternalAppId, ExternalAppKind> = {
${kindRows}
}
`
}

// ---------------------------------------------------------------------------
// orchestration
// ---------------------------------------------------------------------------

/** Reads the contracts; generate() requires every registered domain's file. */
export function loadContracts(contractsDir) {
  const read = name => JSON.parse(readFileSync(posix.join(contractsDir, `${name}.json`), 'utf8'))
  // Every contract the directory holds, so generate() can report a file no domain
  // reads. A schema is a SIBLING of its contract and never a domain of its own.
  const names = () => readdirSync(contractsDir)
    .filter(file => file.endsWith('.json') && !file.endsWith('.schema.json'))
    .map(file => file.slice(0, -'.json'.length))
    .sort()
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
  return { read, has, names }
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
    name: 'user-settings',
    reads: ['desktop'],
    emit(out, read) {
      const u = read('user-settings')
      // desktop.json is read here too: three settings draw their tokens
      // from its windowBehavior blocks, and this is the only place that
      // sees both contracts at once.
      checkUserSettings(u, read('desktop'))
      out['backend/generated/contracts/user-settings.go'] = emitGoUserSettings(u)
      out['frontend/src/generated/contracts/user-settings.ts'] = emitTsUserSettings(u)
    },
  },
  {
    name: 'chat-history',
    emit(out, read) {
      const v = read('chat-history')
      checkChatHistory(v)
      out['backend/generated/contracts/chat-history.go'] = emitGoChatHistory(v)
      out['frontend/src/generated/contracts/chat-history.ts'] = emitTsChatHistory(v)
    },
  },
  {
    name: 'agent-input',
    emit(out, read) {
      const a = read('agent-input')
      checkAgentInput(a)
      out['backend/generated/contracts/agent-input.go'] = emitGoAgentInput(a)
      out['frontend/src/generated/contracts/agent-input.ts'] = emitTsAgentInput(a)
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
  {
    name: 'external-apps',
    emit(out, read) {
      const a = read('external-apps')
      checkExternalApps(a)
      out['backend/generated/contracts/external-apps.go'] = emitGoExternalApps(a)
      out['frontend/src/generated/contracts/external-apps.ts'] = emitTsExternalApps(a)
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
  const { read, has, names } = loadContracts(contractsDir)
  // A contract no domain reads emits NOTHING, and nothing says so: it validates
  // against its schema, `task lint` passes, and the constants it declares never reach
  // either language. That is the inverse of the missing-file check below, and the two
  // together make the directory and the registry one list.
  const registered = new Set(DOMAINS.map(domain => domain.name))
  for (const name of names()) {
    mustBe(registered.has(name), `${name}.json`, 'is not registered -- add a DOMAINS entry so it emits, or delete the file; an unregistered contract emits nothing and no test notices')
  }
  const out = {}
  for (const domain of DOMAINS) {
    mustBe(has(domain.name), `${domain.name}.json`, 'is missing -- every registered domain ships its contract; retire a domain by removing its DOMAINS entry in the same change that deletes the file')
    // A domain that cross-checks against a SIBLING contract declares it in
    // `reads`, so the missing-file report stays this loop's rather than an
    // ENOENT out of the emitter -- which states a temp path and no domain.
    for (const dep of domain.reads ?? []) {
      mustBe(has(dep), `${dep}.json`, `is missing -- the ${domain.name} domain cross-checks against it`)
    }
    if (domain.requiresDescriptor) {
      mustBe(descriptorSet != null, `${domain.name}.json`, 'requires a buf descriptor (run via task generate-contracts)')
    }
    domain.emit(out, read, descriptorSet)
  }
  // The loop above checked each protocol, so each `frameKind` it states is valid.
  out['frontend/src/generated/contracts/provider-frame-kinds.ts'] = emitTsProviderFrameKinds(
    PROVIDER_PROTOCOLS.map(spec => ({ spec, p: read(spec.name) })),
  )
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
