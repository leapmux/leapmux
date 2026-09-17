import type { ToolKindRenderer } from './renderer'
import ChartColumn from 'lucide-solid/icons/chart-column'
import { chartCopyableText } from '../../ir/chartResult'
import { ChartResultBody } from '../chartResult'

export const chartRenderer: ToolKindRenderer<'chart'> = {
  icon: ChartColumn,
  label: 'Chart',
  title(call) {
    // The chart's own heading leads -- the tool asks the model for it, and the result
    // carries the one the drawing actually used. Then the call's own title, then the
    // label: the tail `renderer.ts` states for every kind.
    return call.result?.title ?? call.request.title ?? call.title ?? 'Chart'
  },
  result(call) {
    return <ChartResultBody source={call.result} />
  },
  resultMeta(call) {
    return {
      collapsible: false,
      hasDiff: false,
      copyableContent: () => chartCopyableText(call.result),
    }
  },
}
