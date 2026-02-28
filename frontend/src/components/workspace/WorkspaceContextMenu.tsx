import type { Component } from 'solid-js'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { isMoveTargetSection } from '~/components/shell/sectionUtils'
import { dangerMenuItem } from '~/styles/shared.css'
import * as listStyles from './workspaceList.css'

interface WorkspaceContextMenuProps {
  isOwner: boolean
  isArchived: boolean
  sections: Section[]
  currentSectionId: string | undefined
  onRename: () => void
  onMoveTo: (sectionId: string) => void
  onShare: () => void
  onArchive: () => void
  onUnarchive: () => void
  onDelete: () => void
}

export const WorkspaceContextMenu: Component<WorkspaceContextMenuProps> = (props) => {
  return (
    <DropdownMenu
      trigger={triggerProps => (
        <IconButton
          icon={MoreHorizontal}
          size="sm"
          class={listStyles.itemMenuTrigger}
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            triggerProps.onClick()
          }}
          ref={triggerProps.ref}
          onPointerDown={(e: PointerEvent) => {
            e.stopPropagation()
            triggerProps.onPointerDown(e)
          }}
          aria-expanded={triggerProps['aria-expanded']}
        />
      )}
    >
      <Show when={props.isOwner}>
        <button role="menuitem" onClick={() => props.onRename()}>
          Rename
        </button>
      </Show>

      <Show when={!props.isArchived && props.sections.some(s => s.id !== props.currentSectionId && isMoveTargetSection(s.sectionType))}>
        <DropdownMenu
          trigger={triggerProps => (
            <button role="menuitem" class="sub-trigger" {...triggerProps}>
              Move to
              <Icon icon={ChevronRight} size="xs" />
            </button>
          )}
        >
          <For each={props.sections.filter(s => s.id !== props.currentSectionId && isMoveTargetSection(s.sectionType))}>
            {section => (
              <button
                role="menuitem"
                onClick={() => props.onMoveTo(section.id)}
              >
                {section.name}
              </button>
            )}
          </For>
        </DropdownMenu>
      </Show>

      <Show when={props.isOwner}>
        <button role="menuitem" onClick={() => props.onShare()}>
          Share...
        </button>
      </Show>

      <Show when={!props.isArchived}>
        <button role="menuitem" onClick={() => props.onArchive()}>
          Archive
        </button>
      </Show>

      <Show when={props.isArchived}>
        <button role="menuitem" onClick={() => props.onUnarchive()}>
          Unarchive
        </button>
      </Show>

      <Show when={props.isOwner}>
        <hr />
        <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDelete()}>
          Delete
        </button>
      </Show>
    </DropdownMenu>
  )
}
