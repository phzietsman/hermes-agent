export interface SourceRuntimeSettleCommand {
  launcher: string
  command: string
  args: string[]
}

export function sourceRuntimeSettleCommand(root: string): SourceRuntimeSettleCommand
