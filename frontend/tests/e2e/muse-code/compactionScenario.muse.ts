/**
 * The native Muse compaction summarizer: the host asks the model to compact
 * through a `generate_summary` tool call, and the summary's fields replace the
 * older context in the next model turn.
 */
import type { MockModelMatcher, MockModelStep } from '../helpers/mockModelScript'
import { MANUAL_COMPACTION_SUMMARY } from '../helpers/manualCompaction'

/** The system text that identifies every summarizer request. */
export const MUSE_SUMMARIZER_SYSTEM = 'Compact context for a coding agent'

/**
 * The matcher that recognizes a native Muse summarizer request: its instruction
 * arrives as a developer row, which the joined system text of the generic
 * protocols does not always carry, so the matcher reads the whole body.
 */
export const museSummarizerMatcher: MockModelMatcher = { body: [MUSE_SUMMARIZER_SYSTEM] }

/** The generate_summary fields the host requires, each carrying the marker. */
export function museCompactionSummaryStep(): MockModelStep {
  const fields = {
    primary_request_and_intent: MANUAL_COMPACTION_SUMMARY,
    user_constraints_and_preferences: MANUAL_COMPACTION_SUMMARY,
    current_state: MANUAL_COMPACTION_SUMMARY,
    files_apis_commands_and_tests: MANUAL_COMPACTION_SUMMARY,
    decisions_and_rationale: MANUAL_COMPACTION_SUMMARY,
    errors_failed_attempts_and_fixes: MANUAL_COMPACTION_SUMMARY,
    open_questions_and_risks: MANUAL_COMPACTION_SUMMARY,
    pending_tasks_and_next_step: MANUAL_COMPACTION_SUMMARY,
    user_message_timeline: MANUAL_COMPACTION_SUMMARY,
  }
  return { toolCalls: [{ id: 'muse-generate-summary', name: 'generate_summary', namespace: 'muse', arguments: fields }] }
}
