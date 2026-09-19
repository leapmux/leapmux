import type { ProviderRowOptions } from './toolCallIr'
import type { ToolCallDraft, ToolCallIR } from '~/components/chat/ir/toolCall'
import type { ToolKind } from '~/components/chat/ir/toolKind'
import type { ToolRowStatus } from '~/components/chat/ir/toolRowStatus'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isUnparsedResult, toolCallFault } from '~/components/chat/ir/toolCall'

/**
 * The shared shape of a provider's tool-vocabulary coverage test.
 *
 * Each provider lists its tools in ONE table -- a generated contract where the
 * contract holds the vocabulary, the plugin's own constant table where none does --
 * and turns a name into a {@link ToolKind}. The coverage test walks that table and
 * asserts that every name reaches a kind, so a tool added to the contract cannot
 * quietly take the uncategorized row.
 *
 * The "known world" is never a third hand-written list. The test reads the table,
 * and the only thing it states by hand is which names take the fallback ON PURPOSE.
 * That set is what turns an accidental omission into a failure and a deliberate one
 * into a documented decision.
 */
export interface ToolVocabularyCheck {
  /** Every wire name the provider's table holds. */
  names: readonly string[]
  /** The provider's own name-to-kind function. */
  kindOf: (name: string) => ToolKind
  /**
   * The names that take the fallback on purpose, each mapped to the reason.
   *
   * A reason rather than a bare set, because "no kind in the closed set fits this
   * tool" and "this tool draws through another path entirely" are different answers
   * and the next reader has to be able to tell them apart.
   */
  generic: Readonly<Record<string, string>>
  /** The kind the provider answers for a name its table does not hold. */
  fallback: ToolKind
}

/** The names a check states as generic that its table no longer holds. */
export function staleGenericNames(check: ToolVocabularyCheck): string[] {
  const present = new Set(check.names)
  return Object.keys(check.generic).filter(name => !present.has(name))
}

/** The names that take the fallback without saying so. */
export function undocumentedFallbacks(check: ToolVocabularyCheck): string[] {
  return check.names.filter(name => check.kindOf(name) === check.fallback && !(name in check.generic))
}

/** The names a check calls generic that in fact reach a kind. */
export function documentedNamesThatReachAKind(check: ToolVocabularyCheck): string[] {
  return Object.keys(check.generic).filter(name => check.names.includes(name) && check.kindOf(name) !== check.fallback)
}

/**
 * The shared shape of a provider's tool-RESULT coverage test.
 *
 * The vocabulary test walks the names; this walks what each name ANSWERS. One
 * successful result frame per name, and every name that holds none -- because it
 * draws no row of its own, or because this build cannot read its payload -- says
 * so with a reason. That is what keeps "nobody wrote a case" and "the case is
 * impossible" distinguishable.
 */
export interface ToolResultFixture {
  payload: Record<string, unknown>
  options?: ProviderRowOptions
}

/** The three words that end a call without an answer. `completed` is the fourth final word. */
export type FailedCallStatus = Extract<ToolRowStatus, 'failed' | 'cancelled' | 'declined'>

/**
 * A frame that ends one call BELOW a successful completion.
 *
 * Three fields beyond the frame, and a reader scans them in that order: the KIND the
 * failed row draws, the TOOL it draws for, and the OUTCOME WORD its header states.
 * Each carries a defect of its own. A failure that keeps the kind and loses the word
 * draws the reason under a header that still says the call succeeded. One that keeps
 * the word and loses the kind draws the reason under a wrench. One whose kind is not
 * the kind its success draws sends the reader to another tool's card.
 *
 * `name` is what pairs the two frames. The failure takes its REQUEST half from the
 * successful fixture of that name, so the two describe ONE call rather than two
 * similar ones.
 *
 * The WORDS inside a fixture are the fixture's own. No provider's error sentence is
 * confirmable from this repository, so a failure frame states a plainly synthetic
 * one: the guard is about the ladder -- the outcome word, the brand, the kind and the
 * request -- and never about the wording a provider chooses.
 */
export interface ToolFailureFixture extends ToolResultFixture {
  /** The kind the failed row draws. */
  kind: ToolKind
  /** The tool whose successful fixture this failure pairs with. */
  name: string
  /** The outcome word the extraction must read from this frame. */
  status: FailedCallStatus
}

export interface ToolResultCheck {
  provider: AgentProvider
  /** A SUCCESSFUL result frame for every name the kind table holds, except the names `noResult` explains. */
  fixtures: Readonly<Record<string, ToolResultFixture>>
  /**
   * A FAILED result frame for at least one tool of every kind the fixtures reach.
   *
   * The coverage rule is over KINDS rather than over names, because the failure
   * ladder branches on the kind: one failed `execute` pins the ladder for every
   * command tool a provider holds. A provider that adds a kind then has no failed
   * frame for it until somebody writes one, and `noFailure` is the one way out --
   * with a reason.
   *
   * A list rather than a map, so a kind whose tools walk SEPARATE failure paths can
   * hold one entry for each of them. Claude's two task tools are that case.
   */
  failures: readonly ToolFailureFixture[]
  /** Kinds with no failed frame ON PURPOSE, each with the reason. */
  noFailure: Readonly<Partial<Record<ToolKind, string>>>
  /** Names with no fixture ON PURPOSE (a hidden row, a request-only tool), each with the reason. */
  noResult: Readonly<Record<string, string>>
  /** Names whose SUCCESSFUL result stays UnparsedResult on purpose, each with the reason. */
  unparsed: Readonly<Record<string, string>>
}

/** One frame read into its call, or null when the frame draws no tool row. */
export type CallReader = (name: string) => ToolCallIR | null

/** The names whose kind the table holds but that state no successful result frame. */
export function namesWithoutResultFixture(kinds: ToolVocabularyCheck, results: ToolResultCheck): string[] {
  return kinds.names.filter(name => !(name in results.fixtures) && !(name in results.noResult))
}

/** The names a fixture documents under one kind while its frame extracts another. */
export function fixturesThatChangeKind(kinds: ToolVocabularyCheck, results: ToolResultCheck, callOf: CallReader): string[] {
  return kinds.names.filter((name) => {
    const fixture = results.fixtures[name]
    if (!fixture)
      return false
    const call = callOf(name)
    return call !== null && call.kind !== kinds.kindOf(name)
  })
}

/**
 * The fixtures whose frame reaches the uncategorized kind.
 *
 * The weaker half of the question {@link fixturesThatChangeKind} asks, and the only
 * half a provider whose WIRE kind outranks its own table can ask. Such a table states
 * `grep` for a tool the protocol calls `search`, and the wire word wins -- so the
 * strict form reports a disagreement the adapter produces on purpose. What may never
 * happen is the uncategorized card: it draws a wrench, the word "Other" and a dump of
 * the arguments, and identifies nothing the agent ran.
 *
 * `mcp` stays OUT, although {@link isGenericKind} holds all three together. The Model
 * Context Protocol card states the server and the tool it called, so a tool that
 * reaches it is identified. The empty kind and `other` are the two that identify
 * nothing.
 */
export function fixturesOnTheUncategorizedKind(results: ToolResultCheck, callOf: CallReader): string[] {
  return Object.keys(results.fixtures).filter((name) => {
    const call = callOf(name)
    return call !== null && (call.kind === '' || call.kind === 'other')
  })
}

/** The names whose successful result stays unparsed although no reason says it must. */
export function undocumentedUnparsedResults(results: ToolResultCheck, callOf: CallReader): string[] {
  return Object.keys(results.fixtures).filter((name) => {
    const call = callOf(name)
    return call !== null && call.result !== undefined && isUnparsedResult(call.result) && !(name in results.unparsed)
  })
}

/** The reasons that name a result this build no longer fails to read. */
export function documentedUnparsedThatParse(results: ToolResultCheck, callOf: CallReader): string[] {
  return Object.keys(results.unparsed).filter((name) => {
    const call = callOf(name)
    return call === null || call.result === undefined || !isUnparsedResult(call.result)
  })
}

/** The fixture and reason entries that no longer name a tool the table holds. */
export function staleResultEntries(kinds: ToolVocabularyCheck, results: ToolResultCheck): string[] {
  const present = new Set(kinds.names)
  return [...Object.keys(results.fixtures), ...Object.keys(results.noResult), ...Object.keys(results.unparsed)]
    .filter(name => !present.has(name))
}

/** One FAILED frame read into its call, through the provider's own extraction. */
export type FailureReader = (fixture: ToolFailureFixture) => ToolCallIR | null

/** One failed entry, as a reader identifies it in a message. */
function describeFailure(fixture: ToolFailureFixture): string {
  return `${fixture.name} (${fixture.kind || 'no kind'})`
}

/** The failed frames whose tool holds no successful fixture beside it. */
export function failuresWithoutASuccessFixture(results: ToolResultCheck): string[] {
  return results.failures.filter(fixture => !(fixture.name in results.fixtures)).map(describeFailure)
}

/** The kinds one reader reaches, over the names it can read. */
function kindsOf(names: readonly string[], callOf: CallReader): Set<ToolKind> {
  const kinds = new Set<ToolKind>()
  for (const name of names) {
    const call = callOf(name)
    if (call !== null)
      kinds.add(call.kind)
  }
  return kinds
}

/**
 * The kinds a successful fixture draws that no failed frame pins.
 *
 * This is what makes the failure ladder total. A provider that adds a kind reaches it
 * from a success fixture, and the kind then has no failed frame until somebody writes
 * one or states the reason in `noFailure`.
 *
 * The STATED kind counts, not the extracted one. `failuresThatMisreadTheirKind` holds
 * the two together, so a frame that drew another kind would otherwise pin that kind
 * and leave its own unpinned -- two failures for one defect, and the second one
 * pointing at the wrong table row.
 */
export function kindsWithoutFailureFixture(results: ToolResultCheck, callOf: CallReader): ToolKind[] {
  const pinned = new Set(results.failures.map(fixture => fixture.kind))
  const drawn = kindsOf(Object.keys(results.fixtures), callOf)
  return [...drawn].filter(kind => !pinned.has(kind) && !(kind in results.noFailure)).sort()
}

/**
 * The `noFailure` reasons that no longer excuse anything.
 *
 * Two ways for a reason to go stale, and both leave the same dead sentence behind: a
 * failed frame now pins the kind, or no successful fixture reaches it any more.
 */
export function staleNoFailureReasons(results: ToolResultCheck, callOf: CallReader): ToolKind[] {
  const pinned = new Set(results.failures.map(fixture => fixture.kind))
  const drawn = kindsOf(Object.keys(results.fixtures), callOf)
  return (Object.keys(results.noFailure) as ToolKind[]).filter(kind => pinned.has(kind) || !drawn.has(kind)).sort()
}

/** The failed frames that draw a kind other than the one they state. */
export function failuresThatMisreadTheirKind(results: ToolResultCheck, failureCallOf: FailureReader): string[] {
  return results.failures.flatMap((fixture) => {
    const call = failureCallOf(fixture)
    if (call === null || call.kind === fixture.kind)
      return []
    return [`${fixture.name}: states ${JSON.stringify(fixture.kind)} and draws ${JSON.stringify(call.kind)}`]
  })
}

/** The failed frames that read as an outcome word other than the one they state. */
export function failuresThatMisreadTheirStatus(results: ToolResultCheck, failureCallOf: FailureReader): string[] {
  return results.failures.flatMap((fixture) => {
    const call = failureCallOf(fixture)
    if (call === null || call.status === fixture.status)
      return []
    return [`${fixture.name}: states ${JSON.stringify(fixture.status)} and reads ${JSON.stringify(call.status)}`]
  })
}

/**
 * The OPENING frame of one fixture: its request side, as the wire sent it.
 *
 * Read on its own, that frame is a LIVE row -- the call has not answered yet -- and no
 * other case reads it. It is the one frame that can violate I1, and the ladder that
 * keeps the result away from it is the same ladder the failed frame walks.
 *
 * Null for a fixture that pairs with no request. `providerToolCall` takes the record,
 * so this hands back the raw frame rather than the parsed message around it.
 */
export function openingFrameOf(fixture: ToolResultFixture): Record<string, unknown> | null {
  return fixture.options?.request?.parentObject ?? null
}

/** The names whose OPENING frame already carries a result, so the row answers before the call did. */
export function requestsThatAnswerEarly(results: ToolResultCheck, requestCallOf: CallReader): string[] {
  return Object.keys(results.fixtures).filter((name) => {
    const call = requestCallOf(name)
    return call !== null && call.result !== undefined
  })
}

/**
 * The invariants one extracted call breaks, as messages. Empty means it holds them all.
 *
 * A THIN adapter over {@link toolCallFault}, which is the catalogue: the rules, their
 * historical I-numbers and the reason each one exists all live at that function, so
 * the corpus and the builder can never state different ones. The corpus used to
 * restate them, and the lifecycle union has since made most of the restatement
 * unreachable code.
 *
 * Read a green suite carefully. The union refuses every pair this reports at COMPILE
 * time, and `toolCall` degrades a draft that breaks one to the uncategorized row
 * rather than returning it -- so a call that reaches here has already passed the same
 * check twice. What this still earns is the third case: a call an `as` assertion
 * built behind the compiler, and the empty file name of I7, which no type states.
 * The DEGRADE is what the corpus detects for real, through
 * {@link fixturesThatChangeKind} and {@link failuresThatMisreadTheirKind}: a fixture
 * whose kind fell to `other` is a fixture whose draft broke a rule.
 */
export function invariantViolations(call: ToolCallIR): string[] {
  // `ToolCallDraft`'s optional members refuse an explicit undefined, which the
  // no-result half of the lifecycle still states, so the call is restated with
  // those members omitted rather than undefined. The catalogue reads the same
  // six fields either way.
  const draft: ToolCallDraft = {
    kind: call.kind,
    status: call.status,
    request: call.request,
    result: call.result,
    images: call.images,
    ...(call.extraContent === undefined ? {} : { extraContent: call.extraContent }),
    ...(call.truncated === undefined ? {} : { truncated: call.truncated }),
  }
  const fault = toolCallFault(draft)
  return fault === null ? [] : [fault]
}
