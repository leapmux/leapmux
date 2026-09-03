import type { Component } from 'solid-js'
import type { Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import { Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { createTitleState } from '~/hooks/createTitleState'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { errorText } from '~/styles/shared.css'
import { TitleInput } from './TitleInput'
import { DialogFormFooter } from './WorkerDialogShell'

/** Open-time payload for {@link SectionNameDialog}. */
export interface SectionNamePayload {
  mode: 'create' | 'rename'
  /** Which sidebar a CREATED section lands on. Ignored for a rename. */
  sidebar: Sidebar
  /** The section being renamed. Absent for a create. */
  sectionId?: string
  /** The name the field starts with. Empty for a create. */
  initialName?: string
}

interface SectionNameDialogProps {
  payload: SectionNamePayload
  /**
   * Do the work. Resolves once the RPC settled; the dialog closes after.
   * Rejecting shows the message in the dialog's own error row.
   */
  onSubmit: (name: string) => Promise<void>
  onClose: () => void
}

/**
 * Name a sidebar section, for both creating one and renaming one.
 *
 * ONE dialog for the two, because they differ only in their title, their submit
 * label and their starting value -- and a second component would be a second
 * place the name rule lives. The field is `TitleInput`, so a section name and a
 * workspace title are validated and drawn identically.
 */
export const SectionNameDialog: Component<SectionNameDialogProps> = (props) => {
  // Read ONCE, both of them. The caller's `<Show>` is keyed, so a different
  // payload remounts this component rather than mutating a half-typed field
  // under the user.
  /* eslint-disable solid/reactivity -- one-time initial values; see above */
  const creating = props.payload.mode === 'create'
  const name = createTitleState(() => props.payload.initialName ?? '')
  /* eslint-enable solid/reactivity */
  const { submitting, error, formHandler } = useDialogSubmit({
    fallback: creating ? 'Failed to create section' : 'Failed to rename section',
  })

  // Submit sends the CLEANED name: the hub applies `SanitizeName` to whatever
  // arrives, so raw text would leave the sidebar and the hub disagreeing.
  const submitDisabled = () => submitting.loading() || !name.cleaned() || !!name.error()

  const handleSubmit = formHandler(submitDisabled, async () => {
    await props.onSubmit(name.cleaned())
    props.onClose()
  })

  return (
    <Dialog
      title={creating ? 'New section' : 'Rename section'}
      busy={submitting.loading()}
      onClose={() => props.onClose()}
      data-testid="section-name-dialog"
    >
      <form onSubmit={handleSubmit}>
        <section>
          <div class="vstack gap-4">
            <TitleInput state={name} />
          </div>
          <Show when={error()}>
            <div class={errorText}>{error()}</div>
          </Show>
        </section>
        <footer class={actionsFooter}>
          <DialogFormFooter
            submitting={submitting.loading()}
            submitDisabled={submitDisabled()}
            submitLabel={creating ? 'Create' : 'Rename'}
            submittingLabel={creating ? 'Creating...' : 'Renaming...'}
            onClose={() => props.onClose()}
          />
        </footer>
      </form>
    </Dialog>
  )
}
