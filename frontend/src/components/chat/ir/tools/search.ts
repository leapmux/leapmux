/**
 * What a search LOOKED for, and what it found.
 *
 * Three kinds share this pair -- `search`, `glob` and `grep` -- so it is declared
 * here, at the base name, and `glob.ts` and `grep.ts` alias it. The KIND is the
 * call's own, and it is what picks the renderer.
 */
export interface SearchRequest { pattern: string, paths: string[] }
export type { SearchResult } from '../searchResult'
