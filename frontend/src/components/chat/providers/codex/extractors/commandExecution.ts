import type { CommandResultSource } from '../../../results/commandResult'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { CODEX_ITEM } from '~/types/toolMessages'
import { commandIsError } from '../../../results/commandResult'

/** Regex to strip shell wrappers like `/bin/zsh -lc '...'` from commands. */
const SHELL_WRAPPER_RE = /^\/bin\/(?:ba|z)?sh\s+-lc\s+'(.+)'$/

/**
 * Build a CommandResultSource from a Codex `commandExecution` item. Reads
 * defensively because `cwd` and `durationMs` are wire-format additions
 * not in the public Rust struct.
 *
 * Returns null when the item type isn't `commandExecution`.
 */
export function codexCommandFromItem(item: Record<string, unknown> | null | undefined): CommandResultSource | null {
  if (!item || item.type !== CODEX_ITEM.COMMAND_EXECUTION)
    return null

  const exitCode = pickNumber(item, 'exitCode')
  return {
    output: pickString(item, 'aggregatedOutput'),
    exitCode,
    durationMs: pickNumber(item, 'durationMs'),
    isError: commandIsError(pickString(item, 'status'), exitCode),
  }
}

/** Strip a shell wrapper like `/bin/zsh -lc '...'` to surface the actual command. */
export function codexUnwrapCommand(rawCommand: string): string {
  return rawCommand.replace(SHELL_WRAPPER_RE, '$1')
}

const DIV_OPEN_RE = /<div\b/g
const DIV_CLOSE_RE = /<\/div>/g
const TOOL_USE_HEADER_CLASS_FRAGMENT = 'toolUseHeader__'

/**
 * Strip our own injected tool-use headers from a Codex `aggregatedOutput`
 * payload. The backend sometimes echoes the rendered tool-use chrome (an
 * HTML `<div>` block) back into the aggregated output stream; this walks the
 * matching depth-counted div-block and drops it. Shared by the renderer
 * (commandExecution.tsx) and command-result utilities so rendered output stays
 * consistent.
 */
export function stripToolUseHeaderFromOutput(output: string): string {
  if (!output.includes(TOOL_USE_HEADER_CLASS_FRAGMENT))
    return output

  const lines = output.split('\n')
  const kept: string[] = []

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!line.includes(TOOL_USE_HEADER_CLASS_FRAGMENT)) {
      kept.push(line)
      continue
    }

    // Strip the matching depth-counted <div> block. The header's opening <div> can
    // sit on the PREVIOUS line (a pretty-printed dump puts the class="...toolUseHeader__
    // ..." token on its own line, separate from the `<div`) -- pop it and seed depth
    // with its (unbalanced) open. Only pull in a previous line that is genuinely an
    // UNBALANCED opener (more `<div` than `</div>`): a self-contained, balanced
    // `<div ...></div>` of unrelated content before the header is NOT part of it, and
    // popping it would delete that line AND, via the leftover seeded depth, swallow a
    // line of real output below the header.
    let depth = 0
    const prev = kept.at(-1)
    if (prev !== undefined) {
      const prevOpen = (prev.match(DIV_OPEN_RE) || []).length
      const prevClose = (prev.match(DIV_CLOSE_RE) || []).length
      if (prevOpen > prevClose) {
        kept.pop()
        depth += prevOpen - prevClose
      }
    }
    // Count THIS line's own tags INCLUSIVELY before advancing: a header whose <div> and
    // </div> share one line is already balanced (depth 0) and consumes only itself.
    // Seeding depth=1 and skipping this line left a single-line header unbalanced, so
    // the walk ran to EOF and deleted the real command output below it.
    depth += (line.match(DIV_OPEN_RE) || []).length
    depth -= (line.match(DIV_CLOSE_RE) || []).length
    while (depth > 0 && i + 1 < lines.length) {
      const current = lines[++i]
      depth += (current.match(DIV_OPEN_RE) || []).length
      depth -= (current.match(DIV_CLOSE_RE) || []).length
    }
  }

  return kept.join('\n')
}
