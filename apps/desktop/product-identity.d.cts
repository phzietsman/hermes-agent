interface ProductIdentity {
  /** True when this artifact is Hermes Light (remote-only client). */
  light: boolean
  /** Display name. e.g. "Hermes Light" */
  displayName: string
  /** OS-level app identity. e.g. "com.nousresearch.hermes-light" */
  appId: string
  /** app name in pascal case. e.g. "HermesLight" */
  appNamePascal: string
  /** Artifact prefix stays compatible with release archive consumers. */
  artifactNamePascal: string
  /** Exposed payload CLI command. */
  cliName: string
  /** R2 identity token on channel builds; absent on legacy products. */
  readonly token?: string
  /** Channel subscription for channel builds; legacy electron-updater label otherwise. Stable tags:
   *  "latest" | "light"; canary tags: "canary" | "light-canary". Null
   *  for commit builds (no release feed). */
  readonly channel: string | null
}

declare const identity: ProductIdentity
export = identity
