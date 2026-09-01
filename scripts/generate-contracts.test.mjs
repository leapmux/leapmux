// Tests for generate-contracts.mjs, run by `bun test` via `task test-scripts`.
//
// The checks and emitters are pure functions, so every failure mode is
// testable without staging or buf. What carries the risk:
//   - the derivation arithmetic (a wrong sum here is a wrong wire limit
//     everywhere, silently),
//   - the name tables (a mangled or duplicated entry means one side compiles
//     against a constant the other side never got),
//   - determinism (non-deterministic output defeats the mtime-preserving
//     publish and makes every `task generate` a Vite hard-refresh).
// The generate() orchestration is tested against the REAL contracts/ dir,
// the same way validate-json.test.mjs pins its rule table to the real tree.

import { cpSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

import {
  bufDescriptor,
  checkDesktop,
  checkHeaders,
  checkProviderProtocol,
  checkProviders,
  checkRetry,
  checkScopes,
  checkTabNames,
  checkTheme,
  checkValidate,
  checkWire,
  checkWorkerVocab,
  ContractError,
  deriveWire,
  DESKTOP_RS_MACOS_ONLY_EVENTS,
  emitGoDesktop,
  emitGoHeaders,
  emitGoRetry,
  emitGoValidate,
  emitGoWire,
  emitRsDesktop,
  emitTsDesktop,
  emitTsHeaders,
  emitTsProviders,
  emitTsRetry,
  emitTsValidate,
  emitTsWire,
  enumValues,
  generate,
  HEADERS_GO_NAMES,
  HEADERS_TS_NAMES,
  nameStripClass,
  RETRY_GO_NAMES,
  RETRY_TS_NAMES,
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
    // The frozen pre-contract values: Go wire.go / rekey.go and TS
    // reassembler.ts / channelSession.ts. If a migration changed a number,
    // this is the line that goes red and forces the retune to be deliberate.
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
    // A key listed in a table but missing from the flattened object renders
    // as the literal "undefined" in generated code. The coverage check turns
    // that into a ContractError naming the key.
    expectContractError(
      () => checkWire({ ...WIRE, protocolVersion: undefined }),
      'name table',
    )
  })

  it('rejects a hard nonce limit at or below the soft limit', () => {
    // The soft trigger must fire before the wrap bound, or the session
    // rekeys only once it has already passed the point of refusal.
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
    // The emitters iterate the name tables, so a JSON key without an entry
    // would pass every check and never be emitted on either side.
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
    // A policy the name tables do not know would emit Go consts named
    // undefinedInitial and a TS export literally named "undefined" -- both
    // compile. The bijection makes it a loud generation failure.
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
    // gofmt aligns consecutive const values by padding to the longest name;
    // a misaligned block means `gofmt -l` flags the generated file and the
    // publish turns into a permanent diff.
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
    // The hard limit was the last hand-restated two-language mirror of the
    // Noise limit family; both sides now read one wire.json value, and the
    // emitters route the names through WIRE_GO_NAMES/WIRE_TS_NAMES like
    // every other wire constant.
    const go = emitGoWire(WIRE, deriveWire(WIRE))
    expect(go).toContain('const HardNonceLimit = uint64(4294967295)')
    const ts = emitTsWire(WIRE, deriveWire(WIRE))
    expect(ts).toContain('export const SOFT_NONCE_LIMIT = 2147483647 as const')
    expect(ts).toContain('export const HARD_NONCE_LIMIT = 4294967295 as const')
  })

  it('emits every Tauri event the shell and webview spell, including the log and menu events', () => {
    // sidecar:log and the two menu events stayed hand-spelled on both sides
    // after the first four were contracted; a one-side rename of any of the
    // seven killed a listener with nothing red. The name tables carry all of
    // them now.
    const d = readContract('desktop')
    const rs = emitRsDesktop(d)
    expect(rs).toContain('pub const EVENT_SIDECAR_LOG: &str = "sidecar:log"')
    expect(rs).toContain('pub const EVENT_MENU_SHOW_ABOUT: &str = "menu:show-about"')
    const ts = emitTsDesktop(d)
    expect(ts).toContain('TAURI_EVENT_SIDECAR_LOG = "sidecar:log"')
    expect(ts).toContain('TAURI_EVENT_MENU_SHOW_PREFERENCES = "menu:show-preferences"')
  })

  it('shields the macOS-only menu consts from dead_code off macOS', () => {
    // The menu events are emitted solely from #[cfg(target_os = "macos")]
    // code (the native app menu; Linux and Windows render the menu in the
    // webview), so `cargo clippy --all-targets -- -D warnings` on the
    // Linux/Windows CI runners saw the consts as dead and failed the build.
    // allow, not expect: on macOS the consts are used and expect(dead_code)
    // would itself warn there.
    const d = readContract('desktop')
    const rs = emitRsDesktop(d)
    expect(rs).toContain('#[cfg_attr(not(target_os = "macos"), allow(dead_code))]\npub const EVENT_MENU_SHOW_ABOUT: &str = "menu:show-about"')
    expect(rs).toContain('#[cfg_attr(not(target_os = "macos"), allow(dead_code))]\npub const EVENT_MENU_SHOW_PREFERENCES: &str = "menu:show-preferences"')
    expect(rs).not.toContain('#[cfg_attr(not(target_os = "macos"), allow(dead_code))]\npub const EVENT_CHANNEL_MESSAGE')
  })

  it('emits durations as const expressions, not runtime values', () => {
    const go = emitGoWire(WIRE, deriveWire(WIRE))
    expect(go).toContain('SessionKeyMaxAge      = time.Duration(3600000) * time.Millisecond')
    // A true const is load-bearing: desktop/go/frame.go derives
    // maxFrameSize from channelwire limits in a const expression.
    expect(go).not.toContain('var ')
  })

  it('quotes strings as Go and TS both accept', () => {
    expect(emitGoHeaders(HEADERS)).toContain('"Leapmux-Elevation-Required"')
    // The TS emitter uses JSON.stringify too: double quotes, matching the
    // repo's eslint style for generated string literals.
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
    // The Go backoff previously hard-coded the doubling; the contract value
    // must reach Go or editing it moves only the browser's schedule.
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
    // Proto order, matching the Go twin's AllProviders.
    expect(ts.indexOf('AgentProvider.CLAUDE_CODE')).toBeLessThan(ts.indexOf('AgentProvider.CODEX'))
    // The browser never parses provider strings (the CLI and admin RPCs do,
    // via the Go twin), so the TS emitter must not ship a dead parse table.
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

// The provider-protocol domains (zcode, pi) carry an agent's OWN wire vocabulary, and
// both languages dispatch on the literals -- so the checks below are what stop a table
// from reaching one side and not the other, or from carrying two branches the wire
// cannot tell apart.
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

  // A table added to the JSON but not to PROVIDER_PROTOCOLS emits nothing, so one side
  // would read a vocabulary the other never got.
  it('rejects a table the spec does not declare', () => {
    expectContractError(
      () => checkProviderProtocol(spec, { ...ok(), decisions: { Allow: 'allow' } }),
      'table decisions is not declared',
    )
  })

  // Two keys with one literal make two dispatch branches indistinguishable on the wire.
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

// The pool the worker and the browser dialogs both name tabs from. The schema
// holds the per-name shape; these are the two relations it cannot express.
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

  // Two equal prefixes collapse "Agent Gabe" and "Terminal Gabe" into one
  // title, and plan-mode auto-rename keys on the agent prefix alone -- it
  // would start overwriting terminal titles.
  it('rejects equal title prefixes', () => {
    expectContractError(
      () => checkTabNames({ ...ok(), titlePrefixes: { agent: 'Tab', terminal: 'Tab' } }),
      'title prefixes must differ',
    )
  })

  // uniqueItems in the schema catches an exact repeat; sorting is what makes
  // a near-repeat visible to the reviewer who has to apply the rules the
  // schema cannot express.
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

describe('generate', () => {
  it('emits the shipped domains from the real contracts dir', () => {
    const files = generate(join(ROOT, 'contracts'), DESCRIPTOR)
    expect(Object.keys(files).sort()).toEqual([
      'backend/generated/contracts/captcha.go',
      'backend/generated/contracts/desktop.go',
      'backend/generated/contracts/headers.go',
      'backend/generated/contracts/pi-protocol.go',
      'backend/generated/contracts/providers.go',
      'backend/generated/contracts/retry.go',
      'backend/generated/contracts/scopes.go',
      'backend/generated/contracts/tab-names.go',
      'backend/generated/contracts/theme.go',
      'backend/generated/contracts/validate.go',
      'backend/generated/contracts/wire.go',
      'backend/generated/contracts/worker-vocab.go',
      'backend/generated/contracts/zcode-protocol.go',
      'desktop/rust/src/generated/contracts.rs',
      'frontend/src/generated/contracts/captcha.ts',
      'frontend/src/generated/contracts/desktop.ts',
      'frontend/src/generated/contracts/headers.ts',
      'frontend/src/generated/contracts/pi-protocol.ts',
      'frontend/src/generated/contracts/providers.ts',
      'frontend/src/generated/contracts/retry.ts',
      'frontend/src/generated/contracts/scopes.ts',
      'frontend/src/generated/contracts/tab-names.ts',
      'frontend/src/generated/contracts/theme-default.ts',
      'frontend/src/generated/contracts/validate.ts',
      'frontend/src/generated/contracts/wire.ts',
      'frontend/src/generated/contracts/worker-vocab.ts',
      'frontend/src/generated/contracts/zcode-protocol.ts',
    ])
  })

  it('fails loudly when a registered domain is missing its contract file', () => {
    // A typo'd or renamed contracts/<name>.json used to skip its emitter
    // silently; sync-generated then pruned the domain's outputs as orphans
    // and the failure surfaced as compile errors pointing at generated code.
    // Every registered domain must ship its file, and the error must name it.
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
    // Same constraint as scopes: emitTsProviders interpolates the stripped
    // suffix into AgentProvider.<suffix>, and a digit-leading value (legal
    // protobuf) emits invalid TypeScript while the Go twin compiles.
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
    // The partition check is the headline cross-check: without it, a scope
    // added to the proto passes generation and every surface silently omits
    // it (no token, no sentence, no checkbox).
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
    // tsClassSource emits 4-hex-digit \uXXXX escapes: an astral point becomes
    // a valid BMP escape plus a literal digit, and goRangeTable writes uint16
    // fields. Only a refusedAscii astral point compiles in Go at all -- while
    // the browser class still matches the wrong characters.
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
    // The guard used to destructure only `lo`: [34, 128545] passed every
    // check, and tsClassSource emitted \u0022-\u1f621, which a JS regex
    // silently reads as U+0022-U+1F62 plus the literal "1" -- a different
    // character set than Go's rune list, with no error anywhere.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[34, 128545]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
    }), 'leaves the BMP')
  })

  it('rejects an inverted refusedAscii range', () => {
    // An inverted entry built a character class the browser cannot compile
    // ("range out of order") at module load, while Go silently omitted the
    // rune from its list.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[126, 34]] },
    }), 'inverted')
  })

  it('rejects a session file-path cap that is not larger than the token cap', () => {
    // The two caps hold a RELATION, not two independent numbers: a session file
    // path carries a directory prefix a token never does, and applying the token
    // cap to a path refused every real one. An edit that lowers the file-path cap
    // to the token cap must fail generation rather than reintroduce that refusal.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 1, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      usernames: { systemReserved: {}, publicReserved: {} },
    }), 'filePathByteLimit must be > session.byteLimit')
  })

  it('rejects a reserved username the Go const mangle cannot spell', () => {
    // goReservedUsernames builds `Username<Name>` by case-mangling alone; a
    // hyphen in the name emits invalid Go that fails at go build, not at
    // generation. The check must fail generation with the username named.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 2]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      usernames: { systemReserved: { 'ci-bot': 'why' }, publicReserved: {} },
    }), 'not a valid Go identifier')
  })

  it('rejects a session.invisibleFormat that diverges from the frozen name rule', () => {
    // The repetition is the point: a name-rule change must not silently move
    // what a token may hold. The generator enforces it next to the data.
    expectContractError(() => checkValidate({
      name: { byteLimit: 1, invisibleFormat: [[1, 2]], whitespaceFold: [[3, 4]] },
      session: { byteLimit: 1, filePathByteLimit: 2, invisibleFormat: [[1, 3]], refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      password: { minLength: 1, maxLength: 2, printableAsciiMin: 32, printableAsciiMax: 126 },
      branch: { byteLimit: 1, refusedControl: [[0, 1]], refusedAscii: [[2, 2]] },
      usernames: { systemReserved: {}, publicReserved: {} },
    }), 'FROZEN')
  })

  it('rejects a rename for a page token the contract never lists', () => {
    // A rename whose key is absent from oauthPage.tokens is dead data: the
    // emitter iterates only the listed tokens, so the page never defines the
    // CSS variable and every rule reading it renders unset.
    const theme = {
      light: { '--background': 'a', '--lm-danger-subtle': 'x' },
      dark: { '--background': 'c', '--lm-danger-subtle': 'y' },
      oauthPage: { tokens: ['--background'], renames: { '--danger-subtle': '--lm-danger-subtle' } },
    }
    expectContractError(() => checkTheme(theme), 'not in oauthPage.tokens')
  })

  it('rejects a scope whose prefix-stripped suffix is not a valid TS member', () => {
    // emitTsScopes interpolates the stripped suffix into Scope.<suffix>; a
    // digit-leading value (legal protobuf) emits invalid TypeScript while
    // the Go twin compiles, so the two sides fail asymmetrically.
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
    // The class the browser shipped before contracts existed, frozen: C0
    // minus the fold block, C1 minus NEL, then the format characters. The
    // derived control half carries no name (it has none); the format half
    // keeps the annotation straight from the contract.
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
    // The refused-ASCII runes carry their names from the contract too, so the
    // Go prose cannot go stale against the JSON.
    expect(go).toContain('var SessionRefusedASCII = []rune{0x0022, 0x0024, 0x0025, 0x005C} // QUOTATION MARK, DOLLAR SIGN, PERCENT SIGN, REVERSE SOLIDUS')
    const ts = emitTsValidate(v)
    // The TS side carries the same names as a companion comment block above
    // each class const; the class BODY itself stays pure regex syntax.
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

describe('checkWorkerVocab / checkDesktop', () => {
  it('accepts the shipped contracts', () => {
    const v = readContract('worker-vocab')
    const d = readContract('desktop')
    expect(() => checkWorkerVocab(v)).not.toThrow()
    expect(() => checkDesktop(d)).not.toThrow()
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
    // The browser's thread probe routes on obj.type === NOTIFICATION_THREAD_TYPE;
    // a notification type with the same token would be misrouted into the
    // wrapper branch.
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
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y' },
      tauriEvents: { channelMessage: 'same:event', channelClose: 'same:event', userEventsMessage: 'u:m', userEventsClose: 'u:c' },
    }), 'share one name')
  })

  it('rejects a desktop value with no name-table entry instead of emitting nothing', () => {
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y', extra: 'C_Z' },
      tauriEvents: { channelMessage: 'c:m', channelClose: 'c:c', userEventsMessage: 'u:m', userEventsClose: 'u:c' },
    }), 'has no DESKTOP_GO_ENV_NAMES entry')
    expectContractError(() => checkDesktop({
      envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y' },
      tauriEvents: { channelMessage: 'c:m', channelClose: 'c:c', userEventsMessage: 'u:m', userEventsClose: 'u:c', extra: 'e:x' },
    }), 'has no DESKTOP_RS_EVENT_NAMES entry')
  })

  it('rejects a macOS-only event key that no name table carries', () => {
    // A typo in DESKTOP_RS_MACOS_ONLY_EVENTS would silently stop annotating
    // the const it meant, and the off-macOS clippy failure would come back.
    DESKTOP_RS_MACOS_ONLY_EVENTS.add('noSuchEvent')
    try {
      expectContractError(() => checkDesktop({
        envVars: { devEndpoint: 'A_X', binaryHash: 'B_Y' },
        tauriEvents: { channelMessage: 'c:m', channelClose: 'c:c', userEventsMessage: 'u:m', userEventsClose: 'u:c' },
      }), 'missing from DESKTOP_RS_EVENT_NAMES')
    }
    finally {
      DESKTOP_RS_MACOS_ONLY_EVENTS.delete('noSuchEvent')
    }
  })

  it('emits the Rust module without inner doc comments (include! cannot take them)', () => {
    const d = readContract('desktop')
    const rs = emitRsDesktop(d)
    expect(rs).toContain('pub const ENV_DEV_ENDPOINT: &str = "LEAPMUX_DESKTOP_DEV_ENDPOINT"')
    expect(rs).toContain('pub const EVENT_CHANNEL_MESSAGE: &str = "channel:message"')
    expect(rs).not.toContain('//!')
  })

  it('emits the frame cap to Go and Rust from one contract value', () => {
    // The frame budget was three hand-restated copies (derived Go const,
    // hardcoded Rust const, restated-literal test); it is one contract value
    // now, so the twin emissions must both carry it.
    const d = readContract('desktop')
    expect(emitGoDesktop(d)).toContain(`const MaxFrameSizeBytes = ${d.maxFrameSizeBytes}`)
    expect(emitRsDesktop(d)).toContain(`pub const MAX_FRAME_SIZE_BYTES: u64 = ${d.maxFrameSizeBytes};`)
  })
})

describe('enumValues', () => {
  it('reads enum names from a buf descriptor, reserved values absent', () => {
    // The real descriptor: reserved slot 3 must not appear, or the providers
    // cross-check would demand metadata for a provider that cannot exist.
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
