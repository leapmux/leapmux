import type { ExternalApp } from '~/api/platformBridge'

/**
 * The two shapes an `ExternalApp` fixture ever takes.
 *
 * Six test files built the same literals by hand, and two of them already
 * declared byte-identical local helpers. The ids matter: `isFileManager` reads
 * the generated contract table, so a fixture's KIND follows from the id it
 * carries and cannot be set independently.
 */

/** A detected editor. Any id the contract lists as one will do. */
export function editorApp(id: string, displayName: string): ExternalApp {
  return { id, displayName }
}

/**
 * The operating system's own file manager.
 *
 * Always present on a real machine, and it leads the detected list, so most
 * tests that care about ordering or grouping need exactly this row. The id is
 * fixed, because that is what makes it the file manager.
 */
export function fileManagerApp(displayName = 'Finder'): ExternalApp {
  return { id: 'file-manager', displayName }
}
