import type { ParentComponent } from 'solid-js'
import type { TunnelStore } from '~/stores/tunnel.store'
import { createContext, useContext } from 'solid-js'

const TunnelContext = createContext<TunnelStore>()

export const TunnelProvider: ParentComponent<{ store: TunnelStore }> = (props) => {
  // eslint-disable-next-line solid/reactivity -- store is a stable object, not a reactive primitive
  const store = props.store
  return (
    <TunnelContext.Provider value={store}>
      {props.children}
    </TunnelContext.Provider>
  )
}

export function useTunnel(): TunnelStore | undefined {
  return useContext(TunnelContext)
}
