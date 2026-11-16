/** One labelled fact a tool row lists above its body. */
export interface ToolMetadataEntry {
  label: string
  value: string
  /**
   * The value is a file system path. The row shows it relative to the agent's working
   * directory, as it shows every other path of a call.
   */
  path?: boolean
}
