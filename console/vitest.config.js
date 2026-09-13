import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    // core tests are pure JS; the mermaid-parsing test opts into jsdom
    // per-file via // @vitest-environment jsdom
    environment: 'node',
    include: ['src/**/*.{test,spec}.js'],
  },
})
