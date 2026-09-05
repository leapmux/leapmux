// `task test-scripts` runs these tests for generate-contracts.mjs.
//
// Pure checks and emitters make each failure testable without staging or buf.
// Three areas carry risk:
//   - Incorrect arithmetic silently changes each derived wire limit.
//   - An incorrect name table can make one side omit a shared constant.
//   - Non-deterministic output defeats publication that preserves modification
//     times. Vite then refreshes after each `task generate` command.
// `generate()` uses the real contracts directory in its integration test.
// validate-json.test.mjs tests its rule table against the real tree too.

import { cpSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

import {
  bufDescriptor,
  checkCodexBypass,
  checkDesktop,
  checkExternalApps,
  checkHeaders,
  checkListen,
  checkProviderProtocol,
  checkProviders,
  checkRetry,
  checkScopes,
  checkSessionInfo,
  checkTabNames,
  checkTheme,
  checkTrustedProxies,
  checkValidate,
  checkWire,
  checkWorkerVocab,
  ContractError,
  deriveWire,
  DESKTOP_GO_BEHAVIOR_NAMES,
  DESKTOP_RS_BEHAVIOR_NAMES,
  DESKTOP_RS_MACOS_ONLY_EVENTS,
  DESKTOP_TS_BEHAVIOR_NAMES,
  emitGoDesktop,
  emitGoExternalApps,
  emitGoHeaders,
  emitGoListen,
  emitGoRetry,
  emitGoSessionInfo,
  emitGoTrustedProxies,
  emitGoValidate,
  emitGoWire,
  emitRsDesktop,
  emitTsDesktop,
  emitTsExternalApps,
  emitTsHeaders,
  emitTsListen,
  emitTsProviders,
  emitTsRetry,
  emitTsSessionInfo,
  emitTsTrustedProxies,
  emitTsValidate,
  emitTsWire,
  enumValues,
  generate,
  HEADERS_GO_NAMES,
  HEADERS_TS_NAMES,
  nameStripClass,
  RETRY_GO_NAMES,
  RETRY_TS_NAMES,
  SESSION_INFO_TABLES,
  usernameConstName,
  WIRE_GO_NAMES,
  WIRE_TS_NAMES,
} from './generate-contracts.mjs'

const ROOT = resolve(import.meta.dirname, '..')

/** One buf build for the whole suite: the descriptor is read-only and ROOT never changes under it. */
const DESCRIPTOR = bufDescriptor(ROOT)

const readContract = name => JSON.parse(readFileSync(join(ROOT, 'contracts', `${name}.json`), 'utf8'))

const WIRE = readContract('wire')
const HEADERS = readContract('headers')
const RETRY = readContract('retry')
const TRUSTED_PROXIES = readContract('trusted-proxies')

function expectContractError(fn, fragment) {
  try {
    fn()
  }
  catch (err) {
    expect(err).toBeInstanceOf(ContractError)
    expect(err.message).toContain(fragment)
    return
  }
  throw new Error(`expected a ContractError mentioning ${JSON.stringify(fragment)}`)
}

describe('deriveWire', () => {
  it('computes the chunk, reassembly, and ceiling derivations', () => {
    const d = deriveWire(WIRE)
    expect(d.maxPlaintextPerChunkBytes).toBe(65535 - 16)
    expect(d.maxReassembledMessageSizeBytes).toBe(WIRE.maxMessageSizeBytes + WIRE.innerEnvelopeHeadroomBytes)
    expect(d.sessionKeyHardCeilingMs).toBe(WIRE.sessionKey.maxAgeMs + WIRE.sessionKey.hardCeilingOverrunMs)
  })

  it('matches the values both sides shipped before contracts existed', () => {
    // Freeze the values from the Go and TypeScript sources before the contract.
    // A migration that changes one value must also update this assertion.
    const d = deriveWire(WIRE)
    expect(d.maxPlaintextPerChunkBytes).toBe(65519)
    expect(d.maxReassembledMessageSizeBytes).toBe(16842752)
    expect(d.sessionKeyHardCeilingMs).toBe(4200000)
  })
})

describe('checkWire', () => {
  it('accepts the shipped contract', () => {
    expect(() => checkWire(WIRE)).not.toThrow()
  })

  it('rejects a tag size that eats the whole ciphertext', () => {
    expectContractError(
      () => checkWire({ ...WIRE, noiseAeadTagSizeBytes: WIRE.maxCiphertextForChunkBytes }),
      'must stay positive',
    )
  })

  it('rejects a message budget smaller than one chunk', () => {
    expectContractError(
      () => checkWire({ ...WIRE, maxMessageSizeBytes: 1 }),
      'at least one chunk',
    )
  })

  it('rejects a configurable ceiling below the default message size', () => {
    expectContractError(
      () => checkWire({ ...WIRE, maxConfigurableMessageSizeBytes: WIRE.maxMessageSizeBytes - 1 }),
      '>= maxMessageSizeBytes',
    )
  })

  it('rejects a rekey interval longer than the key lifetime', () => {
    expectContractError(
      () => checkWire({ ...WIRE, sessionKey: { ...WIRE.sessionKey, minRekeyIntervalMs: WIRE.sessionKey.maxAgeMs + 1 } }),
      '<= sessionKey.maxAgeMs',
    )
  })

  it('rejects a framing width other than the uint32 both framers write', () => {
    expectContractError(
      () => checkWire({ ...WIRE, framing: { lengthPrefixBytes: 8 } }),
      'must be 4',
    )
  })

  it('rejects a name-table key flattenWire does not provide', () => {
    // A table key that is absent from the flat object emits `undefined`.
    // The coverage check returns a ContractError that identifies the key.
    expectContractError(
      () => checkWire({ ...WIRE, protocolVersion: undefined }),
      'name table',
    )
  })

  it('rejects a hard nonce limit at or below the soft limit', () => {
    // The soft trigger must run before the wrap limit. Otherwise, the session
    // rekeys after it passes the refusal point.
    expectContractError(
      () => checkWire({ ...WIRE, noise: { softNonceLimit: WIRE.noise.softNonceLimit, hardNonceLimit: WIRE.noise.softNonceLimit } }),
      '< noise.hardNonceLimit',
    )
    expectContractError(
      () => checkWire({ ...WIRE, noise: { ...WIRE.noise, hardNonceLimit: 2 ** 32 } }),
      'uint32 nonce space',
    )
  })
})

describe('checkHeaders / checkRetry', () => {
  it('accepts the shipped contracts', () => {
    expect(() => checkHeaders(HEADERS)).not.toThrow()
    expect(() => checkRetry(RETRY)).not.toThrow()
  })

  it('rejects an un-namespaced header', () => {
    expectContractError(
      () => checkHeaders({ ...HEADERS, elevationRequired: 'X-Elevation' }),
      'Leapmux-Namespaced-Header',
    )
  })

  it('rejects a header with no name-table entry instead of emitting nothing', () => {
    // The emitters iterate the name tables. A JSON key without an entry would
    // pass the checks but reach neither output.
    expectContractError(
      () => checkHeaders({ ...HEADERS, elevationFoo: 'Leapmux-Elevation-Foo' }),
      'has no HEADERS_GO_NAMES entry',
    )
  })

  it('rejects jitter at or above 1 on any policy', () => {
    expectContractError(
      () => checkRetry({ policies: { eventsRejection: { initialMs: 1, maxMs: 2, multiplier: 2, jitterFactor: 1, maxAttempts: 1 } } }),
      'jitterFactor',
    )
  })

  it('rejects a ceiling below the initial delay', () => {
    expectContractError(
      () => checkRetry({ policies: { eventsRejection: { initialMs: 5, maxMs: 2, multiplier: 2, jitterFactor: 0, maxAttempts: 1 } } }),
      '>= initialMs',
    )
  })

  it('rejects a policy with no name-table entry instead of emitting "undefined"', () => {
    // An unknown policy would emit `undefinedInitial` in Go and `undefined` in
    // TypeScript. Both outputs compile. The bijection makes generation fail.
    expectContractError(
      () => checkRetry({ policies: { connect: { initialMs: 1, maxMs: 2, multiplier: 2, jitterFactor: 0, maxAttempts: 1 } } }),
      'has no RETRY_GO_NAMES entry',
    )
  })

  it('rejects a name-table entry with no policy', () => {
    expectContractError(
      () => checkRetry({ policies: {} }),
      'matches no policies key',
    )
  })
})

describe('name tables', () => {
  it('maps every emitted wire value to distinct Go and TS names', () => {
    expect(new Set(Object.values(WIRE_GO_NAMES)).size).toBe(Object.keys(WIRE_GO_NAMES).length)
    expect(new Set(Object.values(WIRE_TS_NAMES)).size).toBe(Object.keys(WIRE_TS_NAMES).length)
  })

  it('maps every emitted header to distinct Go and TS names', () => {
    expect(new Set(Object.values(HEADERS_GO_NAMES)).size).toBe(Object.keys(HEADERS_GO_NAMES).length)
    expect(new Set(Object.values(HEADERS_TS_NAMES)).size).toBe(Object.keys(HEADERS_TS_NAMES).length)
  })

  it('identifies a retry policy for every TS object it emits', () => {
    expect(Object.keys(RETRY_GO_NAMES)).toEqual(Object.keys(RETRY_TS_NAMES))
  })
})

describe('emitters', () => {
  it('produces gofmt-stable const blocks (aligned pads)', () => {
    const go = emitGoWire(WIRE, deriveWire(WIRE))
    // gofmt aligns consecutive constants with padding. A misaligned block
    // makes `gofmt -l` report the generated file after each publication.
    expect(go).toContain('NoiseAEADAuthTagSize             = 16')
    expect(go).toContain('DefaultMaxReassembledMessageSize = 16842752')
  })

  it('emits the ws vocabulary and the noise/framing constants', () => {
    const go = emitGoWire(WIRE, deriveWire(WIRE))
    expect(go).toContain('WSRouteUserEvents            = "/ws/userevents"')
    expect(go).toContain('WSSubprotocolChannelRelay    = "channel-relay"')
    expect(go).toContain('WSParamWorkspaceIDs          = "workspace_ids"')
    expect(go).toContain('const SoftNonceLimit = uint64(2147483647)')
    expect(go).toContain('const LengthPrefixBytes = 4')
  })

  it('emits both Noise nonce limits, driven by the name tables', () => {
    // The hard limit was the last manual mirror in the Noise limit family.
    // Both languages now read one wire.json value. The emitters use the shared
    // Go and TypeScript name tables.
    const go = emitGoWire(WIRE, deriveWire(WIRE))
    expect(go).toContain('const HardNonceLimit = uint64(4294967295)')
    const ts = emitTsWire(WIRE, deriveWire(WIRE))
    expect(ts).toContain('export const SOFT_NONCE_LIMIT = 2147483647 as const')
    expect(ts).toContain('export const HARD_NONCE_LIMIT = 4294967295 as const')
  })

  it('emits every Tauri event the shell and webview spell, including the log and menu events', () => {
    // Both sides once stated `sidecar:log` and the two menu events manually.
    // A change on one side could disable a listener without a failure. The name
    // tables now contain all seven events.
    const d = readContract('desktop')
    const rs = emitRsDesktop(d)
    expect(rs).toContain('pub const EVENT_SIDECAR_LOG: &str = "sidecar:log"')
    expect(rs).toContain('pub const EVENT_MENU_SHOW_ABOUT: &str = "menu:show-about"')
    const ts = emitTsDesktop(d)
    expect(ts).toContain('TAURI_EVENT_SIDECAR_LOG = "sidecar:log"')
    expect(ts).toContain('TAURI_EVENT_MENU_SHOW_PREFERENCES = "menu:show-preferences"')
  })

  it('shields the macOS-only menu consts from dead_code off macOS', () => {
    // Only macOS code emits the menu events. Linux and Windows render the menu
    // in the webview. Their CI jobs therefore see these constants as unused.
    // Use `allow`, because `expect(dead_code)` would warn on macOS.
    const d = readContract('desktop')
    const rs = emitRsDesktop(d)
    expect(rs).toContain('#[cfg_attr(not(target_os = "macos"), allow(dead_code))]\npub const EVENT_MENU_SHOW_ABOUT: &str = "menu:show-about"')
    expect(rs).toContain('#[cfg_attr(not(target_os = "macos"), allow(dead_code))]\npub const EVENT_MENU_SHOW_PREFERENCES: &str = "menu:show-preferences"')
    expect(rs).not.toContain('#[cfg_attr(not(target_os = "macos"), allow(dead_code))]\npub const EVENT_CHANNEL_MESSAGE')
  })

  it('emits durations as const expressions, not runtime values', () => {
    const go = emitGoWire(WIRE, deriveWire(WIRE))
    expect(go).toContain('SessionKeyMaxAge      = time.Duration(3600000) * time.Millisecond')
    // This output must remain a constant. desktop/go/frame.go uses the channel
    // limits in the constant expression for maxFrameSize.
    expect(go).not.toContain('var ')
  })

  it('quotes strings as Go and TS both accept', () => {
    expect(emitGoHeaders(HEADERS)).toContain('"Leapmux-Elevation-Required"')
    // The TypeScript emitter uses JSON.stringify. It produces the double quotes
    // that the ESLint rules require for generated string literals.
    expect(emitTsHeaders(HEADERS)).toContain('"Leapmux-Elevation-Required"')
  })

  it('emits the retry policy as a spreadable TS object and Go consts', () => {
    const ts = emitTsRetry(RETRY)
    expect(ts).toContain('initialMs: 500')
    expect(ts).toContain('maxAttempts: 8')
    const go = emitGoRetry(RETRY)
    expect(go).toContain('EventsRejectionRetryInitial     = time.Duration(500) * time.Millisecond')
    expect(go).toContain('EventsRejectionRetryMaxAttempts = 8')
  })

  it('emits the multiplier on the Go side too, so both sides can consume it', () => {
    // Go once contained a fixed backoff multiplier. The contract must now send
    // the multiplier to Go and the browser.
    const go = emitGoRetry(RETRY)
    expect(go).toContain('EventsRejectionRetryMultiplier  = 2')
  })

  it('emits the wire protocol version both sides stamp on every envelope', () => {
    const go = emitGoWire(WIRE, deriveWire(WIRE))
    expect(go).toContain('const ProtocolVersion = 1')
    const ts = emitTsWire(WIRE, deriveWire(WIRE))
    expect(ts).toContain('export const PROTOCOL_VERSION = 1 as const')
  })

  it('emits the session refused-control table and the TS provider list', () => {
    const v = readContract('validate')
    expect(emitGoValidate(v)).toContain('var SessionRefusedControl = &unicode.RangeTable{')
    const p = readContract('providers')
    const agentEnum = enumValues(DESCRIPTOR, 'leapmux/v1/agent.proto', 'AgentProvider')
    const ts = emitTsProviders(p, agentEnum)
    expect(ts).toContain('export const ALL_PROVIDERS: readonly AgentProvider[] = [')
    // Keep Protocol Buffer order to match AllProviders in Go.
    expect(ts.indexOf('AgentProvider.CLAUDE_CODE')).toBeLessThan(ts.indexOf('AgentProvider.CODEX'))
    // The browser does not parse provider strings. The command-line interface
    // and admin remote procedure calls use the Go table. Do not emit an unused
    // TypeScript parse table.
    expect(ts).not.toContain('PROVIDER_PARSE_ALIASES')
  })

  it('is deterministic across runs', () => {
    const d = deriveWire(WIRE)
    for (const [again, once] of [
      [emitGoWire(WIRE, d), emitGoWire(WIRE, d)],
      [emitTsWire(WIRE, d), emitTsWire(WIRE, d)],
      [emitGoRetry(RETRY), emitGoRetry(RETRY)],
      [emitTsRetry(RETRY), emitTsRetry(RETRY)],
    ]) {
      expect(once).toBe(again)
    }
  })
})

// The provider-protocol domains define the wire vocabulary for two languages.
// Both languages dispatch on these literals. These checks prevent a table from
// reaching only one language. They also reject indistinguishable wire cases.
describe('checkProviderProtocol', () => {
  const spec = {
    name: 'test-protocol',
    tables: [{ key: 'events' }, { key: 'modes' }],
  }
  const ok = () => ({ events: { A: 'a' }, modes: { Plan: 'plan', Build: 'build' }, defaultMode: 'Build' })

  it('accepts a complete contract', () => {
    expect(checkProviderProtocol(spec, ok())).toEqual({})
  })

  it('rejects a declared table that is missing or empty', () => {
    const missing = ok()
    delete missing.events
    expectContractError(() => checkProviderProtocol(spec, missing), 'events is missing or empty')
    expectContractError(() => checkProviderProtocol(spec, { ...ok(), events: {} }), 'events is missing or empty')
  })

  // A JSON table outside PROVIDER_PROTOCOLS emits nothing. One language could
  // then read vocabulary that the other language lacks.
  it('rejects a table the spec does not declare', () => {
    expectContractError(
      () => checkProviderProtocol(spec, { ...ok(), decisions: { Allow: 'allow' } }),
      'table decisions is not declared',
    )
  })

  // Two keys with one literal make two dispatch cases identical on the wire.
  it('rejects a repeated literal inside one table', () => {
    expectContractError(
      () => checkProviderProtocol(spec, { ...ok(), events: { A: 'a', B: 'a' } }),
      'repeats the literal',
    )
  })

  it('allows the same literal in two DIFFERENT tables', () => {
    // `error` is both a tool-update kind and a stream kind in ZCode's own protocol.
    expect(checkProviderProtocol(spec, { ...ok(), events: { A: 'plan' } })).toEqual({})
  })

  it('rejects a defaultMode that names no mode', () => {
    expectContractError(
      () => checkProviderProtocol(spec, { ...ok(), defaultMode: 'Nope' }),
      'defaultMode "Nope" is not a key of modes',
    )
  })
})

describe('checkCodexBypass', () => {
  const valid = () => ({
    settings: [
      { id: 'permissionMode', value: 'never', planOption: false },
      { id: 'network_access', value: 'enabled', planOption: true },
    ],
  })

  it('accepts one permission mode and at least one plan option', () => {
    expect(checkCodexBypass(valid())).toBeUndefined()
  })

  it('rejects duplicate option ids', () => {
    const contract = valid()
    contract.settings.push({ id: 'network_access', value: 'restricted', planOption: true })
    expectContractError(() => checkCodexBypass(contract), 'two settings share one option id')
  })

  it('requires the permission mode and a plan option', () => {
    expectContractError(
      () => checkCodexBypass({ settings: [{ id: 'other', value: 'x', planOption: false }] }),
      'permissionMode is required',
    )
    expectContractError(
      () => checkCodexBypass({ settings: [{ id: 'permissionMode', value: 'never', planOption: false }] }),
      'at least one plan option is required',
    )
  })
})

// The worker and browser dialogs both use this pool for tab titles. The schema
// holds each title shape but cannot express these two relations.
describe('checkTabNames', () => {
  const ok = () => ({
    titlePrefixes: { agent: 'Agent', terminal: 'Terminal' },
    names: ['Ada', 'Bella', 'Gabe', 'Tim'],
  })

  it('accepts the shipped contract', () => {
    expect(checkTabNames(readContract('tab-names'))).toEqual({})
  })

  it('accepts a well-formed contract', () => {
    expect(checkTabNames(ok())).toEqual({})
  })

  // Equal prefixes collapse "Agent Gabe" and "Terminal Gabe" into one title.
  // Plan mode uses the agent prefix for automatic renames. It could then
  // overwrite terminal titles.
  it('rejects equal title prefixes', () => {
    expectContractError(
      () => checkTabNames({ ...ok(), titlePrefixes: { agent: 'Tab', terminal: 'Tab' } }),
      'title prefixes must differ',
    )
  })

  // The schema uses uniqueItems to find an exact repeat. Sorting also makes a
  // near-repeat visible during review.
  it('rejects an unsorted list', () => {
    expectContractError(
      () => checkTabNames({ ...ok(), names: ['Ada', 'Gabe', 'Bella', 'Tim'] }),
      'names must be sorted',
    )
  })

  it('rejects a repeated name, which sorting also surfaces', () => {
    expectContractError(
      () => checkTabNames({ ...ok(), names: ['Ada', 'Gabe', 'Gabe', 'Tim'] }),
      'names must be sorted',
    )
  })
})

describe('checkListen', () => {
  const ok = () => ({
    _readme: 'x',
    anyHost: '*',
    maxExtraAddresses: 8,
    addressSources: { listen: 'a', extra: 'b', merged: 'c' },
  })

  it('accepts the shipped shape', () => {
    expect(checkListen(ok())).toEqual({})
  })

  // listenset.Parse checks the wildcard token before an address or host name.
  // A sentinel that resembles a host would hide that host.
  it('rejects an anyHost spelled like a host', () => {
    expectContractError(() => checkListen({ ...ok(), anyHost: '0.0.0.0' }), 'is spelled like a host')
    expectContractError(() => checkListen({ ...ok(), anyHost: '::' }), 'is spelled like a host')
    expectContractError(() => checkListen({ ...ok(), anyHost: 'any' }), 'is spelled like a host')
    expectContractError(() => checkListen({ ...ok(), anyHost: 'fe80::1%en0' }), 'is spelled like a host')
  })

  it('rejects a cap that is not a positive integer', () => {
    expectContractError(() => checkListen({ ...ok(), maxExtraAddresses: 0 }), 'integer >= 1')
    expectContractError(() => checkListen({ ...ok(), maxExtraAddresses: 2.5 }), 'integer >= 1')
  })

  // A token without a name-table entry emits nothing. One side could compile
  // against a constant that the other side lacks.
  it('rejects a source token no name table renders', () => {
    expectContractError(
      () => checkListen({ ...ok(), addressSources: { ...ok().addressSources, proxied: 'd' } }),
      'has no LISTEN_SOURCE_GO_NAMES entry',
    )
  })

  it('rejects a name-table entry with no token', () => {
    expectContractError(
      () => checkListen({ ...ok(), addressSources: { listen: 'a', extra: 'b' } }),
      'matches no addressSources key',
    )
  })
})

describe('emitGoListen and emitTsListen', () => {
  const l = {
    _readme: 'x',
    anyHost: '*',
    maxExtraAddresses: 8,
    addressSources: { listen: 'a', extra: 'b', merged: 'c' },
  }

  it('emits the cap and the wildcard on both sides', () => {
    const go = emitGoListen(l)
    expect(go).toContain('ListenAnyHost = "*"')
    expect(go).toContain('MaxExtraListenAddresses = 8')
    expect(go).toContain('AddressSourceMerged = "merged"')

    const ts = emitTsListen(l)
    expect(ts).toContain('export const LISTEN_ANY_HOST = "*" as const')
    expect(ts).toContain('export const MAX_EXTRA_LISTEN_ADDRESSES = 8')
    expect(ts).toContain('export const ADDRESS_SOURCE_MERGED = "merged" as const')
  })

  it('is deterministic', () => {
    expect(emitGoListen(l)).toBe(emitGoListen(l))
    expect(emitTsListen(l)).toBe(emitTsListen(l))
  })
})

describe('trusted proxy contract', () => {
  it('accepts the shipped provider catalogue', () => {
    expect(checkTrustedProxies(TRUSTED_PROXIES)).toEqual({})
  })

  it('rejects a provider token that differs from its key', () => {
    expectContractError(
      () => checkTrustedProxies({
        ...TRUSTED_PROXIES,
        providers: {
          ...TRUSTED_PROXIES.providers,
          cloudflare: { ...TRUSTED_PROXIES.providers.cloudflare, token: 'other' },
        },
      }),
      'token must equal',
    )
  })

  it('emits the cap and provider metadata for both languages', () => {
    const go = emitGoTrustedProxies(TRUSTED_PROXIES)
    expect(go).toContain('const MaxTrustedProxySelectors = 32')
    expect(go).toContain('TrustedProxyProviderCloudflare = "cloudflare"')
    expect(go).toContain('{Token: "cloudfront", Label: "AWS CloudFront"')
    const ts = emitTsTrustedProxies(TRUSTED_PROXIES)
    expect(ts).toContain('MAX_TRUSTED_PROXY_SELECTORS = 32')
    expect(ts).toContain('{ token: "cloudflare", label: "Cloudflare"')
  })
})

describe('usernameConstName', () => {
  // The schema admits a hyphen and no language admits one in an identifier.
  it('folds a hyphen the identifier cannot carry', () => {
    expect(usernameConstName('solo')).toBe('USERNAME_SOLO')
    expect(usernameConstName('read-only')).toBe('USERNAME_READ_ONLY')
  })
})

describe('generate', () => {
  it('emits the shipped domains from the real contracts dir', () => {
    const files = generate(join(ROOT, 'contracts'), DESCRIPTOR)
    expect(Object.keys(files).sort()).toEqual([
      'backend/generated/contracts/captcha.go',
      'backend/generated/contracts/claude-protocol.go',
      'backend/generated/contracts/codex-bypass.go',
      'backend/generated/contracts/copilot-permissions.go',
      'backend/generated/contracts/desktop.go',
      'backend/generated/contracts/external-apps.go',
      'backend/generated/contracts/goose-protocol.go',
      'backend/generated/contracts/headers.go',
      'backend/generated/contracts/listen.go',
      'backend/generated/contracts/pi-protocol.go',
      'backend/generated/contracts/providers.go',
      'backend/generated/contracts/retry.go',
      'backend/generated/contracts/scopes.go',
      'backend/generated/contracts/session-info.go',
      'backend/generated/contracts/tab-names.go',
      'backend/generated/contracts/tab-types.go',
      'backend/generated/contracts/theme.go',
      'backend/generated/contracts/trusted-proxies.go',
      'backend/generated/contracts/validate.go',
      'backend/generated/contracts/wire.go',
      'backend/generated/contracts/worker-vocab.go',
      'backend/generated/contracts/zcode-protocol.go',
      'desktop/rust/src/generated/contracts.rs',
      'frontend/src/generated/contracts/captcha.ts',
      'frontend/src/generated/contracts/claude-protocol.ts',
      'frontend/src/generated/contracts/codex-bypass.ts',
      'frontend/src/generated/contracts/copilot-permissions.ts',
      'frontend/src/generated/contracts/desktop.ts',
      'frontend/src/generated/contracts/external-apps.ts',
      'frontend/src/generated/contracts/goose-protocol.ts',
      'frontend/src/generated/contracts/headers.ts',
      'frontend/src/generated/contracts/listen.ts',
      'frontend/src/generated/contracts/pi-protocol.ts',
      'frontend/src/generated/contracts/providers.ts',
      'frontend/src/generated/contracts/retry.ts',
      'frontend/src/generated/contracts/scopes.ts',
      'frontend/src/generated/contracts/session-info.ts',
      'frontend/src/generated/contracts/tab-names.ts',
      'frontend/src/generated/contracts/tab-types.ts',
      'frontend/src/generated/contracts/theme-default.ts',
      'frontend/src/generated/contracts/trusted-proxies.ts',
      'frontend/src/generated/contracts/validate.ts',
      'frontend/src/generated/contracts/wire.ts',
      'frontend/src/generated/contracts/worker-vocab.ts',
      'frontend/src/generated/contracts/zcode-protocol.ts',
    ])
  })

  // These two domains use the shared PROVIDER_PROTOCOLS path. These assertions
  // verify the output that their old custom emitters did not supply.
  it('emits the shared provider-protocol shape for the newest domains', () => {
    const files = generate(join(ROOT, 'contracts'), DESCRIPTOR)

    // Emit one TypeScript union for each table. The old Copilot emitter omitted it.
    const copilotTs = files['frontend/src/generated/contracts/copilot-permissions.ts']
    expect(copilotTs).toContain('export type CopilotPermissionGroup =')
    expect(copilotTs).toContain('export type CopilotPermissionValue =')
    // Preserve the identifiers that both languages import.
    expect(copilotTs).toContain('export const COPILOT_PERMISSION_GROUP = {')
    expect(files['backend/generated/contracts/copilot-permissions.go'])
      .toContain('CopilotPermissionGroupAssistedApproval = "copilot_assisted_approval"')
    // LeapMux owns one of the two Copilot identifiers. The domain therefore
    // overrides the shared header instead of assigning every value to the vendor.
    expect(files['backend/generated/contracts/copilot-permissions.go'])
      .not
      .toContain('vendor owns\n// the values')

    // The contract now supplies the Goose fallback mode to Go and TypeScript.
    expect(files['backend/generated/contracts/goose-protocol.go'])
      .toContain('GooseDefaultMode = GooseModeSmartApprove')
    expect(files['frontend/src/generated/contracts/goose-protocol.ts'])
      .toContain('export const GOOSE_DEFAULT_MODE = GOOSE_MODE.SmartApprove')

    // Claude modes were the last manual permission vocabulary in both languages.
    // The plugin and worker now read this table.
    expect(files['backend/generated/contracts/claude-protocol.go'])
      .toContain('ClaudeModeBypassPermissions = "bypassPermissions"')
    expect(files['frontend/src/generated/contracts/claude-protocol.ts'])
      .toContain('export const CLAUDE_DEFAULT_MODE = CLAUDE_MODE.Default')
  })

  it('fails loudly when a registered domain is missing its contract file', () => {
    // An incorrect contracts/<name>.json path once skipped its emitter.
    // sync-generated then removed the outputs as orphans. The compiler reported
    // the generated code instead of the missing contract. Report the contract
    // path during generation.
    const partial = mkdtempSync(join(tmpdir(), 'contracts-partial-'))
    cpSync(join(ROOT, 'contracts'), partial, { recursive: true })
    rmSync(join(partial, 'desktop.json'))
    expectContractError(() => generate(partial, DESCRIPTOR), 'desktop.json')
  })
})

describe('checkProviders', () => {
  const PROVIDERS = readContract('providers')
  const ENUM = ['AGENT_PROVIDER_UNSPECIFIED', 'AGENT_PROVIDER_CLAUDE_CODE', 'AGENT_PROVIDER_CODEX']

  it('accepts coverage of every non-UNSPECIFIED enum value', () => {
    const p = {
      providers: {
        AGENT_PROVIDER_CLAUDE_CODE: { displayName: 'Claude Code', cliAlias: 'claude-code', parseAliases: [] },
        AGENT_PROVIDER_CODEX: { displayName: 'Codex', cliAlias: 'codex', parseAliases: [] },
      },
    }
    expect(() => checkProviders(p, ENUM)).not.toThrow()
  })

  it('rejects a proto enum value with no entry', () => {
    const p = { providers: { AGENT_PROVIDER_CLAUDE_CODE: { displayName: 'X', cliAlias: 'x', parseAliases: [] } } }
    expectContractError(() => checkProviders(p, ENUM), 'AGENT_PROVIDER_CODEX has no entry')
  })

  it('rejects a provider whose prefix-stripped suffix is not a valid TS member', () => {
    // emitTsProviders inserts the suffix into AgentProvider.<suffix>. Protobuf
    // permits an initial digit, but TypeScript does not. Reject it before Go and
    // TypeScript produce different results.
    const p = { providers: { AGENT_PROVIDER_2FA_CODEX: { displayName: 'X', cliAlias: 'x', parseAliases: [] } } }
    expectContractError(() => checkProviders(p, ['AGENT_PROVIDER_UNSPECIFIED', 'AGENT_PROVIDER_2FA_CODEX']), 'not a valid TS member name')
  })

  it('rejects an entry whose enum value no longer exists', () => {
    const p = {
      providers: {
        AGENT_PROVIDER_CLAUDE_CODE: { displayName: 'X', cliAlias: 'x', parseAliases: [] },
        AGENT_PROVIDER_CODEX: { displayName: 'Y', cliAlias: 'y', parseAliases: [] },
        AGENT_PROVIDER_RETIRED: { displayName: 'Z', cliAlias: 'z', parseAliases: [] },
      },
    }
    expectContractError(() => checkProviders(p, ENUM), 'AGENT_PROVIDER_RETIRED')
  })

  it('rejects an alias claimed by two providers', () => {
    const p = {
      providers: {
        AGENT_PROVIDER_CLAUDE_CODE: { displayName: 'X', cliAlias: 'x', parseAliases: ['dup'] },
        AGENT_PROVIDER_CODEX: { displayName: 'Y', cliAlias: 'y', parseAliases: ['dup'] },
      },
    }
    expectContractError(() => checkProviders(p, ENUM), 'claimed by both')
  })

  it('rejects an alias repeated inside one provider (cliAlias duplicated in parseAliases)', () => {
    const p = {
      providers: {
        AGENT_PROVIDER_CLAUDE_CODE: { displayName: 'X', cliAlias: 'x', parseAliases: ['x'] },
        AGENT_PROVIDER_CODEX: { displayName: 'Y', cliAlias: 'y', parseAliases: [] },
      },
    }
    expectContractError(() => checkProviders(p, ENUM), 'claimed by both')
  })

  it('passes on the shipped contract against the real enum', () => {
    expect(() => checkProviders(PROVIDERS, enumValues(DESCRIPTOR, 'leapmux/v1/agent.proto', 'AgentProvider'))).not.toThrow()
  })
})

describe('checkScopes / checkTheme / checkValidate', () => {
  it('accepts the shipped contracts', () => {
    const d = DESCRIPTOR
    expect(() => checkScopes(readContract('scopes'), enumValues(d, 'leapmux/v1/scope.proto', 'Scope'))).not.toThrow()
    expect(() => checkTheme(readContract('theme-default'))).not.toThrow()
    expect(() => checkValidate(readContract('validate'))).not.toThrow()
  })

  it('rejects a proto enum value in neither scopes nor nonGrantable', () => {
    // The partition check covers every Protocol Buffer scope. Without it, a new
    // scope can pass generation but disappear from every user interface.
    const scopes = {
      SCOPE_ACCOUNT_READ: { token: 'account:read', description: 'd', consentSentence: 'c' },
    }
    expectContractError(() => checkScopes({
      nonGrantable: ['SCOPE_UNSPECIFIED'],
      scopes,
      categories: [{ label: 'Account', scopes: ['SCOPE_ACCOUNT_READ'] }],
      impliedBy: {},
    }, ['SCOPE_UNSPECIFIED', 'SCOPE_ACCOUNT_READ', 'SCOPE_NEW_UNCOVERED']), 'SCOPE_NEW_UNCOVERED')
  })

  it('rejects an oauthPage token missing from one palette variant', () => {
    const theme = {
      light: { '--background': 'a', '--accent': 'b' },
      dark: { '--background': 'c' },
      oauthPage: { tokens: ['--background', '--accent'], renames: {} },
    }
    expectContractError(() => checkTheme(theme), 'missing from the dark palette')
  })

  it('rejects a rename target no palette carries', () => {
    const theme = {
      light: { '--background': 'a' },
      dark: { '--background': 'c' },
      oauthPage: { tokens: ['--danger-subtle'], renames: { '--danger-subtle': '--lm-danger-subtle' } },
    }
    expectContractError(() => checkTheme(theme), '--lm-danger-subtle')
  })

  it('rejects an impliedBy cycle', () => {
    expectContractError(() => checkScopes({
      nonGrantable: [],
      scopes: { A: { token: 'a:x', description: 'd', consentSentence: 'c' }, B: { token: 'b:x', description: 'd', consentSentence: 'c' } },
      categories: [{ label: 'L', scopes: ['A', 'B'] }],
      impliedBy: { A: ['B'], B: ['A'] },
    }, ['A', 'B']), 'cycle')
  })

  it('rejects overlapping ranges in the validate contract', () => {
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[10, 20], [15, 30]], whitespaceFold: [[1, 2]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 1]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
    }), 'non-overlapping')
  })

  it('rejects an astral code point both emitters would mis-encode', () => {
    // tsClassSource emits four-digit Unicode escapes. An astral point becomes a
    // Basic Multilingual Plane escape plus a digit. goRangeTable also writes
    // uint16 fields. Reject astral points before the languages disagree.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[128545, 128545]], whitespaceFold: [[1, 2]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 1]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
    }), 'leaves the BMP')
  })

  it('rejects an astral refusedAscii code point', () => {
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[128545, 128545]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
    }), 'leaves the BMP')
  })

  it('rejects a refusedAscii range whose hi end alone is astral', () => {
    // The old guard checked only `lo`. Thus, [34, 128545] passed and emitted an
    // incorrect JavaScript range. Check both ends before the languages receive
    // different character sets.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[34, 128545]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
    }), 'leaves the BMP')
  })

  it('rejects an inverted refusedAscii range', () => {
    // An inverted entry makes the browser reject its character class. Go would
    // instead omit the rune. Reject the entry during generation.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[126, 34]] },
    }), 'inverted')
  })

  it('rejects a session file-path cap that is not larger than the token cap', () => {
    // These caps form one relation. A session file path contains a directory
    // prefix that a token lacks. The file-path cap must stay above the token cap.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 1, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      usernames: { systemReserved: {}, publicReserved: {} },
    }), 'filePathByteLimit must be > session.byteLimit')
  })

  it('rejects a reserved username the Go const mangle cannot spell', () => {
    // goReservedUsernames builds `Username<Name>` with case changes only. A
    // hyphen produces invalid Go. Generation must identify that username.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      usernames: { systemReserved: { 'ci-bot': 'why' }, publicReserved: {} },
    }), 'not a valid Go identifier')
  })

  it('rejects a session.invisibleFormat that diverges from the frozen name rule', () => {
    // This repetition is intentional. A change to the identifier rules must not
    // change the token rules. The generator checks both beside the data.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 3]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      usernames: { systemReserved: {}, publicReserved: {} },
    }), 'FROZEN')
  })

  it('rejects a rename for a page token the contract never lists', () => {
    // A rename outside oauthPage.tokens is unused data. The emitter iterates the
    // token list, so the page would omit the corresponding CSS variable.
    const theme = {
      light: { '--background': 'a', '--lm-danger-subtle': 'x' },
      dark: { '--background': 'c', '--lm-danger-subtle': 'y' },
      oauthPage: { tokens: ['--background'], renames: { '--danger-subtle': '--lm-danger-subtle' } },
    }
    expectContractError(() => checkTheme(theme), 'not in oauthPage.tokens')
  })

  it('rejects a scope whose prefix-stripped suffix is not a valid TS member', () => {
    // emitTsScopes inserts the suffix into Scope.<suffix>. Protocol Buffers
    // permit an initial digit, but TypeScript does not. Reject the value before
    // only one language fails.
    expectContractError(() => checkScopes({
      nonGrantable: ['SCOPE_UNSPECIFIED'],
      scopes: { SCOPE_2FA: { token: 'account:2fa', description: 'd', consentSentence: 'c' } },
      categories: [{ label: 'Account', scopes: ['SCOPE_2FA'] }],
      impliedBy: {},
    }, ['SCOPE_UNSPECIFIED', 'SCOPE_2FA']), 'not a valid TS member name')
  })
})

describe('subtractRanges / nameStripClass', () => {
  it('removes the whitespace folds from Cc, keeping the format set whole', () => {
    const v = readContract('validate')
    const cls = nameStripClass(v)
    // Freeze the browser class from before contracts existed. It contains C0
    // without the fold block, C1 without the Next Line control, and the format
    // characters. Only the format half receives annotations from the contract.
    expect(cls).toEqual([
      [0, 8],
      [14, 31],
      [127, 132],
      [134, 159],
      [173, 173, 'SOFT HYPHEN'],
      [1564, 1564, 'ARABIC LETTER MARK'],
      [6158, 6158, 'MONGOLIAN VOWEL SEPARATOR'],
      [8203, 8203, 'ZERO WIDTH SPACE'],
      [8206, 8207, 'LEFT-TO-RIGHT MARK, RIGHT-TO-LEFT MARK'],
      [8234, 8238, 'LEFT-TO-RIGHT EMBEDDING, RIGHT-TO-LEFT EMBEDDING, POP DIRECTIONAL FORMATTING, LEFT-TO-RIGHT OVERRIDE, RIGHT-TO-LEFT OVERRIDE'],
      [8288, 8288, 'WORD JOINER'],
      [8294, 8297, 'LEFT-TO-RIGHT ISOLATE, RIGHT-TO-LEFT ISOLATE, FIRST-STRONG ISOLATE, POP DIRECTIONAL ISOLATE'],
      [65279, 65279, 'ZERO WIDTH NO-BREAK SPACE'],
    ])
  })

  it('emits the range annotations on both sides: Go trailing comments, TS companion blocks', () => {
    const v = readContract('validate')
    const go = emitGoValidate(v)
    expect(go).toContain('{Lo: 0x00AD, Hi: 0x00AD, Stride: 1}, // SOFT HYPHEN')
    expect(go).toContain('{Lo: 0x0009, Hi: 0x000D, Stride: 1}, // TAB, LF, VERTICAL TAB, FORM FEED, CARRIAGE RETURN')
    // The contract also supplies the labels for refused ASCII runes. The Go
    // comments therefore stay consistent with the JSON.
    expect(go).toContain('var SessionRefusedASCII = []rune{0x0022, 0x0024, 0x0025, 0x005C} // QUOTATION MARK, DOLLAR SIGN, PERCENT SIGN, REVERSE SOLIDUS')
    const ts = emitTsValidate(v)
    // TypeScript emits the same labels in a comment above each class constant.
    // The class body contains only regular-expression syntax.
    expect(ts).toContain('*   \\u00ad  SOFT HYPHEN')
    expect(ts).toContain('*   \\u0022  QUOTATION MARK')
    const classLine = ts.split('\n').find(l => l.includes('SESSION_FORBIDDEN_CLASS ='))
    expect(classLine).not.toContain('SOFT HYPHEN')
    expect(ts).toContain('NAME_INVISIBLE_CLASS')
  })

  it('emits the reserved usernames as consts, lookup maps, and TS arrays', () => {
    const v = readContract('validate')
    const go = emitGoValidate(v)
    expect(go).toContain('UsernameSolo = "solo"')
    expect(go).toContain('UsernameAdmin = "admin"')
    expect(go).toContain('var UsernamesSystemReserved = map[string]bool{')
    const ts = emitTsValidate(v)
    expect(ts).toContain('SYSTEM_RESERVED_USERNAMES: readonly string[] = ["solo"]')
    expect(ts).toContain('PUBLIC_RESERVED_USERNAMES: readonly string[] = ["admin"]')
  })
})

describe('checkSessionInfo', () => {
  /** A minimal but complete contract; each test perturbs one table. */
  function sessionInfo(overrides = {}) {
    return {
      keys: { TotalCostUsd: 'total_cost_usd', ThinkingTokens: 'thinking_tokens', RunningTool: 'running_tool' },
      contextUsageFields: { InputTokens: 'input_tokens' },
      rateLimitFields: { Status: 'status' },
      runningToolFields: { SpanId: 'span_id' },
      runningToolRetryFields: { Attempt: 'attempt' },
      ...overrides,
    }
  }

  it('accepts the shipped contract', () => {
    expect(() => checkSessionInfo(readContract('session-info'))).not.toThrow()
  })

  it('rejects two entries of one table sharing a wire token', () => {
    expectContractError(
      () => checkSessionInfo(sessionInfo({ keys: { A: 'same_token', B: 'same_token' } })),
      'two keys entries share one wire token',
    )
  })

  it('rejects a duplicate token in a NESTED table too', () => {
    expectContractError(
      () => checkSessionInfo(sessionInfo({ rateLimitFields: { A: 'status', B: 'status' } })),
      'two rateLimitFields entries share one wire token',
    )
  })

  it('accepts the same token in two DIFFERENT tables', () => {
    // Fields in different objects can use the same key. Only a repeated key in
    // one object is ambiguous. Do not reject a key across separate tables.
    expect(() => checkSessionInfo(sessionInfo({
      runningToolFields: { ToolName: 'tool_name' },
      rateLimitFields: { ToolName: 'tool_name' },
    }))).not.toThrow()
  })

  it('rejects an empty table rather than emitting an empty const block', () => {
    // goConstBlock applies Math.max to the list. An empty list produces
    // negative infinity and invalid Go. Reject it before emission.
    expectContractError(
      () => checkSessionInfo(sessionInfo({ contextUsageFields: {} })),
      'contextUsageFields must hold at least one entry',
    )
  })

  // LeapMux does not own this token. Claude Code writes `total_cost_usd` in its
  // result. The worker stores it unchanged. The browser uses this constant, but
  // Go uses a literal struct tag. This assertion prevents a one-sided rename.
  it('rejects a rename of the cost token Claude Code itself writes', () => {
    expectContractError(
      () => checkSessionInfo(sessionInfo({
        keys: { TotalCostUsd: 'cost_usd', ThinkingTokens: 'thinking_tokens', RunningTool: 'running_tool' },
      })),
      'Claude Code writes that spelling on its own result line',
    )
  })

  // A table outside SESSION_INFO_TABLES emits no Go or TypeScript. The compiler
  // would report an undefined constant without identifying the contract. Make
  // generation report the missing registration.
  it('rejects a contract table that no SESSION_INFO_TABLES entry renders', () => {
    expectContractError(
      () => checkSessionInfo(sessionInfo({ compactionFields: { PreTokens: 'pre_tokens' } })),
      'compactionFields has no SESSION_INFO_TABLES entry',
    )
  })

  it('emits one Go constant and one TS entry per table entry', () => {
    const s = readContract('session-info')
    const go = emitGoSessionInfo(s)
    const ts = emitTsSessionInfo(s)
    for (const table of SESSION_INFO_TABLES) {
      for (const [name, token] of Object.entries(s[table.json])) {
        expect(go).toContain(`${table.goPrefix}${name}`)
        expect(go).toContain(JSON.stringify(token))
        expect(ts).toContain(`${name}: ${JSON.stringify(token)},`)
      }
      expect(ts).toContain(`export const ${table.ts} = {`)
      expect(ts).toContain(`export type ${table.tsType} =`)
    }
  })

  it('emits the running_tool vocabulary the Claude tool_progress path needs', () => {
    // The badge reads these fields from the wire. A one-sided rename can stop
    // its updates. Use a regular expression because goConstBlock aligns each
    // equals sign with the longest identifier.
    const go = emitGoSessionInfo(readContract('session-info'))
    for (const [name, token] of [
      ['SessionInfoKeyRunningTool', 'running_tool'],
      ['RunningToolFieldSpanId', 'span_id'],
      ['RunningToolFieldElapsedSeconds', 'elapsed_seconds'],
      ['RunningToolFieldRetry', 'retry'],
      ['RunningToolRetryFieldMaxRetries', 'max_retries'],
    ]) {
      expect(go).toMatch(new RegExp(`\\b${name} += "${token}"$`, 'm'))
    }
  })
})

describe('checkWorkerVocab / checkDesktop', () => {
  // Supply a valid windowBehavior block to each negative desktop fixture. Each
  // fixture must fail with its expected ContractError, not an unrelated
  // TypeError after check order changes.
  const behavior = () => ({
    trayOnClose: { tray: 'tray', quit: 'quit' },
    trayOnMinimize: { tray: 'tray', taskbar: 'taskbar' },
    startMinimized: { window: 'window', minimized: 'minimized' },
  })

  // Supply valid launchVisibility and windowMode blocks too. The later checks
  // must remain reachable without a TypeError.
  const launch = () => ({
    normal: 'normal',
    minimized: 'minimized',
    hidden: 'hidden',
  })
  const windowMode = () => ({
    normal: 'normal',
    maximized: 'maximized',
    fullscreen: 'fullscreen',
  })

  // Supply every event when a fixture must pass the tauriEvents coverage check.
  // Older four-event fixtures fail before they reach that check.
  const events = () => ({
    channelMessage: 'c:m',
    channelClose: 'c:c',
    userEventsMessage: 'u:m',
    userEventsClose: 'u:c',
    sidecarLog: 's:l',
    menuShowAbout: 'm:a',
    menuShowPreferences: 'm:p',
  })

  it('rejects a missing or empty DEV frontend URL', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      tauriEvents: events(),
      windowBehavior: behavior(),
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'devFrontendUrl must be a non-empty URL string')
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: '',
      tauriEvents: events(),
      windowBehavior: behavior(),
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'devFrontendUrl must be a non-empty URL string')
  })

  it('rejects a worker-authored type that is not a notification type', () => {
    expectContractError(() => checkWorkerVocab({
      notificationTypes: { AgentError: 'agent_error' },
      workerAuthoredNotificationTypes: ['NotAType'],
      notificationThreadWrapperType: 'notification_thread',
      codexRateLimitReachedTimeWindow: 'rate_limit_reached',
      modelSentinels: { accountDefaultModel: 'default', effortAuto: 'auto' },
    }), 'not a notificationTypes key')
  })

  it('rejects two notification types sharing one token', () => {
    expectContractError(() => checkWorkerVocab({
      notificationTypes: { A: 'same_token', B: 'same_token' },
      workerAuthoredNotificationTypes: ['A'],
      notificationThreadWrapperType: 'notification_thread',
      codexRateLimitReachedTimeWindow: 'rate_limit_reached',
      modelSentinels: { accountDefaultModel: 'default', effortAuto: 'auto' },
    }), 'share one wire token')
  })

  it('rejects a thread-wrapper token that collides with a notification type', () => {
    // The browser routes thread probes through NOTIFICATION_THREAD_TYPE. An
    // equal notification token would enter the wrong processing case.
    expectContractError(() => checkWorkerVocab({
      notificationTypes: { AgentError: 'notification_thread' },
      workerAuthoredNotificationTypes: ['AgentError'],
      notificationThreadWrapperType: 'notification_thread',
      codexRateLimitReachedTimeWindow: 'rate_limit_reached',
      modelSentinels: { accountDefaultModel: 'default', effortAuto: 'auto' },
    }), 'collides with a notificationTypes token')
  })

  it('rejects model sentinels that collide', () => {
    expectContractError(() => checkWorkerVocab({
      notificationTypes: { AgentError: 'agent_error' },
      workerAuthoredNotificationTypes: ['AgentError'],
      notificationThreadWrapperType: 'notification_thread',
      codexRateLimitReachedTimeWindow: 'rate_limit_reached',
      modelSentinels: { accountDefaultModel: 'same', effortAuto: 'same' },
    }), 'sentinels must be distinct')
  })

  it('rejects two Tauri events sharing one name', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: { channelMessage: 'same:event', channelClose: 'same:event', userEventsMessage: 'u:m', userEventsClose: 'u:c' },
      windowBehavior: behavior(),
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'share one name')
  })

  it('rejects two desktop env vars sharing one name', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'SAME_X', binaryHash: 'SAME_X', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: events(),
      windowBehavior: behavior(),
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'two env vars share one name')
  })

  it('rejects a desktop value with no name-table entry instead of emitting nothing', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z', extra: 'D_W' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: { channelMessage: 'c:m', channelClose: 'c:c', userEventsMessage: 'u:m', userEventsClose: 'u:c' },
      windowBehavior: behavior(),
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'has no DESKTOP_GO_ENV_NAMES entry')
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: { channelMessage: 'c:m', channelClose: 'c:c', userEventsMessage: 'u:m', userEventsClose: 'u:c', extra: 'e:x' },
      windowBehavior: behavior(),
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'has no DESKTOP_RS_EVENT_NAMES entry')
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: events(),
      windowBehavior: { ...behavior(), extra: { one: 'x', two: 'y' } },
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'has no DESKTOP_GO_BEHAVIOR_NAMES entry')
  })

  it('rejects a macOS-only event key that no name table carries', () => {
    // An incorrect DESKTOP_RS_MACOS_ONLY_EVENTS key would omit its annotation.
    // Clippy would then report the constant outside macOS.
    DESKTOP_RS_MACOS_ONLY_EVENTS.add('noSuchEvent')
    try {
      expectContractError(() => checkDesktop({
        envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
        devFrontendUrl: 'http://localhost:4328',
        tauriEvents: { channelMessage: 'c:m', channelClose: 'c:c', userEventsMessage: 'u:m', userEventsClose: 'u:c' },
        windowBehavior: behavior(),
      }), 'missing from DESKTOP_RS_EVENT_NAMES')
    }
    finally {
      DESKTOP_RS_MACOS_ONLY_EVENTS.delete('noSuchEvent')
    }
  })

  it('rejects one setting whose two tokens are the same string', () => {
    // Equal tokens give the pill group two options that store one value.
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: events(),
      windowBehavior: { ...behavior(), trayOnClose: { tray: 'tray', quit: 'tray' } },
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'windowBehavior.trayOnClose declares one token twice')
  })

  // The data now supplies the groups. Therefore, the checker can process a new
  // setting before its name-table entry exists. The old checker table skipped
  // that setting.
  it('checks token uniqueness for a setting no name table knows yet', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: events(),
      windowBehavior: { ...behavior(), closeToDock: { dock: 'dock', tray: 'dock' } },
      launchVisibility: launch(),
      windowMode: windowMode(),
    }), 'windowBehavior.closeToDock declares one token twice')
  })

  it('accepts one token shared by two DIFFERENT settings', () => {
    // The shipped contract uses `tray` for close-to-tray and minimize-to-tray.
    // Check uniqueness inside each setting, not across the full block.
    const d = readContract('desktop')
    expect(Object.keys(d.windowBehavior)).toEqual(['trayOnClose', 'trayOnMinimize', 'startMinimized'])
    expect(d.windowBehavior.trayOnClose.tray).toBe(d.windowBehavior.trayOnMinimize.tray)
    expect(() => checkDesktop(d)).not.toThrow()
  })

  // launchVisibility is one choice, unlike windowBehavior. Each token must
  // differ. Equal wire values would make `parseLaunchVisibility` return the
  // wrong state.
  it('rejects two launch-visibility states sharing one token', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: events(),
      windowBehavior: behavior(),
      launchVisibility: { ...launch(), hidden: 'normal' },
      windowMode: windowMode(),
    }), 'launchVisibility declares one token twice')
  })

  it('rejects a launch-visibility key with no name-table entry', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: events(),
      windowBehavior: behavior(),
      launchVisibility: { ...launch(), extra: 'x' },
      windowMode: windowMode(),
    }), 'has no DESKTOP_RS_LAUNCH_NAMES entry')
  })

  // windowMode is one choice in three languages. Go stores the token, Rust
  // reads it at launch, and the webview reads and writes it. The contract
  // replaces three manual copies.
  it('rejects two window modes sharing one token', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', devFrontend: 'C_Z' },
      devFrontendUrl: 'http://localhost:4328',
      tauriEvents: events(),
      windowBehavior: behavior(),
      launchVisibility: launch(),
      windowMode: { ...windowMode(), fullscreen: 'maximized' },
    }), 'windowMode declares one token twice')
  })

  it('emits the window-mode tokens to all three languages', () => {
    const d = readContract('desktop')
    expect(emitGoDesktop(d)).toMatch(/WindowModeMaximized += "maximized"/)
    expect(emitRsDesktop(d)).toContain('pub const WINDOW_MODE_FULLSCREEN: &str = "fullscreen"')
    expect(emitTsDesktop(d)).toContain('export const WINDOW_MODE_NORMAL = "normal" as const')
  })

  // Rust writes these values through `LaunchVisibility::as_str`. The webview
  // reads them through `parseLaunchVisibility`. An unknown value falls back to
  // `normal`, so a one-sided change can hide a window without an error.
  it('emits the launch-visibility tokens for both Rust and TS', () => {
    const d = readContract('desktop')
    expect(emitRsDesktop(d)).toContain('pub const LAUNCH_VISIBILITY_HIDDEN: &str = "hidden"')
    expect(emitTsDesktop(d)).toContain('export const LAUNCH_VISIBILITY_HIDDEN = "hidden" as const')
  })

  it('emits the Rust module without inner doc comments (include! cannot take them)', () => {
    const d = readContract('desktop')
    const rs = emitRsDesktop(d)
    expect(rs).toContain('pub const ENV_DEV_ENDPOINT: &str = "LEAPMUX_DESKTOP_DEV_ENDPOINT"')
    expect(rs).toContain('pub const ENV_DEV_FRONTEND: &str = "LEAPMUX_HUB_DEV_FRONTEND"')
    expect(rs).toContain('pub const EVENT_CHANNEL_MESSAGE: &str = "channel:message"')
    expect(rs).not.toContain('//!')
  })

  it('emits the hub DevFrontend env var to Go and Rust', () => {
    // The debug sidecar writes this value. Solo mode reads it into
    // `-dev-frontend`. A one-sided change leaves TCP extras on the embedded
    // single-page application.
    const d = readContract('desktop')
    expect(d.envVars.devFrontend).toBe('LEAPMUX_HUB_DEV_FRONTEND')
    expect(emitGoDesktop(d)).toContain('EnvDevFrontend = "LEAPMUX_HUB_DEV_FRONTEND"')
    expect(emitRsDesktop(d)).toContain('pub const ENV_DEV_FRONTEND: &str = "LEAPMUX_HUB_DEV_FRONTEND"')
  })

  it('emits the frame cap to Go and Rust from one contract value', () => {
    // The frame budget once had three manual copies. One contract value now
    // supplies the Go and Rust outputs.
    const d = readContract('desktop')
    expect(emitGoDesktop(d)).toContain(`const MaxFrameSizeBytes = ${d.maxFrameSizeBytes}`)
    expect(emitRsDesktop(d)).toContain(`pub const MAX_FRAME_SIZE_BYTES: u64 = ${d.maxFrameSizeBytes};`)
  })

  it('emits the DEV frontend URL to Go and Rust from one contract value', () => {
    // The debug webview and sidecar DevProxy must share this origin. A one-sided
    // change sends Network Access extras to the wrong Vite port.
    const d = readContract('desktop')
    expect(d.devFrontendUrl).toBe('http://localhost:4328')
    expect(emitGoDesktop(d)).toContain('DevFrontendURL = "http://localhost:4328"')
    expect(emitRsDesktop(d)).toContain('pub const DEV_FRONTEND_URL: &str = "http://localhost:4328";')
  })

  it('emits the window-behaviour tokens to all three languages from one contract value', () => {
    // Three languages use this setting family. These outputs keep the hub
    // validator, webview parser, and shell matcher on the same strings.
    const d = readContract('desktop')
    const go = emitGoDesktop(d)
    const ts = emitTsDesktop(d)
    const rs = emitRsDesktop(d)
    // The name tables use flat `<setting><Value>` keys. The contract keeps one
    // nested object for each setting. Only emitted identifiers remain flat.
    const flat = {
      trayOnCloseTray: d.windowBehavior.trayOnClose.tray,
      trayOnCloseQuit: d.windowBehavior.trayOnClose.quit,
      trayOnMinimizeTray: d.windowBehavior.trayOnMinimize.tray,
      trayOnMinimizeTaskbar: d.windowBehavior.trayOnMinimize.taskbar,
      startMinimizedWindow: d.windowBehavior.startMinimized.window,
      startMinimizedMinimized: d.windowBehavior.startMinimized.minimized,
    }
    for (const [key, token] of Object.entries(flat)) {
      // goConstBlock aligns the identifiers in a column. Match the separator
      // without depending on that spacing.
      expect(go).toMatch(new RegExp(`${DESKTOP_GO_BEHAVIOR_NAMES[key]} += "${token}"`))
      expect(ts).toContain(`export const ${DESKTOP_TS_BEHAVIOR_NAMES[key]} = "${token}" as const`)
      expect(rs).toContain(`pub const ${DESKTOP_RS_BEHAVIOR_NAMES[key]}: &str = "${token}";`)
    }
  })

  it('gives every window-behaviour key a distinct name in each language', () => {
    // A repeated table entry emits one constant twice and omits another token.
    // The coverage check cannot find this because both keys exist.
    for (const table of [DESKTOP_GO_BEHAVIOR_NAMES, DESKTOP_RS_BEHAVIOR_NAMES, DESKTOP_TS_BEHAVIOR_NAMES]) {
      const names = Object.values(table)
      expect(new Set(names).size).toBe(names.length)
    }
  })
})

describe('enumValues', () => {
  it('reads enum names from a buf descriptor, reserved values absent', () => {
    // Use the real descriptor. Reserved slot 3 must stay absent. Otherwise, the
    // provider check requests metadata for a provider that cannot exist.
    const set = DESCRIPTOR
    const names = enumValues(set, 'leapmux/v1/agent.proto', 'AgentProvider')
    expect(names).toContain('AGENT_PROVIDER_CLAUDE_CODE')
    expect(names.filter(n => n.includes('RESERVED'))).toEqual([])
    expect(names).not.toContain('AGENT_PROVIDER_3')
  })

  it('fails with a ContractError for an unknown enum or file', () => {
    expectContractError(() => enumValues({ file: [] }, 'nope.proto', 'X'), 'not found')
  })
})

// The id vocabulary was hand-written twice before this contract -- the three
// Go spec tables and the browser's icon table -- paired only by a comment.
describe('checkExternalApps', () => {
  const KINDS = [
    'EXTERNAL_APP_KIND_UNSPECIFIED',
    'EXTERNAL_APP_KIND_EDITOR',
    'EXTERNAL_APP_KIND_FILE_MANAGER',
  ]

  function contract(overrides = {}) {
    return {
      _readme: 'x',
      kinds: {
        EXTERNAL_APP_KIND_EDITOR: 'an editor',
        EXTERNAL_APP_KIND_FILE_MANAGER: 'the file manager',
      },
      apps: {
        'file-manager': { kind: 'EXTERNAL_APP_KIND_FILE_MANAGER', oses: ['darwin', 'linux', 'windows'] },
        'vscode': { kind: 'EXTERNAL_APP_KIND_EDITOR', oses: ['darwin', 'linux', 'windows'] },
      },
      ...overrides,
    }
  }

  it('accepts a contract that covers every kind and every OS', () => {
    expect(() => checkExternalApps(contract(), KINDS)).not.toThrow()
  })

  it('rejects a proto kind with no contract entry', () => {
    expect(() => checkExternalApps(contract(), [...KINDS, 'EXTERNAL_APP_KIND_NOTEBOOK']))
      .toThrow(/EXTERNAL_APP_KIND_NOTEBOOK has no kinds entry/)
  })

  it('rejects a kinds entry the proto no longer carries', () => {
    const c = contract()
    c.kinds.EXTERNAL_APP_KIND_GONE = 'removed'
    expect(() => checkExternalApps(c, KINDS)).toThrow(/matches no ExternalAppKind enum value/)
  })

  it('rejects the unset value as a kinds entry', () => {
    const c = contract()
    c.kinds.EXTERNAL_APP_KIND_UNSPECIFIED = 'nothing'
    expect(() => checkExternalApps(c, KINDS)).toThrow(/is the unset value/)
  })

  it('rejects an app whose kind is not a kinds entry', () => {
    const c = contract()
    c.apps.vscode.kind = 'EXTERNAL_APP_KIND_MYSTERY'
    expect(() => checkExternalApps(c, KINDS)).toThrow(/carries kind EXTERNAL_APP_KIND_MYSTERY/)
  })

  it('rejects a kind no app carries, because the menu can never show it', () => {
    const c = contract()
    delete c.apps['file-manager']
    expect(() => checkExternalApps(c, KINDS)).toThrow(/is carried by no app/)
  })

  // The app menu renders the file manager as its own always-present group, so
  // a platform without one leaves that group empty and two make it a choice.
  it('rejects an OS with no file manager', () => {
    const c = contract()
    c.apps['file-manager'].oses = ['darwin', 'windows']
    expect(() => checkExternalApps(c, KINDS)).toThrow(/linux must carry exactly one/)
  })

  it('rejects an OS with two file managers', () => {
    const c = contract()
    c.apps.finder = { kind: 'EXTERNAL_APP_KIND_FILE_MANAGER', oses: ['darwin'] }
    expect(() => checkExternalApps(c, KINDS)).toThrow(/darwin must carry exactly one/)
  })
})

describe('emitGoExternalApps and emitTsExternalApps', () => {
  const c = {
    _readme: 'x',
    kinds: {
      EXTERNAL_APP_KIND_EDITOR: 'an editor',
      EXTERNAL_APP_KIND_FILE_MANAGER: 'the file manager',
    },
    apps: {
      'file-manager': { kind: 'EXTERNAL_APP_KIND_FILE_MANAGER', oses: ['darwin', 'linux', 'windows'] },
      'vscode': { kind: 'EXTERNAL_APP_KIND_EDITOR', oses: ['darwin', 'linux', 'windows'] },
      'xcode': { kind: 'EXTERNAL_APP_KIND_EDITOR', oses: ['darwin'] },
    },
  }

  it('maps every id to its proto enum value', () => {
    const go = emitGoExternalApps(c)
    expect(go).toContain('"vscode":       desktopv1.ExternalAppKind_EXTERNAL_APP_KIND_EDITOR,')
    expect(go).toContain('"file-manager": desktopv1.ExternalAppKind_EXTERNAL_APP_KIND_FILE_MANAGER,')
  })

  it('lists each OS only the ids that OS carries', () => {
    const go = emitGoExternalApps(c)
    const linux = go.slice(go.indexOf('"linux": {'), go.indexOf('"windows": {'))
    expect(linux).toContain('"vscode"')
    expect(linux).not.toContain('"xcode"')
    expect(go.slice(go.indexOf('"darwin": {'), go.indexOf('"linux": {'))).toContain('"xcode"')
  })

  // Both sides must agree on the vocabulary; that agreement is the whole
  // reason the table left the two hand-written copies.
  it('emits the same id set to both languages', () => {
    const ts = emitTsExternalApps(c)
    for (const id of Object.keys(c.apps)) {
      expect(ts).toContain(`"${id}",`)
      expect(emitGoExternalApps(c)).toContain(`"${id}"`)
    }
    expect(ts).toContain('export type ExternalAppId')
  })

  it('is deterministic', () => {
    expect(emitGoExternalApps(c)).toBe(emitGoExternalApps(c))
    expect(emitTsExternalApps(c)).toBe(emitTsExternalApps(c))
  })
})
