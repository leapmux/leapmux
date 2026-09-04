import type { LucideIcon } from 'lucide-solid'
import type { Section, Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import Archive from 'lucide-solid/icons/archive'
import Folder from 'lucide-solid/icons/folder'
import FolderTree from 'lucide-solid/icons/folder-tree'
import Layers from 'lucide-solid/icons/layers'
import ListChecks from 'lucide-solid/icons/list-checks'
import ListTree from 'lucide-solid/icons/list-tree'
import Monitor from 'lucide-solid/icons/monitor'
import { SectionType } from '~/generated/proto/leapmux/v1/section_pb'

/** Whether the section type is a workspace section (can contain workspaces). */
export function isWorkspaceSection(sectionType: SectionType): boolean {
  return sectionType === SectionType.WORKSPACES_IN_PROGRESS
    || sectionType === SectionType.WORKSPACES_CUSTOM
    || sectionType === SectionType.WORKSPACES_ARCHIVED
}

/** Map section type to a test ID slug. */
export function sectionTypeTestId(sectionType: SectionType): string {
  switch (sectionType) {
    case SectionType.WORKSPACES_IN_PROGRESS: return 'workspaces_in_progress'
    case SectionType.WORKSPACES_CUSTOM: return 'workspaces_custom'
    case SectionType.WORKSPACES_ARCHIVED: return 'workspaces_archived'
    case SectionType.FILES: return 'files'
    case SectionType.TODOS: return 'todos'
    case SectionType.WORKERS: return 'workers'
    case SectionType.BACKGROUND_TASKS: return 'background_tasks'
    default: return String(sectionType)
  }
}

/** Whether the section type is a valid "Move to" target for workspaces. */
export function isMoveTargetSection(sectionType: SectionType): boolean {
  return isWorkspaceSection(sectionType)
    && sectionType !== SectionType.WORKSPACES_ARCHIVED
}

/**
 * Whether a workspace can be mutated: create agents and terminals, rename it.
 *
 * Archival is the ONE thing that blocks mutation, and this signature says so.
 * The `workspace` parameter it used to take was never read for its
 * `createdBy` -- a vestige of the removed sharing model, used only as a
 * presence check -- and it forced every caller that holds an archived FLAG but
 * no workspace object to invent one.
 */
export function isWorkspaceMutatable(isArchived: boolean): boolean {
  return !isArchived
}

/** Map section to its icon. */
export function getSectionIcon(section: Section): LucideIcon {
  switch (section.sectionType) {
    case SectionType.WORKSPACES_IN_PROGRESS:
      return Layers
    case SectionType.WORKSPACES_ARCHIVED:
      return Archive
    case SectionType.FILES:
      return FolderTree
    case SectionType.TODOS:
      return ListChecks
    case SectionType.WORKERS:
      return Monitor
    case SectionType.BACKGROUND_TASKS:
      return ListTree
    default:
      return Folder
  }
}

/**
 * The three section-CRUD callbacks the sidebar hands to every section header
 * menu.
 *
 * ONE bundle rather than three parallel fields declared in three interfaces
 * that only forward them. A fourth section action then touches one type and one
 * forwarding site instead of three, and the three layers cannot drift on a
 * signature or a doc comment. It follows `WorkspaceStartActions`, which already
 * groups the row's two tab-creation callbacks the same way.
 *
 * Only plain callbacks belong in a bundle like this. A reactive value
 * (`localSolo`) must stay a flat field, because a nested object read once
 * freezes it.
 */
export interface SectionActions {
  /** Open the New section dialog for `sidebar`. */
  onNew: (sidebar: Sidebar) => void
  onRename: (section: Section) => void
  onDelete: (section: Section) => void
}
