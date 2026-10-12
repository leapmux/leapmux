/** Native fields that only the browser reads. */

/** Native fields for tool result pointers. */
export const DIRAC_OUTPUT_REFERENCE = {
  TextPrefix: 'Full output saved to: ',
  DirectoryName: 'dirac',
  FilePrefix: 'large-output-',
  FileExtension: '.log',
} as const

/** The native raw input field that identifies the tool. */
export const DIRAC_RAW_INPUT_TOOL_FIELD = 'tool'
