/**
 * P2SER UI — API Integration Tests
 *
 * Перевіряє що API запити правильно формуються,
 * використовують динамічний токен та правильні endpoints.
 */
import React from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '../App.jsx'

describe('API: Dashboard data fetching', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('p2ser_api_token', 'dashboard-test-token')
    fetch.mockClear()
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('should fetch /stats and /pods on mount', async () => {
    fetch.mockImplementation((url) => {
      if (url.includes('/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ cpu: '2.5', ram_used: '8.1 GB', ram_total: '32 GB' }),
        })
      }
      if (url.includes('/pods')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([]),
        })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}), text: () => Promise.resolve('') })
    })

    render(<App />)

    await waitFor(() => {
      expect(fetch).toHaveBeenCalled()
    })

    const statsCalls = fetch.mock.calls.filter(c => c[0].includes('/stats'))
    const podsCalls = fetch.mock.calls.filter(c => c[0].includes('/pods'))

    expect(statsCalls.length).toBeGreaterThanOrEqual(1)
    expect(podsCalls.length).toBeGreaterThanOrEqual(1)

    // Verify token is included
    expect(statsCalls[0][0]).toContain('token=dashboard-test-token')
    expect(podsCalls[0][0]).toContain('token=dashboard-test-token')
  })

  it('should display real stats from API', async () => {
    fetch.mockImplementation((url) => {
      if (url.includes('/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ cpu: '3.14', ram_used: '12.5 GB', ram_total: '64 GB' }),
        })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('3.14')).toBeInTheDocument()
      expect(screen.getByText('12.5 GB')).toBeInTheDocument()
    })
  })

  it('should display pods from API', async () => {
    fetch.mockImplementation((url) => {
      if (url.includes('/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ cpu: '0', ram_used: '0', ram_total: '0' }),
        })
      }
      if (url.includes('/pods')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            { id: 'web-active-0', app: 'web', image: 'nginx:latest', status: 'running' },
            { id: 'db-active-0', app: 'db', image: 'postgres:15', status: 'running' },
          ]),
        })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
    })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('web')).toBeInTheDocument()
      expect(screen.getByText('db')).toBeInTheDocument()
    })
  })

  it('should handle backend offline gracefully', async () => {
    fetch.mockImplementation(() => Promise.reject(new Error('Connection refused')))

    // Should NOT throw
    expect(() => render(<App />)).not.toThrow()

    await waitFor(() => {
      // App should still render
      expect(screen.getByText('P2SER')).toBeInTheDocument()
    })
  })
})

describe('API: Pod actions', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('p2ser_api_token', 'action-token')
    fetch.mockClear()
    fetch.mockImplementation((url) => {
      if (url.includes('/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ cpu: '0', ram_used: '0', ram_total: '0' }),
        })
      }
      if (url.includes('/pods') && !url.includes('/pod/')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            { id: 'test-pod-1', app: 'myapp', image: 'alpine:latest', status: 'running' },
          ]),
        })
      }
      return Promise.resolve({ ok: true, text: () => Promise.resolve('OK'), json: () => Promise.resolve({}) })
    })
  })

  it('should call restart API with correct token and pod id', async () => {
    const alertMock = vi.spyOn(window, 'alert').mockImplementation(() => {})
    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('myapp')).toBeInTheDocument()
    })

    const user = userEvent.setup()
    await user.click(screen.getByText('Restart'))

    await waitFor(() => {
      const restartCalls = fetch.mock.calls.filter(c =>
        c[0].includes('/pod/restart')
      )
      expect(restartCalls.length).toBe(1)
      expect(restartCalls[0][0]).toContain('id=test-pod-1')
      expect(restartCalls[0][0]).toContain('token=action-token')
      expect(restartCalls[0][1].method).toBe('POST')
    })

    alertMock.mockRestore()
  })

  it('should call delete API with DELETE method', async () => {
    const alertMock = vi.spyOn(window, 'alert').mockImplementation(() => {})
    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('myapp')).toBeInTheDocument()
    })

    const user = userEvent.setup()
    await user.click(screen.getByText('Stop'))

    await waitFor(() => {
      const deleteCalls = fetch.mock.calls.filter(c =>
        c[0].includes('/pod?id=')
      )
      expect(deleteCalls.length).toBe(1)
      expect(deleteCalls[0][0]).toContain('id=test-pod-1')
      expect(deleteCalls[0][0]).toContain('token=action-token')
      expect(deleteCalls[0][1].method).toBe('DELETE')
    })

    alertMock.mockRestore()
  })

  it('should call logs API for pod', async () => {
    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('myapp')).toBeInTheDocument()
    })

    const user = userEvent.setup()
    await user.click(screen.getByText('Logs'))

    await waitFor(() => {
      const logsCalls = fetch.mock.calls.filter(c =>
        c[0].includes('/pod/logs')
      )
      expect(logsCalls.length).toBe(1)
      expect(logsCalls[0][0]).toContain('id=test-pod-1')
      expect(logsCalls[0][0]).toContain('token=action-token')
    })
  })
})
