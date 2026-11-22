import type { Component } from 'solid-js'
import type { SoloBusyWork } from '~/api/platformBridge'
import type { TabBusyReason } from '~/components/shell/tabBusyProbe'
import { createMemo, For } from 'solid-js'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { TabBusyDetails } from '~/components/shell/TabBusyDetails'
import { busyGroupList } from '~/components/shell/TabBusyDetails.css'
import { pluralize } from '~/lib/plural'

export type SoloStopIntent = 'quit' | 'switch-mode'

export interface SoloStopConfirmDialogProps {
  intent: SoloStopIntent
  busyWork: SoloBusyWork
  onConfirm: () => void
  onCancel: () => void
}

interface BusyTabEntry {
  title: string
  reason: TabBusyReason
}

export const SoloStopConfirmDialog: Component<SoloStopConfirmDialogProps> = (props) => {
  const items = createMemo<BusyTabEntry[]>(() => {
    const out: BusyTabEntry[] = []
    for (const agent of props.busyWork.agents) {
      out.push({
        title: agent.title || 'Agent',
        reason: {
          kind: 'agent-work',
          activeBackgroundTasks: agent.activeBackgroundTasks,
          activeChildAgents: agent.activeChildAgents,
        },
      })
    }
    for (const terminal of props.busyWork.terminals) {
      out.push({
        title: terminal.title || 'Terminal',
        reason: {
          kind: 'terminal-processes',
          processes: terminal.processes,
          totalCount: terminal.totalCount,
        },
      })
    }
    return out
  })

  const totalTabs = () => items().length
  const title = () => props.intent === 'quit'
    ? 'Quit LeapMux'
    : 'Switch mode'
  const confirmLabel = () => props.intent === 'quit'
    ? 'Quit anyway'
    : 'Switch mode anyway'
  const lead = () => props.intent === 'quit'
    ? `Quitting stops work in ${pluralize(totalTabs(), 'tab')}:`
    : `Switching mode stops work in ${pluralize(totalTabs(), 'tab')}:`

  return (
    <ConfirmDialog
      title={title()}
      confirmLabel={confirmLabel()}
      danger
      data-testid="solo-stop-confirm-dialog"
      confirmTestId="solo-stop-confirm-confirm"
      cancelTestId="solo-stop-confirm-cancel"
      onConfirm={() => props.onConfirm()}
      onCancel={() => props.onCancel()}
    >
      <p>{lead()}</p>
      <div class={busyGroupList} data-testid="solo-stop-busy-list">
        <For each={items()}>
          {item => (
            <div>
              <strong>{item.title}</strong>
              <TabBusyDetails reason={item.reason} action={props.intent} />
            </div>
          )}
        </For>
      </div>
    </ConfirmDialog>
  )
}
