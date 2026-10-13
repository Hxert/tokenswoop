import { useEffect, useLayoutEffect, useState } from 'react'

export type WorkspaceTheme = 'dark' | 'light'

const storageKey = 'tokenswoop.workspace.theme'
const isTheme = (value: string | null): value is WorkspaceTheme =>
  value === 'light' || value === 'dark'

function savedTheme(): WorkspaceTheme {
  try {
    const value = localStorage.getItem(storageKey)
    if (isTheme(value)) return value
  } catch {
    // Theme switching also works when browser storage is unavailable.
  }
  return 'dark'
}

export function useWorkspaceTheme() {
  const [theme, setTheme] = useState<WorkspaceTheme>(savedTheme)

  useLayoutEffect(() => {
    document.documentElement.dataset.workspaceTheme = theme
    return () => {
      delete document.documentElement.dataset.workspaceTheme
    }
  }, [theme])

  useEffect(() => {
    const sync = (event: StorageEvent) => {
      if (event.key === storageKey || event.key === null)
        setTheme(isTheme(event.newValue) ? event.newValue : 'dark')
    }
    window.addEventListener('storage', sync)
    return () => window.removeEventListener('storage', sync)
  }, [])

  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    try {
      localStorage.setItem(storageKey, next)
    } catch {
      // Keep the user's choice for this page even if it cannot be persisted.
    }
  }

  return { theme, toggleTheme }
}
