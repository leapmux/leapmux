import 'solid-js'

declare module 'solid-js' {
  namespace JSX {
    interface IntrinsicElements {
      'ot-dropdown': JSX.HTMLAttributes<HTMLElement>
      'ot-tabs': JSX.HTMLAttributes<HTMLElement>
    }
  }
}
