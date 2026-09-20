import type { CodexStatus } from './itemVocabulary'
import { CODEX_STATUS } from './itemVocabulary'

export type CodexFinishedStatus = typeof CODEX_STATUS.COMPLETED | typeof CODEX_STATUS.FAILED | typeof CODEX_STATUS.DECLINED

/** Every status that means Codex stopped working on the item, however it stopped. */
const FINISHED_STATUSES: ReadonlySet<string> = new Set<string>([
  CODEX_STATUS.COMPLETED,
  CODEX_STATUS.FAILED,
  CODEX_STATUS.DECLINED,
])

/**
 * Codex item statuses that indicate the agent is finished working on the item
 * (whether successfully or not). Items in any other status are still in
 * progress.
 */
export function isCodexFinishedStatus(status: string | null | undefined): boolean {
  return parseCodexStatus(status) !== CODEX_STATUS.IN_PROGRESS
}

/**
 * Read a Codex item's `status` and normalize it to one of the values downstream
 * consumers handle. A word Codex does not send maps to `inProgress`.
 *
 * `declined` must stay itself. Codex sends it on a `commandExecution` or a
 * `fileChange` whose approval the reader denied, and folding it into `inProgress`
 * drew that row as a call still running for the rest of the transcript.
 */
export function parseCodexStatus(raw: unknown): CodexStatus {
  return typeof raw === 'string' && FINISHED_STATUSES.has(raw)
    ? raw as CodexFinishedStatus
    : CODEX_STATUS.IN_PROGRESS
}
