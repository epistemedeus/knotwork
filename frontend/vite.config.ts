import { defineConfig } from 'vite'

// Assets are embedded into the Go binary and served from /static/.
export default defineConfig({
  base: '/static/',
  build: {
    outDir: '../internal/web/static',
    emptyOutDir: true,
    rollupOptions: {
      input: 'src/main.ts',
      output: {
        entryFileNames: 'app.js',
        chunkFileNames: 'chunks/[name]-[hash].js',
        assetFileNames: (assetInfo) => {
          if (assetInfo.name?.endsWith('.css')) return 'app.css'
          return 'assets/[name]-[hash][extname]'
        },
      },
    },
  },
})
