export function platformDefaultHermesHome(home: string, env?: NodeJS.ProcessEnv): string

export function resolveDesktopUserData(defaultPath: string, env?: NodeJS.ProcessEnv): string

export interface HermesHomeOptions {
  home: string
  env?: NodeJS.ProcessEnv
}

export function resolveDesktopHermesHome(options: HermesHomeOptions): string
