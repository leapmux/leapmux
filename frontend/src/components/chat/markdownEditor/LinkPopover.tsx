import type { Accessor, Component } from 'solid-js'
import Link2 from 'lucide-solid/icons/link-2'
import { createUniqueId } from 'solid-js'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { positionPopoverAbove } from '~/lib/popoverPosition'
import * as styles from './MarkdownEditor.css'

export interface LinkPopoverProps {
  activeLink: Accessor<boolean>
  linkPopoverOpen: Accessor<boolean>
  setLinkPopoverOpen: (open: boolean) => void
  linkUrl: Accessor<string>
  setLinkUrl: (url: string) => void
  handleLinkSubmit: () => void
  handleLinkRemove: () => void
}

export const LinkPopover: Component<LinkPopoverProps> = (props) => {
  const linkPopoverId = createUniqueId()
  let linkPopoverRef: HTMLDivElement | undefined
  let linkTriggerRef: HTMLButtonElement | undefined
  let linkInputRef: HTMLInputElement | undefined

  const handleLinkTriggerClick = () => {
    if (props.activeLink()) {
      props.handleLinkRemove()
      return
    }
    if (props.linkPopoverOpen()) {
      linkPopoverRef?.hidePopover()
      return
    }
    props.setLinkPopoverOpen(true)
    linkPopoverRef?.showPopover()
    if (linkPopoverRef && linkTriggerRef) {
      requestAnimationFrame(() => {
        positionPopoverAbove(linkTriggerRef!, linkPopoverRef!)
      })
    }
  }

  const handleLinkPopoverToggle = (e: Event) => {
    const toggleEvent = e as ToggleEvent
    if (toggleEvent.newState === 'open') {
      linkInputRef?.focus()
    }
    else {
      props.setLinkPopoverOpen(false)
      props.setLinkUrl('')
    }
  }

  return (
    <>
      <IconButton
        ref={linkTriggerRef}
        icon={Link2}
        size="md"
        state={props.activeLink() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-link"
        title="Link"
        onClick={handleLinkTriggerClick}
      />
      <div
        popover="auto"
        id={linkPopoverId}
        class={styles.linkPopover}
        data-testid="link-popover"
        ref={linkPopoverRef}
        onToggle={handleLinkPopoverToggle}
      >
        <form
          onSubmit={(e) => {
            e.preventDefault()
            props.handleLinkSubmit()
            linkPopoverRef?.hidePopover()
          }}
          class={styles.linkPopoverForm}
        >
          <input
            type="url"
            class={styles.linkPopoverInput}
            placeholder="https://..."
            value={props.linkUrl()}
            onInput={e => props.setLinkUrl(e.currentTarget.value)}
            ref={linkInputRef}
            data-testid="link-url-input"
          />
          <button type="submit" class="ghost small" data-testid="link-url-submit">
            Add
          </button>
        </form>
      </div>
    </>
  )
}
