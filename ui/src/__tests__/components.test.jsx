/**
 * P2SER UI — Component Rendering Tests
 *
 * Перевіряє що всі ключові компоненти рендеряться без помилок,
 * навігація працює, і елементи UI присутні.
 */
import React from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '../App.jsx'

describe('App: Initial render', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('p2ser_api_token', 'test-token')
    fetch.mockClear()
    fetch.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ cpu: '1.23', ram_used: '4.2 GB', ram_total: '16 GB' }),
        text: () => Promise.resolve(''),
      })
    )
  })

  it('should render P2SER logo in sidebar', () => {
    render(<App />)
    expect(screen.getByText('P2SER')).toBeInTheDocument()
  })

  it('should render sidebar navigation items', () => {
    render(<App />)
    expect(screen.getByText('Dashboard')).toBeInTheDocument()
    expect(screen.getByText('Projects')).toBeInTheDocument()
    expect(screen.getByText('Compose→P2SER')).toBeInTheDocument()
    expect(screen.getByText('Settings')).toBeInTheDocument()
  })

  it('should show "Cluster Overview" title on dashboard by default', () => {
    render(<App />)
    expect(screen.getByText('Cluster Overview')).toBeInTheDocument()
  })

  it('should show Admin profile', () => {
    render(<App />)
    expect(screen.getByText('Admin')).toBeInTheDocument()
    expect(screen.getByText('A')).toBeInTheDocument() // avatar
  })

  it('should display stat cards on dashboard', async () => {
    render(<App />)
    await waitFor(() => {
      expect(screen.getByText('Host CPU Load')).toBeInTheDocument()
      expect(screen.getByText('Host RAM Used')).toBeInTheDocument()
      expect(screen.getByText('Active Pods')).toBeInTheDocument()
    })
  })

  it('should display "Running Pods" section on dashboard', () => {
    render(<App />)
    expect(screen.getByText('Running Pods')).toBeInTheDocument()
  })
})

describe('App: Navigation', () => {
  beforeEach(() => {
    localStorage.setItem('p2ser_api_token', 'test')
    fetch.mockClear()
    fetch.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ cpu: '0', ram_used: '0', ram_total: '0' }),
        text: () => Promise.resolve(''),
      })
    )
  })

  it('should switch to Projects tab', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Projects'))
    expect(screen.getByText('Namespaces & Projects')).toBeInTheDocument()
    expect(screen.getByText('Namespaces')).toBeInTheDocument()
    expect(screen.getByText('+ Deploy Complex Project')).toBeInTheDocument()
  })

  it('should switch to Settings tab', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Settings'))
    expect(screen.getAllByText('Cluster Settings')[0]).toBeInTheDocument()
    expect(screen.getByText('API Authentication')).toBeInTheDocument()
    expect(screen.getByText('Network & Domains')).toBeInTheDocument()
    expect(screen.getByText('Security & Secrets')).toBeInTheDocument()
  })

  it('should switch to Converter tab', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Compose→P2SER'))
    expect(screen.getByText('Compose → P2SER Converter')).toBeInTheDocument()
  })

  it('should switch back to Dashboard', async () => {
    render(<App />)
    const user = userEvent.setup()

    await user.click(screen.getByText('Settings'))
    expect(screen.getAllByText('Cluster Settings')[0]).toBeInTheDocument()
    
    await user.click(screen.getByText('Dashboard'))
    expect(screen.getByText('Cluster Overview')).toBeInTheDocument()
  })
})

describe('App: Settings — API Token', () => {
  beforeEach(() => {
    localStorage.clear()
    fetch.mockClear()
    fetch.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ cpu: '0', ram_used: '0', ram_total: '0' }),
        text: () => Promise.resolve(''),
      })
    )
  })

  it('should render the API token input field', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Settings'))
    
    const input = screen.getByPlaceholderText('Enter your API token...')
    expect(input).toBeInTheDocument()
    expect(input).toHaveAttribute('type', 'password')
  })

  it('should have a Save button for the token', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Settings'))
    
    expect(screen.getByText('Save')).toBeInTheDocument()
  })

  it('should save token to localStorage when Save is clicked', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Settings'))
    
    const input = screen.getByPlaceholderText('Enter your API token...')
    
    // Clear and type new token
    await user.clear(input)
    await user.type(input, 'new-secure-token-456')
    
    // Mock alert
    const alertMock = vi.spyOn(window, 'alert').mockImplementation(() => {})
    
    await user.click(screen.getByText('Save'))
    
    expect(localStorage.getItem('p2ser_api_token')).toBe('new-secure-token-456')
    expect(alertMock).toHaveBeenCalledWith('Token saved! All API requests will now use this token.')
    
    alertMock.mockRestore()
  })

  it('should load existing token from localStorage', async () => {
    localStorage.setItem('p2ser_api_token', 'pre-existing-token')
    
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Settings'))
    
    const input = screen.getByPlaceholderText('Enter your API token...')
    expect(input).toHaveValue('pre-existing-token')
  })
})

describe('App: Projects — Deploy Modal', () => {
  beforeEach(() => {
    localStorage.setItem('p2ser_api_token', 'test')
    fetch.mockClear()
    fetch.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ cpu: '0', ram_used: '0', ram_total: '0' }),
        text: () => Promise.resolve(''),
      })
    )
  })

  it('should open Deploy modal when button is clicked', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Projects'))
    await user.click(screen.getByText('+ Deploy Complex Project'))
    
    expect(screen.getByText('Deploy Complex Project')).toBeInTheDocument()
    expect(screen.getByText('📁 Upload Folder')).toBeInTheDocument()
    expect(screen.getByText('🌐 Git URL')).toBeInTheDocument()
  })

  it('should have project name input in modal', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Projects'))
    await user.click(screen.getByText('+ Deploy Complex Project'))
    
    const nameInput = screen.getByPlaceholderText('e.g. My P2SER App')
    expect(nameInput).toBeInTheDocument()
  })

  it('should switch between Folder and Git mode', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Projects'))
    await user.click(screen.getByText('+ Deploy Complex Project'))
    
    // Default is folder mode
    expect(screen.getByText('Browse OS')).toBeInTheDocument()
    
    // Switch to Git mode
    await user.click(screen.getByText('🌐 Git URL'))
    expect(screen.getByPlaceholderText('https://github.com/user/repo.git')).toBeInTheDocument()
  })

  it('should close modal when Cancel is clicked', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Projects'))
    await user.click(screen.getByText('+ Deploy Complex Project'))
    expect(screen.getByText('Deploy Complex Project')).toBeInTheDocument()
    
    await user.click(screen.getByText('Cancel'))
    // Modal title should disappear (only one "Deploy Complex Project" should remain - the header)
    expect(screen.queryByText('Deploy Complex Project')).not.toBeInTheDocument()
  })

  it('should have resource quota sliders', async () => {
    render(<App />)
    const user = userEvent.setup()
    
    await user.click(screen.getByText('Projects'))
    await user.click(screen.getByText('+ Deploy Complex Project'))
    
    expect(screen.getByText('CPU Quota (Cores)')).toBeInTheDocument()
    expect(screen.getByText('RAM Quota (GB)')).toBeInTheDocument()
    expect(screen.getByText('Storage Quota (GB)')).toBeInTheDocument()
    expect(screen.getByText('Replicas (Active)')).toBeInTheDocument()
    expect(screen.getByText('Standby (Warm Reserve)')).toBeInTheDocument()
  })
})
