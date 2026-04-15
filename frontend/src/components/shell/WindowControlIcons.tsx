import type { LucideProps } from 'lucide-solid'
import { SvgIconFrame } from '~/components/common/SvgIconFrame'

export function WindowMinimizeIcon(props: LucideProps) {
  // Underbar resting near the baseline.
  return (
    <SvgIconFrame {...props}>
      <line x1="5" y1="18" x2="19" y2="18" />
    </SvgIconFrame>
  )
}

export function WindowMaximizeIcon(props: LucideProps) {
  // Big outline square, vertically centred-ish within the glyph area.
  return (
    <SvgIconFrame {...props}>
      <rect x="5" y="5" width="14" height="14" />
    </SvgIconFrame>
  )
}

export function WindowRestoreIcon(props: LucideProps) {
  // Small outline square centred in the glyph area.
  return (
    <SvgIconFrame {...props}>
      <rect x="7" y="8" width="10" height="10" />
    </SvgIconFrame>
  )
}

export function WindowCloseIcon(props: LucideProps) {
  // Diagonal cross.
  return (
    <SvgIconFrame {...props}>
      <line x1="5" y1="5" x2="19" y2="19" />
      <line x1="19" y1="5" x2="5" y2="19" />
    </SvgIconFrame>
  )
}
