/**
 * P2SER UI — Security Tests
 * 
 * Перевіряє що хардкодовані секрети прибрані,
 * токен зберігається в localStorage, а API URLs динамічні.
 */
import React from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import App from '../App.jsx'

describe('Security: No hardcoded tokens', () => {
  beforeEach(() => {
    localStorage.clear()
    fetch.mockClear()
    fetch.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ cpu: '0.5', ram_used: '2 GB', ram_total: '8 GB' }),
        text: () => Promise.resolve('ok'),
      })
    )
  })

  it('should NOT have hardcoded "p2ser-secret-token" anywhere in source', async () => {
    const fs = await import('fs')
    const source = fs.readFileSync('/app/src/App.jsx', 'utf8')
    
    const matches = source.match(/p2ser-secret-token/g)
    expect(matches).toBeNull()
  })

  it('should NOT have hardcoded "localhost:8002" anywhere in source', async () => {
    const fs = await import('fs')
    const source = fs.readFileSync('/app/src/App.jsx', 'utf8')
    
    const matches = source.match(/localhost:8002/g)
    expect(matches).toBeNull()
  })

  it('should read API token from localStorage', () => {
    localStorage.setItem('p2ser_api_token', 'my-test-token-123')
    
    render(<App />)
    
    // Check that fetch was called with the token from localStorage
    const fetchCalls = fetch.mock.calls
    expect(fetchCalls.length).toBeGreaterThan(0)
    
    const hasCorrectToken = fetchCalls.some(call =>
      call[0].includes('token=my-test-token-123')
    )
    expect(hasCorrectToken).toBe(true)
  })

  it('should use dynamic API_BASE derived from window.location', () => {
    localStorage.setItem('p2ser_api_token', 'test')
    
    render(<App />)
    
    const fetchCalls = fetch.mock.calls
    expect(fetchCalls.length).toBeGreaterThan(0)
    
    // All fetch calls should use http://localhost:8002 (from window.location.hostname)
    const allUseDynamicBase = fetchCalls.every(call =>
      call[0].startsWith('http://localhost:8002/')
    )
    expect(allUseDynamicBase).toBe(true)
  })

  it('should handle empty token gracefully (no crash)', () => {
    localStorage.clear()
    
    // Should not throw
    expect(() => render(<App />)).not.toThrow()
  })
})
