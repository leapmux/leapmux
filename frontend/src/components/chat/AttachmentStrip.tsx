import type { Accessor, Component } from 'solid-js'
import type { FileAttachment } from './attachments'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import X from 'lucide-solid/icons/x'
import { For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { isImageMimeType } from './attachments'
import * as styles from './AttachmentStrip.css'

export interface AttachmentStripProps {
  attachments: Accessor<FileAttachment[]>
  onRemove: (id: string) => void
}

export const AttachmentStrip: Component<AttachmentStripProps> = (props) => {
  let stripRef: HTMLDivElement | undefined

  const handleWheel = (e: WheelEvent) => {
    if (!stripRef || Math.abs(e.deltaY) < Math.abs(e.deltaX))
      return
    e.preventDefault()
    stripRef.scrollLeft += e.deltaY
  }

  return (
    <Show when={props.attachments().length > 0}>
      <div
        ref={stripRef}
        class={styles.strip}
        onWheel={handleWheel}
        data-testid="attachment-strip"
      >
        <For each={props.attachments()}>
          {attachment => (
            <div class={styles.pill} data-testid="attachment-pill">
              <span class={styles.pillIcon}>
                <Icon
                  icon={isImageMimeType(attachment.mimeType) ? FileImageIcon : FileIcon}
                  size="xs"
                />
              </span>
              <Tooltip text={attachment.filename}>
                <span class={styles.pillFilename}>{attachment.filename}</span>
              </Tooltip>
              <Tooltip text="Remove attachment" ariaLabel>
                <button
                  class={styles.removeButton}
                  onClick={() => props.onRemove(attachment.id)}
                  data-testid="attachment-remove"
                >
                  <Icon icon={X} size="xs" />
                </button>
              </Tooltip>
            </div>
          )}
        </For>
      </div>
    </Show>
  )
}
