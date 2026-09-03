import type { Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import type { createSectionStore } from '~/stores/section.store'
import { sectionClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { cleanName } from '~/lib/validate'

export interface UseSectionOperationsProps {
  sectionStore: ReturnType<typeof createSectionStore>
  /** Re-read the sections and their items from the hub. */
  loadSections: () => Promise<void>
}

/**
 * Create, rename and delete a custom sidebar section.
 *
 * The sibling of {@link useWorkspaceOperations}: same shape (RPC, then the
 * optimistic store write, then a warning toast on failure), a different noun.
 * All three RPCs were implemented and tested server-side with no caller at all
 * until the section header menu reached them.
 *
 * The hub refuses a rename or a delete of a BUILT-IN section, so the menu hides
 * both items for In progress and Archived. That is a second layer over a rule
 * that already holds -- see `requireCustomSection` in the hub.
 */
export function useSectionOperations(props: UseSectionOperationsProps) {
  const store = props.sectionStore

  /**
   * Create a custom section on `sidebar`.
   *
   * The response carries the whole row, so the store gets the server's own
   * lexorank position. A response with only the id would leave this function
   * re-deriving the position rule -- a second source of truth for the section
   * order -- or refetching the list.
   */
  const createSection = async (name: string, sidebar: Sidebar): Promise<void> => {
    // The CLEANED name, not the raw one: the hub applies `SanitizeName` to
    // whatever arrives, so raw text leaves the sidebar and the hub disagreeing
    // until the next refresh. Same rule as `commitRename` for a workspace.
    const cleaned = cleanName(name)
    if (!cleaned)
      return
    // NOT caught here, unlike `deleteSection` below. Both of the name
    // operations run from a dialog that owns an error row, and a toast on top
    // of that row would report one failure twice in two voices.
    const resp = await sectionClient.createSection({ name: cleaned, sidebar })
    const section = resp.section
    if (!section)
      throw new Error('No section in response')
    store.addSection(section)
  }

  const renameSection = async (sectionId: string, name: string): Promise<void> => {
    const cleaned = cleanName(name)
    if (!cleaned)
      return
    await sectionClient.renameSection({ sectionId, name: cleaned })
    store.updateSection(sectionId, { name: cleaned })
  }

  /**
   * Delete a custom section. Its workspaces move to In progress, which is what
   * the hub does inside the delete transaction.
   *
   * This one DOES toast: it runs from a confirm dialog that has no error row of
   * its own and is already dismissed by the time the RPC settles.
   *
   * The sections are re-read afterwards rather than patched: the hub re-stamps
   * every relocated item's lexorank to append past the existing In-progress
   * items, and those positions are the server's to compute.
   */
  const deleteSection = async (sectionId: string): Promise<void> => {
    try {
      await sectionClient.deleteSection({ sectionId })
      store.removeSection(sectionId)
      await props.loadSections()
    }
    catch (err) {
      showWarnToast('Failed to delete section', err)
    }
  }

  return { createSection, renameSection, deleteSection }
}

export type SectionOperations = ReturnType<typeof useSectionOperations>
