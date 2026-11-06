import assert from 'node:assert/strict'
import path from 'node:path'

import { test } from 'vitest'

import { createSourcePythonBackend } from './source-backend'
import { resolveSourcePython } from './source-python'

const ROOT: string = path.join(path.sep, 'checkout')

/** A checkout reporting exactly `present` (root-relative) as existing files. */
function checkout(present: string[] = [], exists: boolean = true) {
  const files = new Set(present.map(entry => path.join(ROOT, entry)))

  return { root: ROOT, fileExists: (candidate: string): boolean => exists && files.has(candidate) }
}

test('a checkout with no virtualenv resolves to no interpreter, never a PATH Python', (): void => {
  const { root, fileExists } = checkout()

  // The invariant: "this root has no runtime" must reach callers as null.
  // Answering with a PATH Python runs an install's update probe and its backend
  // under an interpreter nothing selected — it can import the checkout while
  // lacking its selected dependencies (updater/checkout-source.ts).
  assert.equal(resolveSourcePython(root, { fileExists }), null)
})

test('a virtualenv-less checkout yields no source backend, so the ladder moves on', (): void => {
  const { root, fileExists } = checkout()
  const python: string | null = resolveSourcePython(root, { fileExists })

  assert.equal(createSourcePythonBackend(root, python, ['serve']), null)
})

test('the checkout virtualenv is preferred over the flat venv, on both layouts', (): void => {
  const venv: string = path.join('venv', 'bin', 'python')
  const dotVenv: string = path.join('.venv', 'bin', 'python')

  assert.equal(
    resolveSourcePython(ROOT, { fileExists: checkout([venv, dotVenv]).fileExists }),
    path.join(ROOT, dotVenv)
  )
  assert.equal(resolveSourcePython(ROOT, { fileExists: checkout([venv]).fileExists }), path.join(ROOT, venv))
})

test('an explicit developer interpreter wins, and is only honoured when it exists', (): void => {
  const preferred: string = path.join(path.sep, 'opt', 'hermes', 'bin', 'python')
  const aged: string = path.join(ROOT, 'venv', 'bin', 'python')
  const { root, fileExists } = checkout(['venv/bin/python'], true)
  const withPreferred = (candidate: string): boolean => candidate === preferred || fileExists(candidate)

  assert.equal(resolveSourcePython(root, { override: preferred, fileExists: withPreferred }), preferred)
  // An override naming a file that is gone does not resurrect a PATH Python;
  // it falls through to the checkout, then to null.
  assert.equal(resolveSourcePython(root, { override: preferred, fileExists }), aged)
  assert.equal(resolveSourcePython(ROOT, { override: preferred, fileExists: checkout().fileExists }), null)
})
