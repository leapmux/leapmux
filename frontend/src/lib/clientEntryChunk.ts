/**
 * The file name of the SPA client entry chunk.
 *
 * SolidStart builds the client from `src/entry-client.tsx`, and Rolldown names
 * an entry chunk after its input file: `entry-client-<hash>.js`. The hash uses
 * the URL-safe alphabet, so it can hold `-` and `_`.
 */
const CLIENT_ENTRY_CHUNK_RE = /^entry-client-[\w-]+\.js$/

/** True when `basename`, the last path segment of a URL, names the client entry chunk. */
export function isClientEntryChunk(basename: string): boolean {
  return CLIENT_ENTRY_CHUNK_RE.test(basename)
}
