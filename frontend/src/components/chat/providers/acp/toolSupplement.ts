import { ACP_SUPPLEMENT, ACP_SUPPLEMENT_IDENTITY, ACP_TERMINAL_RESULT } from '~/generated/contracts/acp-protocol'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

/**
 * The output ONE terminal produced, as the worker stored it.
 *
 * This is the browser's half of `acpTerminalResult` in the worker. The four field
 * names are contract constants (contracts/acp-protocol.json `terminalResult`) and the
 * Go tags are pinned to the same table by TestSupplementTagsMatchTheContract, so
 * neither language can rename one without the other.
 *
 * `exitCode` and `signal` are EXCLUSIVE: the worker derives them from one
 * `os.ProcessState`, which answers with a code or with a signal and never both.
 */
export type ACPTerminalOutput = {
  output: string
  /** The host kept only a suffix of the stream. */
  truncated: boolean
} & ACPTerminalExit

/**
 * How the terminal's process ended: with its own code, or with the signal that ended
 * it. Never both, and a terminal still running states neither.
 *
 * The worker derives the pair from ONE `os.ProcessState`, which answers one way or the
 * other, so the exclusivity is a property of the wire and belongs in the type that
 * reads it. A terminal the host killed on purpose reaches the reader as a cancelled
 * row; what is left here is a process the OS ended for its own reasons, which used to
 * draw with no code and no reason at all.
 */
export type ACPTerminalExit
  = | { exitCode?: number, signal?: never }
    | { exitCode?: never, signal: string }

/**
 * The envelope a tool row keeps beside the agent's own frame, after the identity gate.
 *
 * It stays a RECORD rather than a closed interface because it is open by design, the
 * same way the Go `acpToolSupplement` is: a provider adds its own key beside the
 * shared ones -- Cursor stores an extension frame under `cursorExtension`, and both
 * Cursor and Reasonix store a native record under `rawOutput`. The payload readers
 * below are what keep the reads from being bare `pickObject(x, 'terminals')` guesses.
 */
export type ACPToolSupplement = Record<string, unknown>

/**
 * The supplement of ONE tool row, or undefined when the stored envelope belongs to
 * another frame.
 *
 * PRESENCE and value must agree on every identity key, which is the same gate the
 * worker applies before it resolves. A supplement is stored beside one frame: without
 * this a retained row could take another call's output, or a snapshot of its own state
 * from before the status moved on.
 */
export function acpToolSupplement(
  original: Record<string, unknown>,
  supplemental: unknown,
): ACPToolSupplement | undefined {
  if (!isObject(supplemental))
    return undefined
  // The frame must NAME a call. A supplement belongs beside exactly one tool call, so
  // an envelope with no id matches nothing -- and without this test two different
  // calls that both omitted the id would each take the other's supplement.
  if (!identityValue(original[ACP_SUPPLEMENT_IDENTITY.ToolCallID]))
    return undefined
  for (const key of Object.values(ACP_SUPPLEMENT_IDENTITY)) {
    // `hasOwn`, never `in`: the worker compares map membership, which has no prototype
    // chain, so an own-property test is the only one that answers the same question.
    const present = Object.hasOwn(original, key)
    if (present !== Object.hasOwn(supplemental, key))
      return undefined
    if (!present)
      continue
    const frame = identityValue(original[key])
    const stored = identityValue(supplemental[key])
    if (frame === null || stored === null || frame !== stored)
      return undefined
  }
  return supplemental
}

/**
 * One identity field as the WORKER decodes it, or null for a value its decode refuses.
 *
 * The worker unmarshals each identity key into a Go `string`, where a JSON `null` is a
 * no-op that leaves the field empty and every other non-string type is an error that
 * refuses the whole supplement. A plain `!==` here accepted an identity of `5` that
 * the worker refused, so the worker's own extractors and the row on screen could read
 * two different supplements.
 */
function identityValue(value: unknown): string | null {
  if (value === null)
    return ''
  return typeof value === 'string' ? value : null
}

/** The state fields the original frame did not carry. */
export function acpSupplementProtocol(supplement: ACPToolSupplement | undefined): Record<string, unknown> | undefined {
  return pickObject(supplement, ACP_SUPPLEMENT.Protocol, undefined)
}

/** The native record a provider read out of its own transcript. */
export function acpSupplementRawOutput(supplement: ACPToolSupplement | undefined): Record<string, unknown> | undefined {
  return pickObject(supplement, ACP_SUPPLEMENT.RawOutput, undefined)
}

/**
 * Every terminal output the supplement carries, by terminal id.
 *
 * A MAP rather than a record, because the agent chooses the ids. A plain object
 * answers `terminals['toString']` with a function from `Object.prototype`, and the
 * caller then reads an entry whose `output` is `undefined` through a field the model
 * declares as `string` -- which the command body dereferences and crashes on. A Map
 * holds only what was put in it, so no id can reach anything the supplement did not
 * carry.
 *
 * An entry whose `output` is not a string is dropped rather than coerced: the caller
 * distinguishes a terminal it can read from one the host no longer holds, and an
 * entry turned into `''` would read as a command that printed nothing.
 */
export function acpSupplementTerminals(supplement: ACPToolSupplement | undefined): Map<string, ACPTerminalOutput> {
  const stored = pickObject(supplement, ACP_SUPPLEMENT.Terminals)
  const terminals = new Map<string, ACPTerminalOutput>()
  for (const [id, entry] of Object.entries(stored ?? {})) {
    if (!isObject(entry))
      continue
    const output = entry[ACP_TERMINAL_RESULT.Output]
    if (typeof output !== 'string')
      continue
    const exitCode = pickNumber(entry, ACP_TERMINAL_RESULT.ExitCode, undefined)
    const signal = pickString(entry, ACP_TERMINAL_RESULT.Signal)
    terminals.set(id, {
      output,
      truncated: pickBoolean(entry, ACP_TERMINAL_RESULT.Truncated) ?? false,
      // The signal answers only when no code does, exactly as the worker wrote it,
      // and a terminal still running states neither.
      ...(signal ? { signal } : exitCode !== undefined ? { exitCode } : {}),
    })
  }
  return terminals
}
