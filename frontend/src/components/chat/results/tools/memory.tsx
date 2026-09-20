import NotebookPen from 'lucide-solid/icons/notebook-pen'
import { proseRenderer } from './proseResult'

export const memoryRenderer = proseRenderer<'memory'>({
  icon: NotebookPen,
  label: 'Memory',
  title(call) {
    return call.title ?? 'Memory'
  },
})
