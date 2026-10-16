/**
 * The translucency contract both processes read (`@hermes/shared/translucency`)
 * plus the one piece that needs a BrowserWindow to mean anything: which backing
 * a chat window is born with.
 *
 * The ramp assertions carry over from the clear-mode fix — its endpoints are
 * load-bearing, because a persisted intensity has to keep looking the same
 * across the upgrade that curved the middle of the lever.
 */

import { describe, expect, it } from 'vitest'

import {
  clampIntensity,
  DEFAULT_GLASS_MATERIAL,
  DEFAULT_GLASS_SCOPE,
  defaultTranslucencyState,
  defaultTranslucencyValues,
  GLASS_MATERIALS,
  GLASS_SCOPES,
  glassActive,
  type GlassMaterial,
  glassSupportedOn,
  glassSurfaceKeep,
  hudFrostFor,
  normalizeBook,
  normalizeMaterial,
  normalizeMode,
  normalizeScope,
  normalizeState,
  opacityNeedsSetting,
  resolveTranslucency,
  setTranslucencyValues,
  TRANSLUCENCY_CURVE,
  TRANSLUCENCY_MAX,
  TRANSLUCENCY_MIN,
  TRANSLUCENCY_OPACITY_FLOOR,
  type TranslucencyState,
  translucencySupportedOn,
  vibrancyFor,
  windowBackingOptions,
  windowOpacityFor,
  windowOpacityOptions
} from './translucency'

/** The linear ramp the curve replaced. Endpoints must still agree with it. */
const legacyOpacity = (intensity: number) => 1 - (intensity / 100) * 0.7

const clear = (intensity: number): TranslucencyState => ({
  intensity,
  fade: 0,
  mode: 'clear',
  material: DEFAULT_GLASS_MATERIAL,
  scope: DEFAULT_GLASS_SCOPE
})

const glass = (intensity: number, material: GlassMaterial = DEFAULT_GLASS_MATERIAL, fade = 0): TranslucencyState => ({
  intensity,
  fade,
  mode: 'glass',
  material,
  scope: DEFAULT_GLASS_SCOPE
})

describe('clampIntensity', () => {
  it('clamps to the lever bounds and rounds to a whole percent', () => {
    expect(clampIntensity(-5)).toBe(TRANSLUCENCY_MIN)
    expect(clampIntensity(0)).toBe(0)
    expect(clampIntensity(49.6)).toBe(50)
    expect(clampIntensity(100)).toBe(TRANSLUCENCY_MAX)
    expect(clampIntensity(250)).toBe(TRANSLUCENCY_MAX)
    expect(clampIntensity('35')).toBe(35)
  })

  it('treats junk as 0 (opaque) rather than letting it reach setOpacity', () => {
    expect(clampIntensity(undefined)).toBe(0)
    expect(clampIntensity(null)).toBe(0)
    expect(clampIntensity(NaN)).toBe(0)
    expect(clampIntensity(Infinity)).toBe(0)
    expect(clampIntensity('glass')).toBe(0)
  })
})

describe('normalizeMode', () => {
  it('accepts glass only on a platform that has a native material', () => {
    expect(normalizeMode('glass', true)).toBe('glass')
    expect(normalizeMode('glass', false)).toBe('clear')
  })

  it('honours an explicit choice', () => {
    expect(normalizeMode('clear', true)).toBe('clear')
    expect(normalizeMode('glass', true)).toBe('glass')
  })

  // Glass is pre-selected so the better half of the feature is the one you
  // find, which is free because the intensity still starts at 0.
  it('pre-selects glass when the platform supports it and nothing is recorded', () => {
    expect(normalizeMode(undefined, true)).toBe('glass')
    expect(normalizeMode('acrylic', true)).toBe('glass')
    expect(normalizeMode(42, true)).toBe('glass')
    expect(normalizeMode(undefined, false)).toBe('clear')
  })

  // The one case that must NOT flip: a profile carrying a non-zero intensity
  // with no mode has been rendering as clear since before glass existed, and
  // defaulting it to glass would change a window the user already tuned.
  it('leaves an already-tuned legacy profile on clear', () => {
    expect(normalizeMode(undefined, true, 45)).toBe('clear')
    expect(normalizeMode(undefined, true, 1)).toBe('clear')
    expect(normalizeMode(undefined, true, 0)).toBe('glass')

    // An explicit mode still wins over the legacy heuristic.
    expect(normalizeMode('glass', true, 45)).toBe('glass')
  })
})

describe('windowOpacityFor', () => {
  it('leaves both endpoints bit-identical to the linear ramp it replaced', () => {
    expect(windowOpacityFor(clear(TRANSLUCENCY_MIN))).toBe(legacyOpacity(0))
    expect(windowOpacityFor(clear(TRANSLUCENCY_MAX))).toBe(legacyOpacity(100))
  })

  it('is fully opaque at 0 and exactly the floor at 100', () => {
    expect(windowOpacityFor(clear(0))).toBe(1)
    expect(windowOpacityFor(clear(100))).toBe(1 - (1 - TRANSLUCENCY_OPACITY_FLOOR))
  })

  it('decreases monotonically and never sinks below the floor', () => {
    let previous = windowOpacityFor(clear(TRANSLUCENCY_MIN))

    for (let intensity = TRANSLUCENCY_MIN + 1; intensity <= TRANSLUCENCY_MAX; intensity += 1) {
      const opacity = windowOpacityFor(clear(intensity))

      expect(opacity, `${intensity} should be more see-through than ${intensity - 1}`).toBeLessThan(previous)
      expect(opacity, `${intensity} should stay at or above the floor`).toBeGreaterThanOrEqual(
        TRANSLUCENCY_OPACITY_FLOOR
      )

      previous = opacity
    }
  })

  // The bug the curve fixes: setOpacity fades text too, so anything under ~0.95
  // is where reading gets hard. A linear ramp put that boundary at intensity 7,
  // leaving almost the whole lever unusable.
  it('spends a useful stretch of the lever on readable settings, not its first few percent', () => {
    const readable = []

    for (let intensity = TRANSLUCENCY_MIN; intensity <= TRANSLUCENCY_MAX; intensity += 1) {
      if (windowOpacityFor(clear(intensity)) >= 0.95) {
        readable.push(intensity)
      }
    }

    expect(readable.length, `only ${readable.length} readable settings`).toBeGreaterThan(20)
  })

  it('keeps fine control near the opaque end', () => {
    // Linear gave 0.965 here — a visible jump off "off" on the very first step.
    expect(windowOpacityFor(clear(5))).toBeGreaterThan(0.99)
    expect(windowOpacityFor(clear(1))).toBeGreaterThan(legacyOpacity(1))
    expect(TRANSLUCENCY_CURVE).toBeGreaterThan(1)
  })

  it('clamps before mapping so corrupt state cannot escape the range', () => {
    expect(windowOpacityFor(clear(-40))).toBe(1)
    expect(windowOpacityFor(clear(240))).toBe(windowOpacityFor(clear(TRANSLUCENCY_MAX)))
  })

  // The tint is painted by the renderer, so the intensity lever must never
  // reach setOpacity under glass — that separation is what keeps text sharp.
  it('ignores the intensity lever entirely in glass mode', () => {
    expect(windowOpacityFor(glass(0))).toBe(1)
    expect(windowOpacityFor(glass(60))).toBe(1)
    expect(windowOpacityFor(glass(100))).toBe(1)
  })

  it('fades a glass window only through its own lever, on the ramp clear uses', () => {
    expect(windowOpacityFor(glass(60, DEFAULT_GLASS_MATERIAL, 0))).toBe(1)
    expect(windowOpacityFor(glass(60, DEFAULT_GLASS_MATERIAL, 40))).toBe(windowOpacityFor(clear(40)))
    expect(windowOpacityFor(glass(100, DEFAULT_GLASS_MATERIAL, 100))).toBe(windowOpacityFor(clear(100)))
  })

  it('leaves fade inert under clear, where the intensity lever already is the opacity', () => {
    expect(windowOpacityFor({ ...clear(40), fade: 100 })).toBe(windowOpacityFor(clear(40)))
  })
})

describe('glassSurfaceKeep', () => {
  // Full range on purpose: the top of the lever is bare, untinted blur, so the
  // slider spans opaque theme -> clean glass. Text and cards keep their own
  // opaque tokens, which is what makes 100% usable rather than unreadable.
  it('runs linear to zero so the top of the lever is untinted glass', () => {
    expect(glassSurfaceKeep(0)).toBe(100)
    expect(glassSurfaceKeep(50)).toBe(50)
    expect(glassSurfaceKeep(100)).toBe(0)
  })

  it('clamps its input like every other consumer of the lever', () => {
    expect(glassSurfaceKeep(-40)).toBe(100)
    expect(glassSurfaceKeep(240)).toBe(0)
  })
})

describe('normalizeMaterial / normalizeScope', () => {
  it('accepts every shipped material and area', () => {
    for (const material of GLASS_MATERIALS) {
      expect(normalizeMaterial(material)).toBe(material)
    }

    for (const scope of GLASS_SCOPES) {
      expect(normalizeScope(scope)).toBe(scope)
    }
  })

  // The picker was rebuilt from a material census precisely because several
  // NSVisualEffectView materials composite identically; a value that isn't in
  // the shipped ladder must not reach setVibrancy.
  it('falls back for junk, and for materials deliberately left out of the ladder', () => {
    expect(normalizeMaterial('sidebar')).toBe(DEFAULT_GLASS_MATERIAL)
    expect(normalizeMaterial('hud')).toBe(DEFAULT_GLASS_MATERIAL)
    expect(normalizeMaterial(undefined)).toBe(DEFAULT_GLASS_MATERIAL)
    expect(normalizeMaterial(7)).toBe(DEFAULT_GLASS_MATERIAL)
    expect(normalizeScope('rail')).toBe(DEFAULT_GLASS_SCOPE)
    expect(normalizeScope(null)).toBe(DEFAULT_GLASS_SCOPE)
  })
})

describe('vibrancyFor', () => {
  it('uses the chosen frost material only while glass is actually on', () => {
    expect(vibrancyFor(glass(60, 'header'))).toBe('header')
    expect(vibrancyFor(glass(60, 'popover'))).toBe('popover')
  })

  // 'sidebar' is what the titlebar band was designed against, so every
  // non-glass state has to land back on it rather than keeping a stale pick.
  it('falls back to the long-standing sidebar material otherwise', () => {
    expect(vibrancyFor(glass(0, 'header'))).toBe('sidebar')
    expect(vibrancyFor(clear(60))).toBe('sidebar')
  })
})

// The HUD is a transparent window, so its frost has no opaque page to hide
// behind: every state that isn't "frost wanted" has to resolve to no material
// at all, or the band leaves a grey slab hanging over another app.
describe('hudFrostFor', () => {
  it('wears the chosen frost while the band is showing', () => {
    expect(hudFrostFor(glass(60, 'header'), true)).toEqual({ vibrancy: 'header' })
    expect(hudFrostFor(glass(60, 'under-window'), true)).toEqual({
      vibrancy: 'under-window'
    })
  })

  // The material is the whole window rectangle and nothing on the page can
  // clip it, so a hidden band must mean no frost — this is the veto that keeps
  // idle HUD mode the bar and nothing else.
  it('is off whenever the band is not covering the window', () => {
    expect(hudFrostFor(glass(60, 'header'), false)).toEqual({ vibrancy: null })
  })

  // ...and the setting is the other veto: Glass off, or the tint at zero,
  // means the HUD never frosts however engaged the band is.
  it('is off whenever glass itself is off', () => {
    expect(hudFrostFor(clear(60), true)).toEqual({ vibrancy: null })
    expect(hudFrostFor(glass(0, 'header'), true)).toEqual({ vibrancy: null })
  })

  // The tint is painted by the renderer, exactly as it is for a chat window —
  // dragging it must not re-issue setVibrancy, whose 150ms animation restarts
  // on every call and never lets the material settle.
  it('does not move any native property as the tint slider is dragged', () => {
    for (let intensity = 1; intensity <= 100; intensity += 1) {
      expect(hudFrostFor(glass(intensity, 'popover'), true)).toEqual({
        vibrancy: 'popover'
      })
    }
  })
})

describe('glassSupportedOn', () => {
  it('is on for macOS regardless of kernel version', () => {
    expect(glassSupportedOn('darwin')).toBe(true)
    expect(glassSupportedOn('darwin')).toBe(true)
  })

  it('is off on Linux — Electron has no first-party desktop material there', () => {
    expect(glassSupportedOn('linux')).toBe(false)
  })
})

describe('translucencySupportedOn', () => {
  it('covers the platform where setOpacity or a native material exists', () => {
    expect(translucencySupportedOn('darwin')).toBe(true)
  })

  // Electron documents setOpacity as doing nothing on Linux, and there is no
  // material either — so the setting has no working half to offer there.
  it('is off on Linux, where neither mode does anything', () => {
    expect(translucencySupportedOn('linux')).toBe(false)
    expect(translucencySupportedOn('freebsd')).toBe(false)
  })
})

describe('normalizeState', () => {
  it('parses a modern payload', () => {
    expect(
      normalizeState({ intensity: 40, fade: 15, mode: 'glass', material: 'header', scope: 'sidebar' }, true)
    ).toEqual({
      intensity: 40,
      fade: 15,
      mode: 'glass',
      material: 'header',
      scope: 'sidebar'
    })
  })

  // The migration contract: a pre-glass translucency.json is intensity-only and
  // always meant clear. It must NOT silently become glass on update.
  it('keeps a legacy intensity-only payload on clear', () => {
    expect(normalizeState({ intensity: 70 }, true)).toEqual({
      intensity: 70,
      fade: 0,
      mode: 'clear',
      material: DEFAULT_GLASS_MATERIAL,
      scope: DEFAULT_GLASS_SCOPE
    })
  })

  // Fade arrived after glass shipped, so a profile written by the older build
  // has no key for it and must come back unfaded rather than undefined.
  it('defaults a payload written before fade existed to no fade', () => {
    expect(normalizeState({ intensity: 60, mode: 'glass' }, true).fade).toBe(0)
  })

  it('survives junk payloads', () => {
    const base = { intensity: 0, fade: 0, material: DEFAULT_GLASS_MATERIAL, scope: DEFAULT_GLASS_SCOPE }

    // A fresh glass-capable profile lands on glass at zero intensity: selected, but off.
    expect(normalizeState(null, true)).toEqual({ ...base, mode: 'glass' })
    expect(normalizeState('nope', true)).toEqual({ ...base, mode: 'glass' })
    expect(
      normalizeState({ intensity: 'x', fade: 'x', material: 'nope', mode: 'glass', scope: 'nope' }, false)
    ).toEqual({
      ...base,
      mode: 'clear'
    })
  })
})

describe('glassActive', () => {
  it('is on only for glass with nonzero intensity', () => {
    expect(glassActive(glass(60))).toBe(true)
    expect(glassActive(glass(0))).toBe(false)
    expect(glassActive(clear(60))).toBe(false)
  })
})

// The default must be selected-but-off: a fresh glass-capable profile shows
// Glass in the picker while the window itself is untouched until the lever moves.
describe('a fresh profile', () => {
  const fresh = normalizeState(null, true)

  it('pre-selects glass with the feature still off', () => {
    expect(fresh.mode).toBe('glass')
    expect(fresh.intensity).toBe(TRANSLUCENCY_MIN)
    expect(glassActive(fresh)).toBe(false)
  })

  it('leaves the window exactly as it is today', () => {
    expect(windowOpacityFor(fresh)).toBe(1)
    expect(windowBackingOptions(fresh, '#101014')).toEqual({ backgroundColor: '#101014' })
  })
})

describe('windowBackingOptions', () => {
  // The cold-launch bug: `backgroundColor: '#00000000'` on a non-transparent
  // window is silently treated as OPAQUE, so a window born while glass was
  // persisted blocked the vibrancy material no matter how clear the page went.
  // Omitting the key entirely is the only shape that works, and a runtime
  // setBackgroundColor fixup is lost in a fresh window's first seconds.
  it('omits backgroundColor entirely while glass is active', () => {
    expect(windowBackingOptions(glass(60), '#111111')).toEqual({})
    expect('backgroundColor' in windowBackingOptions(glass(60), '#111111')).toBe(false)
  })

  it('keeps the themed anti-flash backing in every other state', () => {
    expect(windowBackingOptions(glass(0), '#111111')).toEqual({ backgroundColor: '#111111' })
    expect(windowBackingOptions(clear(60), '#111111')).toEqual({ backgroundColor: '#111111' })
    expect(windowBackingOptions(clear(0), '#f7f7f7')).toEqual({ backgroundColor: '#f7f7f7' })
  })
})

// A glass window is fully opaque natively — the tint is the renderer's and fade
// defaults to zero — so the no-op `opacity: 1` ask must not be made.
describe('windowOpacityOptions', () => {
  it('omits opacity entirely when the window is fully opaque', () => {
    expect(windowOpacityOptions(glass(60))).toStrictEqual({})
    expect(windowOpacityOptions(clear(0))).toStrictEqual({})
  })

  it('passes it the moment the state actually fades', () => {
    for (const state of [clear(1), clear(100), glass(60, DEFAULT_GLASS_MATERIAL, 1)]) {
      expect(windowOpacityOptions(state)).toStrictEqual({ opacity: windowOpacityFor(state) })
    }
  })
})

describe('opacityNeedsSetting', () => {
  it('skips the call for an opaque window that has never been faded', () => {
    expect(opacityNeedsSetting(1, 1)).toBe(false)
  })

  it('makes the call whenever the target fades', () => {
    expect(opacityNeedsSetting(0.5, 1)).toBe(true)
    expect(opacityNeedsSetting(0.9999, 1)).toBe(true)
  })

  // The way back. A window already at 0.5 has paid for the layering, so
  // returning it to opaque is free — and skipping it would strand the fade.
  it('makes the call to return an already-faded window to opaque', () => {
    expect(opacityNeedsSetting(1, 0.5)).toBe(true)
  })
})

// The jank fix, as a contract: dragging the intensity slider under glass must
// not touch ANY native property. Each tick used to re-issue setVibrancy, whose
// 150ms animation then restarted before macOS could settle the material —
// which is both the stutter and the reason the frost levels read alike.
describe('what an update actually changes natively', () => {
  const nativeDiff = (previous: TranslucencyState, next: TranslucencyState) => ({
    backing: glassActive(previous) !== glassActive(next),
    material: vibrancyFor(previous) !== vibrancyFor(next),
    opacity: windowOpacityFor(previous) !== windowOpacityFor(next)
  })

  it('is nothing at all while dragging intensity under glass', () => {
    for (let intensity = 41; intensity <= 100; intensity += 1) {
      expect(nativeDiff(glass(intensity - 1), glass(intensity)), `at ${intensity}`).toEqual({
        backing: false,
        material: false,
        opacity: false
      })
    }
  })

  it('is only the opacity while dragging intensity under clear', () => {
    expect(nativeDiff(clear(40), clear(41))).toEqual({ backing: false, material: false, opacity: true })
  })

  // The one glass drag that reaches main, and it costs what a clear drag costs.
  it('is only the opacity while dragging fade under glass', () => {
    expect(nativeDiff(glass(60, DEFAULT_GLASS_MATERIAL, 40), glass(60, DEFAULT_GLASS_MATERIAL, 41))).toEqual({
      backing: false,
      material: false,
      opacity: true
    })
  })

  it('is the material alone when the frost level changes', () => {
    expect(nativeDiff(glass(60, 'under-window'), glass(60, 'header'))).toEqual({
      backing: false,
      material: true,
      opacity: false
    })
  })

  // Crossing zero flips glass on/off, which is exactly when the backing has to
  // move — the one intensity change that is NOT free.
  it('is the backing and material when glass crosses zero', () => {
    expect(nativeDiff(glass(0), glass(1))).toEqual({ backing: true, material: true, opacity: false })
    expect(nativeDiff(glass(1), glass(0))).toEqual({ backing: true, material: true, opacity: false })
  })

  it('is everything when switching between the two modes', () => {
    expect(nativeDiff(clear(60), glass(60))).toEqual({ backing: true, material: true, opacity: true })
  })

  it('leaves a window alone when glass is selected but off', () => {
    // A saved fade must not follow the tint to zero: off means opaque.
    expect(windowOpacityFor({ ...glass(0), fade: 1 })).toBe(1)
    expect(windowOpacityFor({ ...glass(0), fade: 40 })).toBe(1)
  })

  it('still fades a window whose glass is actually on', () => {
    expect(windowOpacityFor({ ...glass(66), fade: 40 })).toBeLessThan(1)
  })
})

/** Fresh profiles share the sidebar treatment, with native frost per platform. */
describe('the defaults a fresh profile lands on', () => {
  const mac = (appearance: 'dark' | 'light') => defaultTranslucencyValues(appearance)

  it('ships glass on, not a lever resting at zero', () => {
    for (const values of [mac('light'), mac('dark')]) {
      expect(values.intensity).toBeGreaterThan(0)
      expect(glassActive({ ...values, mode: 'glass' })).toBe(true)
    }

    for (const appearance of ['light', 'dark'] as const) {
      expect(defaultTranslucencyState(appearance, true).mode).toBe('glass')
      expect(defaultTranslucencyState(appearance, true).mode).toBe('glass')
    }
  })

  it('falls back to clear where no native material exists', () => {
    expect(defaultTranslucencyState('dark', false).mode).toBe('clear')
  })

  it('keeps the content column opaque at the native level', () => {
    for (const values of [mac('light'), mac('dark')]) {
      expect(windowOpacityFor({ ...values, mode: 'glass' })).toBe(1)
    }
  })

  it('uses the normalized scope default for every appearance and platform', () => {
    for (const values of [mac('light'), mac('dark')]) {
      expect(values.scope).toBe(normalizeScope(undefined))
    }
  })
})

/**
 * The per-appearance ladder: appearance slot → base → platform default, per
 * key. This is what makes tuning light mode stay in light mode while an
 * untouched dark keeps inheriting.
 */
describe('resolving the book for the painted appearance', () => {
  const empty = normalizeBook(null, true)

  it('agrees with the native first-window defaults in either appearance', () => {
    for (const appearance of ['light', 'dark'] as const) {
      expect(resolveTranslucency(empty, appearance)).toEqual(defaultTranslucencyState(appearance, true))
    }
  })

  it('scopes an edit to the appearance it was made in', () => {
    const book = setTranslucencyValues(empty, 'light', { intensity: 90 })

    expect(resolveTranslucency(book, 'light').intensity).toBe(90)
    expect(resolveTranslucency(book, 'dark').intensity).toBe(defaultTranslucencyValues('dark').intensity)
  })

  it('preserves a saved whole-window treatment in both appearances', () => {
    const saved = { intensity: 40, scope: 'window', mode: 'glass' } as const
    const migrated = normalizeBook(saved, true)

    expect(migrated.base).toEqual({ intensity: saved.intensity, scope: saved.scope })

    for (const appearance of ['light', 'dark'] as const) {
      expect(resolveTranslucency(migrated, appearance)).toMatchObject(saved)
    }
  })

  it('lets an appearance override base without disturbing the other', () => {
    const tuned = setTranslucencyValues(normalizeBook({ intensity: 40, mode: 'glass' }, true), 'dark', {
      intensity: 10
    })

    expect(resolveTranslucency(tuned, 'dark').intensity).toBe(10)
    expect(resolveTranslucency(tuned, 'light').intensity).toBe(40)
  })

  it('inherits per KEY, not per appearance', () => {
    // Editing only the tint in dark must leave dark's material still tracking
    // base — a partial edit is not a full snapshot of the appearance.
    const book = setTranslucencyValues(normalizeBook({ material: 'popover', mode: 'glass' }, true), 'dark', {
      intensity: 33
    })

    const resolved = resolveTranslucency(book, 'dark')

    expect(resolved.intensity).toBe(33)
    expect(resolved.material).toBe('popover')
  })

  it('keeps mode global — clear vs glass is about the window, not the palette', () => {
    const book = setTranslucencyValues({ ...empty, mode: 'clear' }, 'light', { intensity: 50 })

    expect(resolveTranslucency(book, 'light').mode).toBe('clear')
    expect(resolveTranslucency(book, 'dark').mode).toBe('clear')
  })
})
