/** Goose code-execution tokens that only the frontend interprets. */
export const GOOSE_CODE_EXECUTION = {
  Extension: 'code_execution',
  Tool: 'execute_javascript',
} as const

/** Native shell notices identify retained output files. */
export const GOOSE_OUTPUT_REFERENCE = {
  Notice: /^\[Output exceeded [1-9]\d* (?:line|byte) limit \([1-9]\d* (?:lines|bytes) total\)\. Full output saved to (.+)\. Read it with .+ up to [1-9]\d* lines at a time\.\]$/u,
  Filename: /^(?:stdout|stderr|output)-\d+$/u,
} as const
