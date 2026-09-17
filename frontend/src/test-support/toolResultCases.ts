import type { CallReader, FailureReader, ToolResultCheck, ToolVocabularyCheck } from './toolVocabulary'
import { describe, expect, it } from 'vitest'
import {
  documentedUnparsedThatParse,
  failuresThatMisreadTheirKind,
  failuresThatMisreadTheirStatus,
  failuresWithoutASuccessFixture,
  invariantViolations,
  kindsWithoutFailureFixture,
  namesWithoutResultFixture,
  openersThatAnswerEarly,
  staleNoFailureReasons,
  staleResultEntries,
  undocumentedUnparsedResults,
} from './toolVocabulary'

// The cases every provider's `toolResults.test.ts` runs, emitted from ONE place.
//
// Nine hand-copies are nine chances to leave a case out, and the rules these cases
// carry are rules about EVERY provider. "A provider that adds a kind cannot leave its
// failure path unpinned" holds only while every provider runs the same cases.
//
// ONE case stays with the provider, and `describeToolResultCorpus` says which.

/**
 * The three readings one provider's failure ladder needs.
 *
 * Each goes through the provider's OWN extraction, so a reader states the span column
 * and the paired sides its protocol needs and nothing else varies between providers.
 */
export interface FailureLadderReaders {
  /** The call a SUCCESSFUL fixture extracts, by tool name. */
  callOf: CallReader
  /** The call a FAILED frame extracts. */
  failureCallOf: FailureReader
  /** The call one fixture's OPENING frame extracts, read alone as a call still in flight. */
  openerCallOf: CallReader
}

/**
 * Every case of one provider's result CORPUS, emitted from ONE place.
 *
 * The corpus holds one successful frame for each tool the kind table lists. These
 * cases ask whether it stays complete, whether it documents what this build cannot
 * read, whether it holds anything the table dropped, and whether each frame extracts
 * into a call the seven invariants admit.
 *
 * ONE question about a corpus is the provider's own, so no case here asks it: the KIND
 * each fixture reaches. Seven providers ask the narrow form -- no fixture lands on the
 * uncategorized card, which `fixturesOnTheUncategorizedKind` answers. Claude and Pi ask
 * the strict form -- the extracted kind is the kind the table states, which
 * `fixturesThatChangeKind` answers. A provider whose wire kind outranks its own table
 * cannot hold the strict form, so the two are separate guards rather than one guard
 * with an option, and each provider spells the one it can hold.
 *
 * Call it INSIDE the provider's own `describe`, with the reader that carries whatever
 * span column that protocol needs.
 */
export function describeToolResultCorpus(kinds: ToolVocabularyCheck, check: ToolResultCheck, callOf: CallReader): void {
  describe('the result corpus', () => {
    it('holds a successful result frame for every name the kind table holds', () => {
      expect(
        namesWithoutResultFixture(kinds, check),
        'A tool with no fixture is a result nobody reads. Add the frame the provider sends.',
      ).toStrictEqual([])
    })

    it('documents every result that stays unparsed', () => {
      expect(
        undocumentedUnparsedResults(check, callOf),
        'A successful result nobody can read is a body the row pretends to understand.',
      ).toStrictEqual([])
    })

    it('keeps every unparsed reason pinned to a result that stays unparsed', () => {
      expect(
        documentedUnparsedThatParse(check, callOf),
        'The result parses now; delete the stale reason.',
      ).toStrictEqual([])
    })

    it('holds no fixture or reason for a name the table dropped', () => {
      expect(
        staleResultEntries(kinds, check),
        'The table no longer holds the name, so the fixture or the reason describes a tool nobody reaches. Delete it.',
      ).toStrictEqual([])
    })

    it('satisfies the call invariants for every fixture', () => {
      for (const name of Object.keys(check.fixtures)) {
        const call = callOf(name)
        expect(call, name).not.toBeNull()
        expect(invariantViolations(call!), name).toStrictEqual([])
      }
    })
  })
}

/**
 * Every case of one provider's failure ladder, emitted from ONE place.
 *
 * A provider that adds a kind must find no failed frame for it, and that only holds
 * while every provider runs the same eight cases.
 *
 * Call it INSIDE the provider's own `describe`, with readers that carry whatever span
 * column and paired request that protocol needs.
 */
export function describeToolFailureLadder(check: ToolResultCheck, readers: FailureLadderReaders): void {
  const { callOf, failureCallOf, openerCallOf } = readers
  describe('the failure ladder', () => {
    it('pairs every failed frame with the successful frame of the same tool', () => {
      expect(
        failuresWithoutASuccessFixture(check),
        'A failed frame takes its request half from the successful fixture of that tool, so the tool needs one.',
      ).toStrictEqual([])
    })

    it('pins a failed frame for every kind a fixture draws', () => {
      expect(
        kindsWithoutFailureFixture(check, callOf),
        'A kind with no failed frame leaves its whole failure ladder unpinned. Add the frame, or state the reason in noFailure.',
      ).toStrictEqual([])
    })

    it('holds no failure reason for a kind a failed frame pins', () => {
      expect(
        staleNoFailureReasons(check, callOf),
        'The reason excuses a kind a failed frame now pins, or one no fixture reaches. Delete it.',
      ).toStrictEqual([])
    })

    it('draws the kind every failed frame states', () => {
      expect(
        failuresThatMisreadTheirKind(check, failureCallOf),
        'A failure that lands on another kind draws its reason under another tool.',
      ).toStrictEqual([])
    })

    it('reads the outcome word every failed frame states', () => {
      expect(
        failuresThatMisreadTheirStatus(check, failureCallOf),
        'The header takes its word from the status alone, so a failure that reads as completed contradicts the reason under it.',
      ).toStrictEqual([])
    })

    // I3 and I4 both land here. A failed call that answers `unparsedResult` claims it
    // completed, and a completed call that answers `failedResult` claims it did not:
    // the two brands draw the same pixels, so nothing but this states the difference.
    // I7 lands here too -- a failed file change that emptied its request cannot name
    // the file the reader is looking at.
    it('satisfies the call invariants for every failed frame', () => {
      for (const fixture of check.failures) {
        const call = failureCallOf(fixture)
        expect(call, fixture.name).not.toBeNull()
        expect(invariantViolations(call!), fixture.name).toStrictEqual([])
      }
    })

    // The OPENING frame of each fixture, which the corpus cases never read alone.
    // Invariant I1 states that a result implies a final status, and `ToolMessage`
    // draws the live output the worker broadcasts only while the row is in flight AND
    // states no result -- so a reader that fills a result early replaces the streaming
    // tail with an empty card.
    it('satisfies the call invariants for the opening frame of every fixture', () => {
      for (const name of Object.keys(check.fixtures)) {
        const call = openerCallOf(name)
        expect(call, name).not.toBeNull()
        expect(invariantViolations(call!), name).toStrictEqual([])
      }
    })

    it('answers no result on the opening frame of any fixture', () => {
      expect(
        openersThatAnswerEarly(check, openerCallOf),
        'The call has not answered yet, so a result on its opening frame is one the tool never sent.',
      ).toStrictEqual([])
    })
  })
}
