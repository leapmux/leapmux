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
  /**
   * The element to position the popover against -- the editor WRAPPER, never the
   * clicked `<a>`. ProseMirror owns the anchor and redraws it on every document
   * change, so positioning from it leaves a detached node and a zero-sized rect.
   */
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

  // Saving applies the URL and DISMISSES the popover -- the same way Remove
  // does, and the same way Enter in the field does, since it submits this form.
  // The edit is finished at that point, so leaving the panel over the text the
  // user just linked only hides the result.
  //
  // Apply BEFORE closing: `applyLinkHref` focuses the editor, and doing that
  // while the panel is still open would light-dismiss it mid-write.
  //
  // The close is safe here even though Oat animates it with `display` and
  // `overlay` in `allow-discrete`, which keeps the element in the top layer for
  // 150ms: the trap that window creates needs a close IMMEDIATELY FOLLOWED BY A
  // REOPEN (`showPopover()` re-entering an element the browser is still
  // removing, which lands the panel below the page). Nothing reopens it here --
  // only a fresh click on a link or a fresh Mod-K does, both of which are a
  // separate gesture.
  const submit = () => {
    props.onApply(href())
    props.setOpen(false)
  }

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
