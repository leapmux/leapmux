import { describe, expect, it } from 'vitest'
import { MESSAGE_METADATA_FIELD, TOOL_OUTCOME } from '~/generated/contracts/worker-vocab'
import { parseToolOutcome, toolOutcomeNote } from './toolOutcome'

function metadata(outcome: Record<string, unknown>): Record<string, unknown> {
  return { [MESSAGE_METADATA_FIELD.ToolOutcome]: outcome }
}

describe('parseToolOutcome', () => {
  it('reads a complete note', () => {
    expect(parseToolOutcome(metadata({
      [TOOL_OUTCOME.FieldSource]: TOOL_OUTCOME.SourceBatchSummary,
      [TOOL_OUTCOME.FieldOutcome]: TOOL_OUTCOME.OutcomeSucceeded,
    }))).toEqual({ source: 'batch_summary', outcome: 'succeeded' })
  })

  it.each([
    ['no metadata', undefined],
    ['a non-object', 'note'],
    ['another metadata field', { duration_ms: 4 }],
    ['a note with no source', metadata({ [TOOL_OUTCOME.FieldOutcome]: TOOL_OUTCOME.OutcomeUnknown })],
    ['a note with no outcome', metadata({ [TOOL_OUTCOME.FieldSource]: TOOL_OUTCOME.SourceBatchSummary })],
  ])('returns null for %s', (_label, value) => {
    expect(parseToolOutcome(value)).toBeNull()
  })
})

describe('toolOutcomeNote', () => {
  it('states that the batch reported no error', () => {
    const note = toolOutcomeNote(metadata({
      [TOOL_OUTCOME.FieldSource]: TOOL_OUTCOME.SourceBatchSummary,
      [TOOL_OUTCOME.FieldOutcome]: TOOL_OUTCOME.OutcomeSucceeded,
    }))
    expect(note).toContain('no error')
    expect(note).not.toContain('unknown')
  })

  // The summary counts errors without naming the call that failed, so the note must
  // not claim that this call succeeded.
  it('states that the outcome is unknown when the batch reported an error', () => {
    expect(toolOutcomeNote(metadata({
      [TOOL_OUTCOME.FieldSource]: TOOL_OUTCOME.SourceBatchSummary,
      [TOOL_OUTCOME.FieldOutcome]: TOOL_OUTCOME.OutcomeUnknown,
    }))).toContain('unknown')
  })

  it('returns null for a source this build does not describe', () => {
    expect(toolOutcomeNote(metadata({
      [TOOL_OUTCOME.FieldSource]: 'a_source_a_later_build_adds',
      [TOOL_OUTCOME.FieldOutcome]: TOOL_OUTCOME.OutcomeSucceeded,
    }))).toBeNull()
  })
})
