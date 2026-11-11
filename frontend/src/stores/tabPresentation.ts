import type { FileDiffBase, FileDisplayMode, FileViewMode } from './tab.types'
import { TabDisplayMode, TabFileViewMode } from '~/generated/proto/leapmux/v1/tab_creation_pb'

// The browser interprets initial presentation. The Hub stores these values as
// opaque registers. Wire value zero supplies no initial preference.
const DISPLAY_MODES = createModeCodec<FileDisplayMode, TabDisplayMode>({
  render: TabDisplayMode.RENDER,
  source: TabDisplayMode.SOURCE,
  split: TabDisplayMode.SPLIT,
})
const FILE_VIEW_MODES = createModeCodec<FileViewMode, TabFileViewMode>({
  'working': TabFileViewMode.WORKING,
  'head': TabFileViewMode.HEAD,
  'staged': TabFileViewMode.STAGED,
  'unified-diff': TabFileViewMode.UNIFIED_DIFF,
  'split-diff': TabFileViewMode.SPLIT_DIFF,
})

function createModeCodec<Mode extends string, ProtocolMode extends number>(values: Record<Mode, ProtocolMode>) {
  const reverse = new Map<number, Mode>()
  for (const mode in values)
    reverse.set(values[mode], mode)
  return {
    encode: (mode: Mode): ProtocolMode => values[mode],
    decode: (value: number | undefined): Mode | undefined => value === undefined
      ? undefined
      : reverse.get(value),
  }
}

export const encodeDisplayMode = DISPLAY_MODES.encode
export const decodeDisplayMode = DISPLAY_MODES.decode
export const encodeFileViewMode = FILE_VIEW_MODES.encode
export const decodeFileViewMode = FILE_VIEW_MODES.decode

export function decodeFileDiffBase(value: string | undefined): FileDiffBase | undefined {
  return value === 'head-vs-working' || value === 'head-vs-staged'
    ? value
    : undefined
}
