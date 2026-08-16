import type { AltchaWidgetElement } from 'altcha'
import 'solid-js'

// The altcha npm package ships React JSX typings; Solid needs its own
// intrinsic-element declaration for <altcha-widget>. All configuration is
// done imperatively via the element's configure() in CaptchaField, so the
// attributes stay generic.
declare module 'solid-js' {
  namespace JSX {
    interface IntrinsicElements {
      'altcha-widget': JSX.HTMLAttributes<AltchaWidgetElement>
    }
  }
}
