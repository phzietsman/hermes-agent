import { describe, expect, it } from 'vitest'

import { cleanPath, isUnderPath } from './path-compare'

describe('cleanPath', () => {
  it('drops surrounding whitespace and trailing slashes', () => {
    expect(cleanPath('  /home/user/repo//  ')).toBe('/home/user/repo')
  })

  it('keeps root rather than collapsing to empty', () => {
    expect(cleanPath('/')).toBe('/')
  })
})

describe('isUnderPath', () => {
  it('matches the path itself and nested paths', () => {
    expect(isUnderPath('/home/user/repo/', '/home/user/repo')).toBe(true)
    expect(isUnderPath('/home/user/repo', '/home/user/repo/src')).toBe(true)
  })

  it('stays case-sensitive', () => {
    expect(isUnderPath('/home/user/repo', '/home/user/Repo/src')).toBe(false)
  })

  it('does not treat a sibling with a shared prefix as nested', () => {
    expect(isUnderPath('/repos/app', '/repos/app-retry')).toBe(false)
  })
})
