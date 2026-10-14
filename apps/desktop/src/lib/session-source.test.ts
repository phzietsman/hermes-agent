import { describe, expect, it } from 'vitest'

import { isMessagingSource, sessionSourceSearchTerms } from './session-source'

// Messaging sources get their own sidebar section. refreshMessagingSessions()
// filters rows through isMessagingSource(), so registration here is the sole
// condition that keeps their sessions out of generic recents.
describe('messaging source registration', () => {
  it('treats telegram as a messaging source (own sidebar section)', () => {
    expect(isMessagingSource('telegram')).toBe(true)
  })

  it('is case/space insensitive on the source id', () => {
    expect(isMessagingSource('TELEGRAM')).toBe(true)
    expect(isMessagingSource('  telegram ')).toBe(true)
  })

  it('exposes the search aliases so sessions are findable', () => {
    expect(sessionSourceSearchTerms('telegram')).toContain('tg')
  })

  it('does not flag local/CLI-ish sources as messaging (guard sanity)', () => {
    expect(isMessagingSource('cli')).toBe(false)
    expect(isMessagingSource(null)).toBe(false)
    expect(isMessagingSource(undefined)).toBe(false)
  })
})
