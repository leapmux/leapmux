import type { Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { cleanName } from '~/lib/validate'
import { tabDisplayLabel } from '~/stores/tab.helpers'

/**
 * The stores a rename touches: the tab lookup and the local metadata.
 *
 * Narrowed to the three methods this module calls, so a caller can pass the
 * shell's real stores and a test can pass exactly what the rename reads.
 */
export interface RenameTabDeps {
  view: Pick<TabView, 'getAgentTab' | 'getTerminalTab'>
  metadata: Pick<TabMetadataStore, 'patch'>
}

/**
 * Applies a user-typed tab title: cleans it, patches the local metadata, and
 * tells the worker.
 *
 * This is the ONE client-side write point for a tab title, so the tab strip and
 * the sidebar tree cannot rename a tab two different ways. Both used to inline
 * their own copy, and the sidebar's copy never called `updateTerminalTitle` —
 * renaming a terminal there survived until the next reload and reached no other
 * client.
 *
 * `cleanName` is the same rule the worker applies at `RenameAgent` and
 * `UpdateTerminalTitle` (see `testdata/title_cleaning_conformance.json`), so
 * the optimistic patch below holds exactly what the worker stores. Sending the
 * raw string instead left the tab showing a 400-byte title the worker had cut
 * to 128, with no error to say so.
 *
 * Both replies now carry a `title` field holding what the worker stored, and
 * this module deliberately ignores it. The patch has to land BEFORE the round
 * trip, so the local rule is required either way; reading the reply could only
 * add a second, later patch. That later patch would undo a rename the user
 * typed before the first reply arrived, because it would restore the older
 * title. It would also correct nothing, because the shared conformance fixture
 * pins the two rules to the same answer. The field is for a client with no
 * local copy of the rule: the control CLI prints it, and a script reads it.
 *
 * Two titles are dropped rather than sent:
 *
 * - One that cleaning empties. The worker keeps the current title for it, so
 *   patching the metadata would put the tab out of step with the row.
 * - One that equals the label the tab already shows, which is what a commit
 *   after an untouched edit produces.
 */
export function renameTab(deps: RenameTabDeps, tab: Tab, typed: string): void {
  const title = cleanName(typed)
  if (!title || title === tabDisplayLabel(tab))
    return

  if (tab.type === TabType.AGENT) {
    deps.metadata.patch(tab.id, { title })
    const workerId = deps.view.getAgentTab(tab.id)?.workerId ?? ''
    workerRpc.renameAgent(workerId, { agentId: tab.id, title }).catch((err) => {
      showWarnToast('Failed to rename agent', err)
    })
    return
  }

  if (tab.type === TabType.TERMINAL) {
    // Clearing ptyTitle lets a manual rename stick: TitleChanged only patches
    // ptyTitle, and tabDisplayLabel prefers title.
    deps.metadata.patch(tab.id, { title, ptyTitle: '' })
    const workerId = deps.view.getTerminalTab(tab.id)?.workerId ?? ''
    workerRpc.updateTerminalTitle(workerId, { terminalId: tab.id, title }).catch((err) => {
      showWarnToast('Failed to rename terminal', err)
    })
  }

  // A FILE tab has no rename: its title IS its path, and no RPC carries one.
  // Both call sites already refuse to open the editor on one, so reaching here
  // means a new caller — do nothing rather than patch a title the tab derives.
}
