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
 * The cleaned name, or a rejection.
 *
 * The hub applies `SanitizeName` to whatever arrives, so a caller that sent the
 * raw text would leave the sidebar and the hub disagreeing until the next
 * refresh -- the same rule `commitRename` follows for a workspace.
 */
function requireName(name: string): string {
  const cleaned = cleanName(name)
  if (!cleaned)
    throw new Error('A section needs a name')
  return cleaned
}

/**
 * Create, rename and delete a custom sidebar section.
 *
 * The sibling of {@link useWorkspaceOperations}, with a different noun and a
 * different error policy. All three operations REJECT, and the caller decides
 * how to report it: the two name operations run from a dialog that owns an
 * error row, and the delete runs from a confirm that is already dismissed, so
 * its call site adds the toast. One policy per module, so a new call site
 * cannot pick up the wrong surface by accident.
 *
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
    // THROWS rather than returning. A resolve is the dialog's success signal,
    // so returning here closed it as though a section had been created. The
    // dialog's own submit guard already blocks an empty name, which makes this
    // unreachable today -- and that is exactly why it must fail loudly if that
    // guard ever loosens.
    const cleaned = requireName(name)
    const resp = await sectionClient.createSection({ name: cleaned, sidebar })
    const section = resp.section
    if (!section)
      throw new Error('No section in response')
    store.addSection(section)
  }

  const renameSection = async (sectionId: string, name: string): Promise<void> => {
    const cleaned = requireName(name)
    await sectionClient.renameSection({ sectionId, name: cleaned })
    store.updateSection(sectionId, { name: cleaned })
  }

  /**
   * Delete a custom section. Its workspaces move to In progress, which is what
   * the hub does inside the delete transaction.
   *
   * The sections are re-read afterwards rather than patched: the hub re-stamps
   * every relocated item's lexorank to append past the existing In-progress
   * items, and those positions are the server's to compute. That reload is
   * NOT part of the delete's success: it runs after the section is already
   * gone, so a failure there must not report the delete as failed. It re-raises
   * under its own message instead.
   */
  const deleteSection = async (sectionId: string): Promise<void> => {
    await sectionClient.deleteSection({ sectionId })
    store.removeSection(sectionId)
    try {
      await props.loadSections()
    }
    catch (err) {
      showWarnToast('Deleted the section, but could not refresh the sidebar', err)
    }
  }

  return { createSection, renameSection, deleteSection }
}

export type SectionOperations = ReturnType<typeof useSectionOperations>
