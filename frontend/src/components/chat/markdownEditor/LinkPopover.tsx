import type { Accessor, Component, Setter } from 'solid-js'
import type { LinkRange } from '~/lib/editor/linkPlugin'
import Trash2 from 'lucide-solid/icons/trash-2'
import { createEffect, createSignal } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './MarkdownEditor.css'

export interface LinkPopoverProps {
  open: Accessor<boolean>
  setOpen: Setter<boolean>
  /** The clicked link run, or null when none is selected. */
  range: Accessor<LinkRange | null>
  /** The clicked anchor element, for positioning. */
  anchorRef: Accessor<HTMLElement | undefined>
  /** Save a new URL for the current run. */
  onApply: (href: string) => void
  /** Strip the link mark from the current run, keeping its text. */
  onRemove: () => void
}

/**
 * Edit or remove the URL of the link the user clicked.
 *
 * This is the only surface that can unmake a link. Editing a link's visible text
 * does NOT clear its href — the mark is inclusive, so ProseMirror re-applies it
 * even across a delete-and-retype — which means without this popover a corrected
 * label silently ships the original URL to the agent.
 */
export const LinkPopover: Component<LinkPopoverProps> = (props) => {
  const [href, setHref] = createSignal('')
  let popoverRef: HTMLElement | undefined

  // Seed the input from the run each time the popover opens. The popover stays
  // mounted between opens, so without this it would still hold the previous
  // link's URL.
  createEffect(() => {
    if (!props.open())
      return
    setHref(props.range()?.href ?? '')
    requestAnimationFrame(() => {
      if (!props.open())
        return
      const input = popoverRef?.querySelector<HTMLInputElement>('input')
      input?.focus()
      input?.select()
    })
  })

  // Saving applies the URL and leaves the popover OPEN, so the user can see the
  // value that took and reach the remove button without a second click.
  //
  // It also avoids a browser-level trap. Oat animates a popover's close with
  // `display` and `overlay` in `allow-discrete`, so the element stays in the top
  // layer for 150ms afterwards. This popover sits over its own trigger, so a
  // click that closes it and immediately reopens it lands inside that window:
  // `showPopover()` then re-enters an element the browser is still removing from
  // the top layer, and the popover comes back BELOW the page, where the chat
  // transcript intercepts every click meant for its buttons.
  const submit = () => props.onApply(href())

  return (
    <DropdownMenu
      as="div"
      anchorRef={props.anchorRef}
      open={props.open}
      popoverRef={(el) => { popoverRef = el }}
      class={styles.linkPopover}
      data-testid="link-popover"
      aria-label="Edit link"
      // A form, not a menu: typing in the field, saving, and removing are all
      // clicks inside it, and the menu default would dismiss the popover on
      // each one.
      closeOnContentClick={false}
      onToggle={(open) => {
        if (!open)
          props.setOpen(false)
      }}
    >
      <form
        class={styles.linkPopoverForm}
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <input
          type="url"
          class={styles.linkPopoverInput}
          placeholder="https://..."
          value={href()}
          aria-label="Link URL"
          data-testid="link-url-input"
          onInput={e => setHref(e.currentTarget.value)}
          onKeyDown={(e) => {
            // Escape closes without applying; the popover's own handler would
            // otherwise leave the half-typed URL staged for the next open.
            if (e.key === 'Escape')
              props.setOpen(false)
          }}
        />
        <button type="submit" class="ghost small" data-testid="link-url-submit">
          Save
        </button>
        <Tooltip text="Remove link, keeping the text" ariaLabel>
          <button
            type="button"
            class="ghost small"
            data-testid="link-url-remove"
            onClick={() => {
              props.onRemove()
              props.setOpen(false)
            }}
          >
            <Icon icon={Trash2} size="xs" />
          </button>
        </Tooltip>
      </form>
    </DropdownMenu>
  )
}
