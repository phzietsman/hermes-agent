import assert from 'node:assert/strict'
import fs from 'node:fs'
import { JSDOM } from 'jsdom'
import { afterEach, test, vi } from 'vitest'

// Execute the shipped page, including its inline script, rather than matching
// source strings or testing a second implementation of the progress client.
const html = fs.readFileSync(new URL('../../../scripts/desktop-update/ui.html', import.meta.url), 'utf8')
const windows = []

function openPage(fetch) {
  vi.useFakeTimers()
  const dom = new JSDOM(html, {
    url: 'http://127.0.0.1:12345/',
    runScripts: 'dangerously',
    beforeParse(window) {
      window.fetch = fetch
      window.AbortController = AbortController
      window.setTimeout = setTimeout
      window.clearTimeout = clearTimeout
      window.requestAnimationFrame = () => 1
      window.cancelAnimationFrame = () => {}
    }
  })
  windows.push(dom.window)
  return dom.window.document
}

afterEach(() => {
  windows.splice(0).forEach(window => window.close())
  vi.useRealTimers()
})

test.each(['done', 'manual', 'error'])('renders %s once the orchestrator reports it', async status => {
  const requests = []
  const fetch = vi.fn(async url => {
    requests.push(url)
    return { ok: true, json: async () => ({ status, message: 'The updater result' }) }
  })
  const document = openPage(fetch)
  await vi.advanceTimersByTimeAsync(1000)
  assert.equal(document.body.className, status === 'error' ? 'error' : 'done')
  assert.notEqual(document.getElementById('title').textContent, 'Updating Hermes')
  assert.deepEqual(requests, ['/progress'])
})

test.each(['disconnect', 'hung', 'hung-body', 'http', 'invalid'])('bounds %s progress failures without inventing an update outcome', async failure => {
  let attempts = 0
  const fetch = vi.fn((_url, options) => {
    attempts++
    if (attempts === 1) {
      return Promise.resolve({ ok: true, json: async () => ({ status: 'running', message: 'Installing dependencies' }) })
    }
    if (failure === 'hung' || failure === 'hung-body') {
      const pending = () => new Promise((_resolve, reject) => {
        options.signal?.addEventListener('abort', () => reject(new Error('timeout')), { once: true })
      })
      return failure === 'hung' ? pending() : Promise.resolve({ ok: true, json: pending })
    }
    if (failure === 'http') return Promise.resolve({ ok: false })
    if (failure === 'invalid') return Promise.resolve({ ok: true, json: async () => ({}) })
    return Promise.reject(new Error('connection refused'))
  })
  const document = openPage(fetch)
  await vi.advanceTimersByTimeAsync(20_000)
  assert.equal(document.body.className, 'disconnected')
  assert.ok(attempts <= 4, `unbounded retry loop: ${attempts}`)
})

test.each(['steady', 'transient'])('preserves terminal truth with %s servers', async mode => {
  let attempts = 0
  const fetch = vi.fn(async () => {
    if (++attempts === 1 && mode === 'transient') throw new Error('temporary disconnect')
    return { ok: true, json: async () => ({ status: 'done' }) }
  })
  const document = openPage(fetch)
  await vi.advanceTimersByTimeAsync(20_000)
  assert.equal(document.body.className, 'done')
})

test('continues displaying a healthy long update while progress remains reachable', async () => {
  const fetch = vi.fn(async () => ({ ok: true, json: async () => ({ status: 'running', message: 'Building Desktop' }) }))
  const document = openPage(fetch)
  await vi.advanceTimersByTimeAsync(60_000)
  assert.equal(document.body.className, '')
  assert.equal(document.getElementById('line').textContent, 'Building Desktop')
})
