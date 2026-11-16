import type { Accessor } from 'solid-js'
import type { FileImageSizeAnswer, FileImageSource, SettledFileImageSize } from '../fileImageResolver'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { createEffect, createSignal, on, onCleanup, untrack } from 'solid-js'
import { settledFileImageSize } from '../fileImageResolver'

/** What createFileImageHeaderSize reads from the view of one image. */
export interface FileImageHeaderSizeOptions {
  /** The path of the image file, or undefined for an image that has no file. */
  path: Accessor<string | undefined>
  /** Whether the source of the image states its size. The header then needs no read. */
  stated: Accessor<boolean>
  /** The image files of the row, or undefined for a view that has no actions. */
  files: Accessor<FileImageSource | undefined>
}

/**
 * The size of a file image, from the first bytes of its file, as the view of the image
 * knows it (see FileImageSizeAnswer). The view asks for it whatever `deferLoad` says, so
 * it can reserve the image box before it reads the image: a hidden premeasure copy then
 * measures the height that the row keeps.
 *
 * The answer is `unknown` while no answer exists, and for an image that has no file or
 * no image files. A size read that failed gives `none`, as a header with no size does.
 *
 * The answer of the view's own ask comes first, and the view keeps it. Before that answer
 * arrives, the size cache answers, with no subscription: a size that the cache holds
 * shows in the first render. So nothing that the resolver learns or drops later changes
 * what a mounted view decided, in particular whether it reads its image.
 *
 * Call it once, while the view sets up: the read runs in an effect of the view's owner.
 */
export function createFileImageHeaderSize(options: FileImageHeaderSizeOptions): Accessor<FileImageSizeAnswer> {
  const [sizeRead, setSizeRead] = createSignal<{ path: string, answer: SettledFileImageSize }>()

  createEffect(on(
    () => [
      options.path(),
      options.files(),
      options.stated(),
    ] as const,
    ([path, files, stated]) => {
      // A size that the source states never yields to the header, so the header needs no read.
      if (!path || stated || !files)
        return
      // The view keeps its own copy of a size that the cache holds: the cache can drop the
      // size later (its limit, a clear), and the box of a row that shows must stay.
      const cached = untrack(() => files.peekSize(path))
      if (cached.kind !== 'unknown')
        setSizeRead({ path, answer: cached })
      let current = true
      onCleanup(() => {
        current = false
      })
      const settle = (size: ImageDimensions | undefined): void => {
        if (current) {
          setSizeRead({
            path,
            answer: settledFileImageSize(size),
          })
        }
      }
      // The view asks for a size that the cache holds also. The resolver answers such an
      // ask with no request, and counts it: it drops the sizes that no view asked for the
      // longest. The read never rejects by its contract. A rejection still counts as no size.
      void files.loadSize(path).then(settle, () => settle(undefined))
    },
  ))

  return () => {
    const path = options.path()
    if (!path)
      return { kind: 'unknown' }
    const read = sizeRead()
    if (read?.path === path)
      return read.answer
    return untrack(() => options.files()?.peekSize(path)) ?? { kind: 'unknown' }
  }
}
