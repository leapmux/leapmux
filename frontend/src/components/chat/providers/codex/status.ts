import { CODEX_STATUS } from '~/types/toolMessages'

export type CodexFinishedStatus = typeof CODEX_STATUS.COMPLETED | typeof CODEX_STATUS.FAILED

/**
 * Codex item statuses that indicate the agent is finished working on the item
 * (whether successfully or not). Items in any other status are still in
 * progress.
 */
export function isCodexFinishedStatus(status: string | null | undefined): boolean {
  return status === CODEX_STATUS.COMPLETED || status === CODEX_STATUS.FAILED
}

/**
 * Read a Codex item's `status` and normalize it to one of the three values
 * downstream consumers handle. Anything other than `failed`/`completed` maps
 * to `in_progress`.
 */
export function parseCodexStatus(raw: unknown): typeof CODEX_STATUS[keyof typeof CODEX_STATUS] {
  return raw === CODEX_STATUS.FAILED || raw === CODEX_STATUS.COMPLETED
    ? raw
    : CODEX_STATUS.IN_PROGRESS
}
