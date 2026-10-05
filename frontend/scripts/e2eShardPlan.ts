import type { BalanceMode } from './e2eOptions'
import type { E2ETestCoverage, FileDuration } from './e2eReports'
import type { StateFileOperations } from './e2eStateFiles'
import { isObject } from '../src/lib/jsonPick'
import { readOptionalStateFile, writeFileAtomically } from './e2eStateFiles'

/** The output root keeps the measured duration of each spec file under this name. */
export const DURATION_HISTORY_FILE = '.file-durations.json'
const DURATION_HISTORY_VERSION = 1

/** The last measured duration of each spec file, keyed by its path relative to the Playwright root directory. */
export type DurationHistory = ReadonlyMap<string, FileDuration>

export type DurationHistoryRead
  = | { readonly status: 'absent' }
    | { readonly status: 'unreadable', readonly reason: string }
    | { readonly status: 'present', readonly files: DurationHistory }

function historyEntry(value: unknown): FileDuration {
  if (!isObject(value) || typeof value.durationMs !== 'number' || !Number.isFinite(value.durationMs) || value.durationMs < 0
    || typeof value.cases !== 'number' || !Number.isSafeInteger(value.cases) || value.cases < 1) {
    throw new Error('an entry has no valid durationMs and cases')
  }
  return { durationMs: value.durationMs, cases: value.cases }
}

/** Accept the complete history or nothing, so one damaged entry cannot skew a plan. */
export function parseDurationHistory(value: unknown): DurationHistory {
  if (!isObject(value) || value.version !== DURATION_HISTORY_VERSION || !isObject(value.files))
    throw new Error(`the file is not version ${DURATION_HISTORY_VERSION} of the duration history`)
  const files = new Map<string, FileDuration>()
  for (const [file, entry] of Object.entries(value.files)) {
    if (!file)
      throw new Error('an entry has an empty file path')
    files.set(file, historyEntry(entry))
  }
  return files
}

/** Read the history. An unreadable history is a plan input, not a run failure: the plan uses the native split. */
export function readDurationHistory(path: string): DurationHistoryRead {
  try {
    const content = readOptionalStateFile(path)
    if (content === undefined)
      return { status: 'absent' }
    return { status: 'present', files: parseDurationHistory(JSON.parse(content)) }
  }
  catch (error) {
    return { status: 'unreadable', reason: error instanceof Error ? error.message : String(error) }
  }
}

/**
 * Replace the entry of each measured file and keep every other entry.
 * A run of a few files then keeps the durations of the files that it did not select.
 */
export function mergeDurationHistory(previous: DurationHistoryRead, measured: DurationHistory): DurationHistory {
  const files = new Map(previous.status === 'present' ? previous.files : [])
  for (const [file, duration] of measured)
    files.set(file, duration)
  return files
}

/** Write the history with sorted keys, so two equal histories produce equal files. */
export function writeDurationHistory(path: string, history: DurationHistory, io?: StateFileOperations): void {
  const files = Object.fromEntries([...history].sort(([left], [right]) => compareText(left, right)))
  writeFileAtomically(path, `${JSON.stringify({ version: DURATION_HISTORY_VERSION, files }, null, 2)}\n`, io)
}

/** Compare by UTF-16 code unit, which does not change with the locale. */
function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0
}

export interface FileEstimate {
  readonly file: string
  readonly estimateMs: number
  /** `history` scales the recorded duration to the selected cases. `median` marks a file without history. */
  readonly source: 'history' | 'median'
}

function median(values: readonly number[]): number {
  const sorted = [...values].sort((left, right) => left - right)
  const middle = Math.floor(sorted.length / 2)
  return sorted.length % 2 === 1 ? sorted[middle]! : (sorted[middle - 1]! + sorted[middle]!) / 2
}

/**
 * Estimate each selected file from its recorded mean case duration and its selected case count.
 * A filtered run, such as a --last-failed run, then estimates only the cases that it selects.
 * A file without history receives the median estimate of the files with history.
 * Return undefined when no selected file has history.
 */
export function estimateFileDurations(coverage: E2ETestCoverage, history: DurationHistory): FileEstimate[] | undefined {
  const selectedCases = new Map<string, number>()
  for (const testCase of coverage.cases)
    selectedCases.set(testCase.file, (selectedCases.get(testCase.file) ?? 0) + 1)
  const recorded = coverage.files.map((file) => {
    const entry = history.get(file)
    return entry === undefined ? undefined : entry.durationMs / entry.cases * (selectedCases.get(file) ?? 0)
  })
  const known = recorded.filter(estimate => estimate !== undefined)
  if (known.length === 0)
    return undefined
  const fallback = median(known)
  return coverage.files.map((file, index) => {
    const estimateMs = recorded[index]
    return estimateMs === undefined ? { file, estimateMs: fallback, source: 'median' } : { file, estimateMs, source: 'history' }
  })
}

export interface PlannedShard {
  /** The shard's files, sorted by path. */
  readonly files: readonly FileEstimate[]
  readonly estimateMs: number
}

/**
 * Assign files to shards with the longest-processing-time rule.
 * Take the files from the longest estimate to the shortest. Ties take the path order.
 * Give each file to the shard with the smallest total. Ties take the shard with fewer files, then the lower index.
 * The file-count tie rule gives every shard a file even when every estimate is zero.
 */
export function assignLongestFirst(estimates: readonly FileEstimate[], count: number): PlannedShard[] {
  if (!Number.isSafeInteger(count) || count < 1)
    throw new Error('The E2E shard count must be a positive integer.')
  const ordered = [...estimates].sort((left, right) => right.estimateMs - left.estimateMs || compareText(left.file, right.file))
  const shards = Array.from({ length: Math.min(count, estimates.length) }, (): { files: FileEstimate[], estimateMs: number } => ({ files: [], estimateMs: 0 }))
  for (const estimate of ordered) {
    let target = shards[0]!
    for (const shard of shards.slice(1)) {
      if (shard.estimateMs < target.estimateMs || (shard.estimateMs === target.estimateMs && shard.files.length < target.files.length))
        target = shard
    }
    target.files.push(estimate)
    target.estimateMs += estimate.estimateMs
  }
  return shards.map(shard => ({ files: shard.files.sort((left, right) => compareText(left.file, right.file)), estimateMs: shard.estimateMs }))
}

// Native loadTestList (playwright/lib/runner/index.js) trims each line, skips a line that starts with "#",
// splits a line on "›" or ">", reads a leading "[project]", and parses a trailing ":line:column" location.
const NATIVE_LOCATION_SUFFIX = /:\d+(?::\d+)?$/u
const TEST_LIST_SYNTAX = /[\0\n\r>›]/u

/** Decide whether a native test list line selects exactly this file, as a path relative to the root directory. */
export function isTestListPath(file: string): boolean {
  return file !== '' && file === file.trim() && !TEST_LIST_SYNTAX.test(file) && !file.startsWith('#') && !file.startsWith('[') && !NATIVE_LOCATION_SUFFIX.test(file)
}

/**
 * Write a native test list that selects exactly these files.
 * A positional filter is a regular expression, so `a.spec.ts` also selects `ba.spec.ts` and `sub/a.spec.ts`.
 * A test list compares each whole relative path instead.
 */
export function testListContent(files: readonly string[]): string {
  if (files.length === 0)
    throw new Error('A test list requires at least one file.')
  const invalid = files.find(file => !isTestListPath(file))
  if (invalid !== undefined)
    throw new Error(`A Playwright test list cannot select the file ${JSON.stringify(invalid)} exactly.`)
  return `${files.join('\n')}\n`
}

export type ShardPlan
  = | { readonly kind: 'static', readonly total: number, readonly reason: string }
    | { readonly kind: 'balanced', readonly shards: readonly PlannedShard[] }

export interface ShardPlanInput {
  readonly coverage: E2ETestCoverage
  readonly workers: number
  readonly balance: BalanceMode
  readonly history: DurationHistoryRead
  /** The caller selected tests with --test-list. Native Playwright accepts one test list only. */
  readonly callerTestList: boolean
}

/** Choose a balanced plan when the history allows one. Otherwise keep the native `--shard=i/N` split. */
export function planShards(input: ShardPlanInput): ShardPlan {
  const { coverage, workers } = input
  if (coverage.files.length === 0)
    throw new Error('An E2E shard plan requires at least one selected file.')
  if (!Number.isSafeInteger(workers) || workers < 1)
    throw new Error('The E2E shard count must be a positive integer.')
  const total = Math.min(workers, coverage.files.length)
  const nativeSplit = (reason: string): ShardPlan => ({ kind: 'static', total, reason })
  if (input.balance === 'off')
    return nativeSplit('--balance=off selects it')
  if (input.callerTestList)
    return nativeSplit('the run gives its own --test-list, and Playwright accepts one test list only')
  if (input.history.status === 'absent')
    return nativeSplit('no duration history exists')
  if (input.history.status === 'unreadable')
    return nativeSplit(`the duration history is unreadable: ${input.history.reason}`)
  const unlisted = coverage.files.find(file => !isTestListPath(file))
  if (unlisted !== undefined)
    return nativeSplit(`a Playwright test list cannot select the file ${JSON.stringify(unlisted)} exactly`)
  const estimates = estimateFileDurations(coverage, input.history.files)
  if (estimates === undefined)
    return nativeSplit('the duration history holds no selected file')
  return { kind: 'balanced', shards: assignLongestFirst(estimates, total) }
}

function seconds(milliseconds: number): string {
  return `${(milliseconds / 1000).toFixed(1)} s`
}

/** Describe the plan before the shards start. */
export function formatShardPlan(plan: ShardPlan, historyPath: string): string {
  if (plan.kind === 'static')
    return `E2E shard plan: the native --shard=i/${plan.total} split. Reason: ${plan.reason}. Duration history: ${historyPath}\n`
  const lines = [`E2E shard plan: ${plan.shards.length} shards, balanced by the duration history at ${historyPath}`]
  for (const [index, shard] of plan.shards.entries()) {
    lines.push(`  Shard ${index + 1}/${plan.shards.length}: ${shard.files.length} ${shard.files.length === 1 ? 'file' : 'files'}, estimated ${seconds(shard.estimateMs)}`)
    for (const file of shard.files)
      lines.push(`    ${file.file}: ${seconds(file.estimateMs)}${file.source === 'median' ? ' (median estimate, no history)' : ''}`)
  }
  return `${lines.join('\n')}\n`
}
