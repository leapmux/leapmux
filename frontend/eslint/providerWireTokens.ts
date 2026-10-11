import type { ProviderFrameKind } from '../src/generated/contracts/provider-frame-kinds'
import { PROVIDER_FRAME_KINDS } from '../src/generated/contracts/provider-frame-kinds'

/**
 * The wire tokens that shared chat code must not spell, from the provider contracts.
 *
 * A wire token is a frame kind that a contract table marks with `frameKind`: an
 * event, a method, a notification, an update, or the type of a line, an item or a
 * request. A reader dispatches on it, so shared code that spells one decides by the
 * provider. The contracts are the one source of the list. A contract that gains a
 * frame kind extends the lint with no edit here.
 *
 * The list holds what the contracts hold. A method that only the worker sends, such
 * as ACP's `session/prompt`, never reaches the browser, so it has no contract entry
 * and no browser code can decide on it.
 */

/**
 * Whether a word is distinctive enough to be a wire token alone.
 *
 * The lint cannot tell a frame kind from an ordinary word that shared code spells
 * for its own meaning. Copilot sends an `error` event, and shared code spells `error`
 * as a status. A word that holds a `.`, a `/` or a `_`, or a lowercase letter before
 * a capital letter, is not an ordinary word. A hyphen does not count, because class
 * names and test ids hold one.
 */
export function isDistinctiveWord(word: string): boolean {
  return /[./_]|[a-z][A-Z]/.test(word)
}

/** The separator that ends a prefix, which the distinctiveness test ignores. */
const TRAILING_SEPARATOR = /[./_]$/

/** One guarded prefix, and the tables that own it. */
export interface WireTokenPrefix {
  readonly prefix: string
  readonly sources: readonly string[]
}

/** The guarded wire tokens, each with the tables that own it. */
export interface WireTokenIndex {
  /** Each whole token. */
  readonly names: ReadonlyMap<string, readonly string[]>
  /** Each prefix. A literal that starts with one, or equals one, is a token. */
  readonly prefixes: readonly WireTokenPrefix[]
}

/** Add `source` to the owners of `key`, once. */
function addSource(map: Map<string, string[]>, key: string, source: string): void {
  const sources = map.get(key)
  if (sources === undefined)
    map.set(key, [source])
  else if (!sources.includes(source))
    sources.push(source)
}

/**
 * The index of the wire tokens that `kinds` states.
 *
 * - A `name` kind that is a distinctive word is a token.
 * - A `prefix` kind guards every literal that starts with it, when the prefix is a
 *   distinctive word without its final separator. Copilot's `model.` family fails
 *   that test. Shared code can spell a key such as `model.row`, so that family stays
 *   open.
 * - A method that holds a `/` opens with its namespace, as `_kiro/` opens
 *   `_kiro/userInput`. The namespace becomes a prefix under the same test, so a
 *   method of that namespace that no contract holds yet is a token too.
 */
export function wireTokenIndex(kinds: readonly ProviderFrameKind[]): WireTokenIndex {
  const names = new Map<string, string[]>()
  const prefixes = new Map<string, string[]>()
  for (const kind of kinds) {
    if (kind.match === 'prefix') {
      addSource(prefixes, kind.literal, kind.source)
      continue
    }
    if (isDistinctiveWord(kind.literal))
      addSource(names, kind.literal, kind.source)
    const slash = kind.literal.indexOf('/')
    if (slash > 0)
      addSource(prefixes, kind.literal.slice(0, slash + 1), kind.source)
  }
  const guarded = [...prefixes]
    .filter(([prefix]) => isDistinctiveWord(prefix.replace(TRAILING_SEPARATOR, '')))
    .map(([prefix, sources]) => ({ prefix, sources }))
  return { names, prefixes: guarded }
}

/**
 * The tables that own `literal` as a wire token. The list is empty when shared code
 * may spell the literal.
 */
export function wireTokenSources(index: WireTokenIndex, literal: string): readonly string[] {
  const sources = new Set(index.names.get(literal))
  for (const { prefix, sources: owners } of index.prefixes) {
    if (literal.startsWith(prefix)) {
      for (const owner of owners)
        sources.add(owner)
    }
  }
  return [...sources]
}

/** The wire tokens of every provider contract. */
export const PROVIDER_WIRE_TOKENS: WireTokenIndex = wireTokenIndex(PROVIDER_FRAME_KINDS)
