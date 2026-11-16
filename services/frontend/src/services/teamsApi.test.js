import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// Stub auth helpers so authFetch can attach a header without real auth.
vi.mock('./auth', () => ({
  getAuthHeaders: () => ({ Authorization: 'Bearer test', 'Content-Type': 'application/json' }),
  refreshAccessToken: vi.fn(),
}))

import { detectTeams, saveTeams, skipOnboarding, listTeams, deleteTeamMember } from './api'

describe('Phase 7 — Teams API client (api.js)', () => {
  let originalFetch

  beforeEach(() => {
    originalFetch = global.fetch
  })

  afterEach(() => {
    global.fetch = originalFetch
    vi.restoreAllMocks()
  })

  function mockResponse({ ok = true, status = 200, json = {} } = {}) {
    return {
      ok,
      status,
      clone() { return this },
      json: () => Promise.resolve(json),
    }
  }

  // -------- detectTeams --------

  it('detectTeams POSTs and returns parsed DetectionResult on 200', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: true,
      json: { status: 'completed', teams: [], run_id: 'r1' },
    })))

    const result = await detectTeams()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [url, opts] = global.fetch.mock.calls[0]
    expect(url).toMatch(/\/teams\/detect$/)
    expect(opts.method).toBe('POST')
    expect(result).toEqual({ status: 'completed', teams: [], run_id: 'r1' })
  })

  it('detectTeams throws Error with .status + .code on 409', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: false,
      status: 409,
      json: { error: 'Tarama zaten devam ediyor', code: 'DETECT_RUNNING' },
    })))

    let caught
    try {
      await detectTeams()
    } catch (err) {
      caught = err
    }

    expect(caught).toBeInstanceOf(Error)
    expect(caught.status).toBe(409)
    expect(caught.code).toBe('DETECT_RUNNING')
  })

  // -------- saveTeams --------

  it('saveTeams POSTs JSON body and returns SaveResponse on 200', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: true,
      json: { status: 'completed', teams: [{ id: 't1', name: 'A', members: [] }] },
    })))

    const payload = { teams: [{ name: 'A', members: [{ name: 'X', email: 'x@e.com' }] }] }
    const result = await saveTeams(payload)

    const [url, opts] = global.fetch.mock.calls[0]
    expect(url).toMatch(/\/teams\/save$/)
    expect(opts.method).toBe('POST')
    expect(JSON.parse(opts.body)).toEqual(payload)
    expect(result.status).toBe('completed')
  })

  it('saveTeams throws Error with .code + .field on 400 validation', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: false,
      status: 400,
      json: { error: 'invalid email', code: 'INVALID_EMAIL', field: 'teams[0].members[1].email' },
    })))

    let caught
    try {
      await saveTeams({ teams: [] })
    } catch (err) {
      caught = err
    }

    expect(caught.status).toBe(400)
    expect(caught.code).toBe('INVALID_EMAIL')
    expect(caught.field).toBe('teams[0].members[1].email')
  })

  it('saveTeams surfaces ALREADY_COMPLETED on 409', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: false,
      status: 409,
      json: { error: 'already completed', code: 'ALREADY_COMPLETED' },
    })))

    let caught
    try {
      await saveTeams({ teams: [] })
    } catch (err) {
      caught = err
    }
    expect(caught.status).toBe(409)
    expect(caught.code).toBe('ALREADY_COMPLETED')
  })

  // -------- skipOnboarding --------

  it('skipOnboarding sends empty_state=true and returns status', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: true,
      json: { status: 'empty_state_skipped' },
    })))

    const result = await skipOnboarding(true)

    const [url, opts] = global.fetch.mock.calls[0]
    expect(url).toMatch(/\/teams\/skip$/)
    expect(opts.method).toBe('POST')
    expect(JSON.parse(opts.body)).toEqual({ empty_state: true })
    expect(result.status).toBe('empty_state_skipped')
  })

  it('skipOnboarding coerces falsy to false', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: true,
      json: { status: 'skipped' },
    })))

    await skipOnboarding(undefined)

    const [, opts] = global.fetch.mock.calls[0]
    expect(JSON.parse(opts.body)).toEqual({ empty_state: false })
  })

  // -------- listTeams --------

  it('listTeams GETs /teams and returns the teams payload', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: true,
      json: { teams: [] },
    })))

    const result = await listTeams()

    const [url, opts] = global.fetch.mock.calls[0]
    expect(url).toMatch(/\/teams$/)
    // GET — no explicit method, but absence is also fine
    expect(opts.method).toBeUndefined()
    expect(result).toEqual({ teams: [] })
  })

  // -------- deleteTeamMember --------

  it('deleteTeamMember DELETEs and returns nothing on 204', async () => {
    global.fetch = vi.fn(() => Promise.resolve({
      ok: true,
      status: 204,
      clone() { return this },
      json: () => Promise.resolve({}),
    }))

    const result = await deleteTeamMember('00000000-0000-0000-0000-000000000001')

    const [url, opts] = global.fetch.mock.calls[0]
    expect(url).toMatch(/\/teams\/members\/00000000-0000-0000-0000-000000000001$/)
    expect(opts.method).toBe('DELETE')
    expect(result).toBeUndefined()
  })

  it('deleteTeamMember throws Error with .status=404 on not-found', async () => {
    global.fetch = vi.fn(() => Promise.resolve(mockResponse({
      ok: false,
      status: 404,
      json: { error: 'not found' },
    })))

    let caught
    try {
      await deleteTeamMember('11111111-1111-1111-1111-111111111111')
    } catch (err) {
      caught = err
    }
    expect(caught.status).toBe(404)
  })
})
