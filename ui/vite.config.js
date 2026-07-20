import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],

  // Збірка йде в директорію поруч з main.go для go:embed
  build: {
    outDir: '../backend/cmd/p2ser/ui/dist',
    emptyOutDir: true,
  },

  // Dev-режим: проксі API запитів до Go бекенду
  server: {
    proxy: {
      '/pods':       'http://localhost:8002',
      '/pod':        'http://localhost:8002',
      '/compose':    'http://localhost:8002',
      '/upload':     'http://localhost:8002',
      '/deploy-git': 'http://localhost:8002',
      '/stats':      'http://localhost:8002',
      '/nodes':      'http://localhost:8002',
      '/apply':      'http://localhost:8002',
      '/state':      'http://localhost:8002',
      '/ls':         'http://localhost:8002',
      '/ban':        'http://localhost:8002',
      '/bot':        'http://localhost:8002',
    },
  },
})
