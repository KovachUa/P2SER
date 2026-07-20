import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react({
    jsxRuntime: 'automatic',
  })],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/__tests__/setup.js',
    css: {
      // Don't process CSS modules/imports in tests
      modules: { classNameStrategy: 'non-scoped' },
    },
    include: ['src/__tests__/**/*.test.{js,jsx}'],
    deps: {
      // Ensure all transforms go through vite pipeline (with react plugin)
      optimizer: {
        web: {
          include: ['@testing-library/react', '@testing-library/user-event'],
        },
      },
    },
  },
})
