import type { Component } from 'solid-js'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import { For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { isMoveTargetSection } from '~/components/shell/sectionUtils'
import { dangerMenuItem } from '~/styles/shared.css'

interface WorkspaceContextMenuProps {
  isArchived: boolean
  sections: Section[]
  currentSectionId: string | undefined
  onRename: () => void
  onMoveTo: (sectionId: string) => void
  onArchive: () => void
  onUnarchive: () => void
  onDelete: () => void
}

export const WorkspaceContextMenu: Component<WorkspaceContextMenuProps> = (props) => {
  return (
    <DropdownMenu trigger={rowContextMenuTrigger()}>
      <button role="menuitem" onClick={() => props.onRename()}>
        Rename
      </button>

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

      <hr />
      <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDelete()}>
        Delete
      </button>
    </DropdownMenu>
  )
}
