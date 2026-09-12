import type { DirectoryResultSource } from '../../results/directoryResult'
import { formatBytes } from '~/lib/formatBytes'

/** Separate the ACP clip notice from native directory entries and byte sizes. */
export function reasonixDirectoryOutput(output: string): DirectoryResultSource {
  // Complete directory output ends with a newline. A file can otherwise resemble the clip notice.
  const clip = output.endsWith('\n') ? null : /\n…\(\d+ more chars truncated\)$/.exec(output)
  const text = clip ? output.slice(0, clip.index) : output
  const empty = ['', '(empty directory)', '(empty directory tree)'].includes(text.trim())
  const entries = empty
    ? []
    : text.trimEnd().split('\n').filter(Boolean).map((line) => {
        const size = /\t(-?\d+)$/.exec(line)
        if (!size)
          return { path: line }
        const bytes = Number(size[1])
        return { path: line.slice(0, size.index), detail: Number.isSafeInteger(bytes) && bytes >= 0 ? formatBytes(bytes) : undefined }
      })
  return { entries, truncated: !!clip, notice: clip?.[0].slice(1) }
}
