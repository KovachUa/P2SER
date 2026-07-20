import '@testing-library/jest-dom'
import React from 'react'
global.React = React
window.React = React

// Mock localStorage
const localStorageMock = (() => {
  let store = {}
  return {
    getItem: (key) => store[key] ?? null,
    setItem: (key, value) => { store[key] = String(value) },
    removeItem: (key) => { delete store[key] },
    clear: () => { store = {} },
  }
})()
Object.defineProperty(window, 'localStorage', { value: localStorageMock })

// Mock window.location for API_BASE / API_WS_BASE
Object.defineProperty(window, 'location', {
  value: {
    protocol: 'http:',
    hostname: 'localhost',
    host: 'localhost:5173',
    href: 'http://localhost:5173/',
    pathname: '/',
    search: '',
    hash: '',
  },
  writable: true,
})

// Mock fetch globally
global.fetch = vi.fn(() =>
  Promise.resolve({
    ok: true,
    json: () => Promise.resolve({}),
    text: () => Promise.resolve(''),
  })
)

// Mock WebSocket
global.WebSocket = vi.fn(() => ({
  send: vi.fn(),
  close: vi.fn(),
  readyState: 1,
  OPEN: 1,
  addEventListener: vi.fn(),
  removeEventListener: vi.fn(),
}))

// Mock navigator.hardwareConcurrency
Object.defineProperty(navigator, 'hardwareConcurrency', { value: 4 })

// Suppress xterm console warnings in tests
vi.mock('@xterm/xterm', () => ({
  Terminal: vi.fn(() => ({
    open: vi.fn(),
    write: vi.fn(),
    writeln: vi.fn(),
    onData: vi.fn(),
    dispose: vi.fn(),
    loadAddon: vi.fn(),
  })),
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: vi.fn(() => ({
    fit: vi.fn(),
  })),
}))
