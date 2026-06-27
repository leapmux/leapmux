/** Maximum visual rows shown for a collapsed command input summary. */
export const COLLAPSED_COMMAND_INPUT_ROWS = 3

/**
 * Long single-line commands must be expandable even though they contain no hard
 * newline. This threshold intentionally approximates "likely to exceed three
 * visual rows" without reading layout; DOM measurement supplies the exact height
 * after render.
 */
export const COMMAND_INPUT_EXPAND_CHAR_THRESHOLD = 240

/**
 * Highlight short command inputs only. Shiki tokenization cost scales with long
 * pasted commands and creates many DOM spans; long commands remain cheap plain
 * pre-wrap text, especially in expanded form.
 */
export const COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT = 1000

/** True when the command contains more than one hard line. */
export function isMultiLineCommand(command: string | null | undefined): boolean {
  return !!command && command.includes('\n')
}

/**
 * Whether command input needs an expand affordance. Multi-line commands always
 * need it because the collapsed summary clips after three rows; long single-line
 * commands need it because `pre-wrap` + `break-all` can wrap into many rows.
 */
export function commandInputNeedsExpansion(command: string | null | undefined): boolean {
  return !!command && (isMultiLineCommand(command) || command.length > COMMAND_INPUT_EXPAND_CHAR_THRESHOLD)
}
