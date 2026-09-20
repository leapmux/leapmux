import Hourglass from 'lucide-solid/icons/hourglass'
import { proseRenderer } from './proseResult'

export const waitRenderer = proseRenderer<'wait'>({
  icon: Hourglass,
  label: 'Wait',
  title(call) {
    return call.request.durationMs !== undefined ? `${call.request.durationMs / 1000}s` : call.title ?? 'Wait'
  },
})
