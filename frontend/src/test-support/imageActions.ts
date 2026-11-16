import type { FileImageSource } from '~/components/chat/fileImageResolver'
import type { ImageRenderActions } from '~/components/chat/renderContext'
import { NO_FILE_IMAGES } from '~/components/chat/fileImageResolver'
import { imageActionsFrom } from '~/components/chat/renderContext'

/** What a test sets of the host of imageActionsFrom. It can set a part of the image files. */
export type TestImageActionsHost = Partial<Omit<Parameters<typeof imageActionsFrom>[0], 'files'>> & {
  files?: Partial<FileImageSource>
}

/**
 * The image actions of a row in the list near the view, through the one assembly
 * (imageActionsFrom): no read is deferred, and the host has no source for an image file
 * (NO_FILE_IMAGES). A test sets only the members that it needs.
 */
export function testImageActions(overrides: TestImageActionsHost = {}): ImageRenderActions {
  const { files, ...host } = overrides
  return imageActionsFrom({
    deferLoad: () => false,
    premeasurePass: () => false,
    ...host,
    files: {
      ...NO_FILE_IMAGES,
      ...files,
    },
  })
}
