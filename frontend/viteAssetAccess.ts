import { fileURLToPath } from 'node:url'
import { searchForWorkspaceRoot } from 'vite'

const frontendDirectory = fileURLToPath(new URL('.', import.meta.url))

/** The app and its tests read provider SVG assets from their one source directory. */
export const VITE_SOURCE_DIRECTORIES = [
  searchForWorkspaceRoot(frontendDirectory),
  fileURLToPath(new URL('../icons/agents', import.meta.url)),
]
