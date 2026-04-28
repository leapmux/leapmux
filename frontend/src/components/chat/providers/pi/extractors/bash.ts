import type { CommandResultSource } from '../../../results/commandResult'
import { isObject, pickString } from '~/lib/jsonPick'
import { PI_TOOL } from '../protocol'
import { piExtractTool } from './toolCommon'

export interface PiBashCommand {
  command: string
  /** Process output, with Pi's trailing status marker stripped when present. */
  output: string
  /**
   * Exit code parsed from a `Command exited with code N` marker that Pi
   * appends on non-zero exits. Null on success (Pi doesn't surface zero
   * exits) and on cancel/timeout (those use a different marker).
   */
  exitCode: number | null
  cancelled: boolean
  truncated: boolean
  fullOutputPath: string | null
  isError: boolean
}

/**
 * Pi's bash tool ([bash.ts:381-405](pi-mono/packages/coding-agent/src/core/tools/bash.ts))
 * appends a status line to `result.text` on three error paths:
 *
 *   non-zero exit → `…\n\nCommand exited with code N`
 *   user abort    → `…\n\nCommand aborted`        (or just the marker if no output)
 *   timeout       → `…\n\nCommand timed out after N seconds`
 *
 * The marker is always at the end of the text and is only appended on the
 * reject path (so `isError: true`). The regex anchors to end-of-string
 * with a `\n\n` separator (or start-of-string when output was empty), and
 * the parser only fires when `isError` is true — that combination defends
 * against a successful process that legitimately prints the same string,
 * and against an erroring process that prints a marker-shaped line earlier
 * in its output (Pi's actual marker still wins because it's the trailing
 * one).
 */
const PI_BASH_MARKER_REGEX = /(?:^|\n\n)(Command aborted|Command timed out after \d+ seconds|Command exited with code (-?\d+))\s*$/

interface PiBashMarker {
  /** Output with the trailing marker stripped (no marker → unchanged). */
  output: string
  exitCode: number | null
  cancelled: boolean
}

function parsePiBashMarker(text: string, isError: boolean): PiBashMarker {
  if (!isError)
    return { output: text, exitCode: null, cancelled: false }
  const m = PI_BASH_MARKER_REGEX.exec(text)
  if (!m)
    return { output: text, exitCode: null, cancelled: false }
  const head = m[1]
  return {
    output: text.slice(0, m.index),
    exitCode: m[2] != null ? Number(m[2]) : null,
    cancelled: head.startsWith('Command aborted') || head.startsWith('Command timed out'),
  }
}

/**
 * Build a Pi bash result from a tool_execution_end (or update) payload.
 * Returns null when the payload is not a bash tool execution.
 */
export function extractPiBash(payload: Record<string, unknown> | null | undefined): PiBashCommand | null {
  const tool = piExtractTool(payload ?? undefined)
  if (!tool || tool.toolName !== PI_TOOL.Bash)
    return null

  const args = tool.args
  const command = pickString(args, 'command')

  const result = tool.result ?? tool.partialResult
  const rawOutput = result?.text ?? ''
  const details = result?.details ?? {}
  const isError = tool.isError
  const marker = parsePiBashMarker(rawOutput, isError)
  // Pi's BashToolDetails wraps the truncation flag inside a nested object
  // (`details.truncation.truncated`), unlike most other providers that put
  // it on the top-level details.
  const truncation = isObject(details.truncation) ? details.truncation : null

  return {
    command,
    output: marker.output,
    exitCode: marker.exitCode,
    cancelled: marker.cancelled,
    truncated: truncation?.truncated === true,
    fullOutputPath: typeof details.fullOutputPath === 'string' ? (details.fullOutputPath as string) : null,
    isError,
  }
}

/** Adapt a PiBashCommand to the shared CommandResultSource shape. */
export function piBashToCommandSource(bash: PiBashCommand): CommandResultSource {
  return {
    output: bash.output,
    exitCode: bash.exitCode ?? undefined,
    interrupted: bash.cancelled,
    // The marker parser already determined error state from `isError`; we
    // don't double-classify on exitCode (it's only set when the marker
    // fired, which means isError was already true).
    isError: bash.isError || bash.cancelled,
  }
}
