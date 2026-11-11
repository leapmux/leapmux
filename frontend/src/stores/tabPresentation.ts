import type { FileDiffBase, FileDisplayMode, FileViewMode } from './tab.types'

// The browser interprets initial presentation. The Hub stores these values as
// opaque registers. Wire value zero supplies no initial preference.
const DISPLAY_MODES = ['render', 'source', 'split'] as const satisfies readonly FileDisplayMode[]
const FILE_VIEW_MODES = [
  'working',
  'head',
  'staged',
  'unified-diff',
  'split-diff',
] as const satisfies readonly FileViewMode[]

export function encodeDisplayMode(mode: FileDisplayMode): number {
  return DISPLAY_MODES.indexOf(mode) + 1
}

export function decodeDisplayMode(value: number | undefined): FileDisplayMode | undefined {
  return DISPLAY_MODES[(value ?? 0) - 1]
}

export function encodeFileViewMode(mode: FileViewMode): number {
  return FILE_VIEW_MODES.indexOf(mode) + 1
}

export function decodeFileViewMode(value: number | undefined): FileViewMode | undefined {
  return FILE_VIEW_MODES[(value ?? 0) - 1]
}

export function decodeFileDiffBase(value: string | undefined): FileDiffBase | undefined {
  return value === 'head-vs-working' || value === 'head-vs-staged'
    ? value
    : undefined
}
