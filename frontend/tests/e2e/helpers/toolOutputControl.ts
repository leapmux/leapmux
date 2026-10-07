import type { FSWatcher } from 'node:fs'
import { Buffer } from 'node:buffer'
import { existsSync, mkdtempSync, statSync, watch, writeFileSync } from 'node:fs'
import { writeFile } from 'node:fs/promises'
import { dirname, isAbsolute, join } from 'node:path'
import { uniqueMarker } from './shellArguments'

const FILE_SIGNAL_DEADLINE_MS = 30_000
const NATIVE_OUTPUT_DEADLINE_MS = 600_000

/**
 * How often a held native command looks for its release file, in milliseconds.
 *
 * The command polls. It does not watch the directory. A native client can run the
 * command in a sandbox. Codex does so on macOS, and there `fs.watch` fails with EMFILE
 * ("too many open files, watch"). A watching command then exited with code 1 straight
 * after its first output. A file check needs no watch.
 */
export const RELEASE_POLL_MS = 25

/**
 * The padding line after each segment's marker, in characters.
 *
 * Large enough to move a byte counter by a visible step ("4.0 KB"). The marker
 * comes FIRST, so a completed row that a reader has not expanded still shows
 * both markers in its first lines.
 */
const SEGMENT_PADDING_CHARS = 4096

/**
 * The end of a segment's padding, which a live view of the running command shows.
 *
 * The Worker broadcasts only the last 2 KiB of a running call's output
 * (`runningToolTailBytes` in backend/internal/worker/service/generation_progress.go),
 * and the padding after the marker is longer than that. A live view that draws
 * this broadcast therefore never shows the marker. It shows the padding, and a
 * collapsed row clips that line to its first 240 characters
 * (`COLLAPSED_LINE_CHAR_CAP`), so the probe stays shorter than that cap.
 */
const LIVE_TAIL_PROBE_CHARS = 128

export interface ToolOutputControl {
  command: string
  scriptPath: string
  firstMarker: string
  secondMarker: string
  /** Text that the live view of the first segment shows. See {@link LIVE_TAIL_PROBE_CHARS}. */
  firstLiveTail: string
  /** Text that the live view of the second segment shows. See {@link LIVE_TAIL_PROBE_CHARS}. */
  secondLiveTail: string
  waitForFirstOutput: () => Promise<void>
  waitForSecondOutput: () => Promise<void>
  /** Let a command that holds its first output (see {@link ToolOutputOptions.holdFirstOutput}) write it. */
  releaseStartOutput: () => Promise<void>
  releaseFirstOutput: () => Promise<void>
  releaseFinalOutput: () => Promise<void>
}

export interface ToolOutputOptions {
  /**
   * Hold the first output segment until `releaseStartOutput`.
   *
   * Without this the command writes the first segment as soon as it runs. A native
   * client streams only the output that a command writes after the client attached
   * to the command's output. Codex attaches a few milliseconds after it starts the
   * command, and drops anything written before that, so the first segment of a
   * command that does not hold it can be lost to a live view. A spec that needs the
   * first segment live holds it until the client reports that the command started.
   */
  holdFirstOutput?: boolean
}

/** Wait for a file signal. A test can supply a watcher that sends controlled notifications. */
export async function waitForFileSignal(path: string, watchDirectory: (directory: string) => Pick<FSWatcher, 'on' | 'close'> = watch): Promise<void> {
  if (existsSync(path))
    return
  await new Promise<void>((resolve, reject) => {
    const subscription = watchDirectory(dirname(path))
    const deadline = setTimeout(() => finish(new Error('The native output signal did not arrive.')), FILE_SIGNAL_DEADLINE_MS)
    // macOS starts the FSEvents stream after watch() returns. A file in that interval can produce no event.
    // Check the file also, so a missed event cannot hold the wait until its deadline.
    const checkTimer = setInterval(inspect, RELEASE_POLL_MS)
    let finished = false
    function finish(error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      clearInterval(checkTimer)
      subscription.close()
      if (error)
        reject(error)
      else
        resolve()
    }
    function inspect() {
      if (existsSync(path))
        finish()
    }
    subscription.on('change', inspect)
    subscription.on('error', finish)
    inspect()
  })
}

/** Hold a real native command between two independently observable output segments. */
export function createToolOutputControl(workingDir: string, markers?: { first: string, second: string }, options: ToolOutputOptions = {}): ToolOutputControl {
  if (!isAbsolute(workingDir) || !statSync(workingDir).isDirectory())
    throw new Error('The native output command needs an absolute working directory.')
  if (markers && (!markers.first || !markers.second || markers.first === markers.second))
    throw new Error('The native output command needs two distinct nonempty markers.')
  const directory = mkdtempSync(join(workingDir, 'tool-output-control-'))
  const firstReady = join(directory, 'first-ready')
  const secondReady = join(directory, 'second-ready')
  const releaseStart = join(directory, 'release-start')
  const releaseFirst = join(directory, 'release-first')
  const releaseFinal = join(directory, 'release-final')
  const firstMarker = markers?.first ?? uniqueMarker('NATIVEFIRST')
  const secondMarker = markers?.second ?? uniqueMarker('NATIVESECOND')
  const firstPadding = 'x'.repeat(SEGMENT_PADDING_CHARS)
  const secondPadding = 'y'.repeat(SEGMENT_PADDING_CHARS)
  const source = `
const fs = require('node:fs')
const releaseStart = ${JSON.stringify(releaseStart)}
const releaseFirst = ${JSON.stringify(releaseFirst)}
const releaseFinal = ${JSON.stringify(releaseFinal)}
let firstWritten = false
let secondWritten = false
let finished = false
const poll = setInterval(inspect, ${RELEASE_POLL_MS})
const deadline = setTimeout(() => {
  finished = true
  process.exitCode = 1
  clearInterval(poll)
}, ${NATIVE_OUTPUT_DEADLINE_MS})
function writeFirst() {
  firstWritten = true
  fs.writeSync(1, ${JSON.stringify(`${firstMarker}\n${firstPadding}\n`)})
  fs.writeFileSync(${JSON.stringify(firstReady)}, 'ready')
}
function inspect() {
  if (finished) return
  if (!firstWritten && fs.existsSync(releaseStart)) writeFirst()
  if (firstWritten && !secondWritten && fs.existsSync(releaseFirst)) {
    secondWritten = true
    fs.writeSync(1, ${JSON.stringify(`${secondMarker}\n${secondPadding}\n`)})
    fs.writeFileSync(${JSON.stringify(secondReady)}, 'ready')
  }
  if (secondWritten && fs.existsSync(releaseFinal)) {
    finished = true
    clearTimeout(deadline)
    clearInterval(poll)
  }
}
${options.holdFirstOutput ? '' : 'writeFirst()\n'}inspect()
`
  const scriptPath = join(directory, 'output.cjs')
  writeFileSync(scriptPath, source)
  const encoded = Buffer.from(source).toString('base64')
  return {
    command: `node -e "eval(Buffer.from('${encoded}','base64').toString())"`,
    scriptPath,
    firstMarker,
    secondMarker,
    firstLiveTail: firstPadding.slice(-LIVE_TAIL_PROBE_CHARS),
    secondLiveTail: secondPadding.slice(-LIVE_TAIL_PROBE_CHARS),
    waitForFirstOutput: () => waitForFileSignal(firstReady),
    waitForSecondOutput: () => waitForFileSignal(secondReady),
    releaseStartOutput: () => writeFile(releaseStart, 'start'),
    releaseFirstOutput: () => writeFile(releaseFirst, 'continue'),
    releaseFinalOutput: () => writeFile(releaseFinal, 'finish'),
  }
}
