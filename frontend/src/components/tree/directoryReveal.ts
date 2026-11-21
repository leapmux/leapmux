/**
 * Request to expand, scroll to, focus, and highlight a directory row in
 * `DirectoryTree` (used when activating a local directory link in Markdown).
 */
export interface DirectoryRevealRequest {
  /** Target directory path in the tree root's logical spelling. */
  path: string
  /** Optional canonicalized path from StatFileResponse for symlink/macOS child matching. */
  canonicalPath?: string
  /** Monotonic counter so repeated clicks on the same directory re-trigger focus and cue. */
  nonce: number
}
