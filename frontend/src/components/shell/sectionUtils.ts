import type { LucideIcon } from 'lucide-solid'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import Archive from 'lucide-solid/icons/archive'
import Folder from 'lucide-solid/icons/folder'
import FolderTree from 'lucide-solid/icons/folder-tree'
import Layers from 'lucide-solid/icons/layers'
import ListChecks from 'lucide-solid/icons/list-checks'
import Users from 'lucide-solid/icons/users'
import { SectionType } from '~/generated/leapmux/v1/section_pb'

/** Whether the section type is a workspace section (can contain workspaces). */
export function isWorkspaceSection(sectionType: SectionType): boolean {
  return sectionType === SectionType.WORKSPACES_IN_PROGRESS
    || sectionType === SectionType.WORKSPACES_CUSTOM
    || sectionType === SectionType.WORKSPACES_ARCHIVED
    || sectionType === SectionType.WORKSPACES_SHARED
}

/** Map section type to a test ID slug. */
export function sectionTypeTestId(sectionType: SectionType): string {
  switch (sectionType) {
    case SectionType.WORKSPACES_IN_PROGRESS: return 'workspaces_in_progress'
    case SectionType.WORKSPACES_CUSTOM: return 'workspaces_custom'
    case SectionType.WORKSPACES_ARCHIVED: return 'workspaces_archived'
    case SectionType.WORKSPACES_SHARED: return 'workspaces_shared'
    case SectionType.FILES: return 'files'
    case SectionType.TODOS: return 'todos'
    default: return String(sectionType)
  }
}

/** Whether the section type is a valid "Move to" target for workspaces. */
export function isMoveTargetSection(sectionType: SectionType): boolean {
  return isWorkspaceSection(sectionType)
    && sectionType !== SectionType.WORKSPACES_ARCHIVED
    && sectionType !== SectionType.WORKSPACES_SHARED
}

/** Whether a workspace can be mutated (create agents/terminals, rename, etc.). */
export function isWorkspaceMutatable(
  workspace: { createdBy: string } | undefined,
  currentUserId: string,
  isArchived: boolean,
): boolean {
  if (!workspace)
    return false
  if (workspace.createdBy !== currentUserId)
    return false
  return !isArchived
}

/** Map section to its icon. */
export function getSectionIcon(section: Section): LucideIcon {
  switch (section.sectionType) {
    case SectionType.WORKSPACES_IN_PROGRESS:
      return Layers
    case SectionType.WORKSPACES_ARCHIVED:
      return Archive
    case SectionType.WORKSPACES_SHARED:
      return Users
    case SectionType.FILES:
      return FolderTree
    case SectionType.TODOS:
      return ListChecks
    default:
      return Folder
  }
}
