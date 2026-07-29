import { defineConfig } from 'vitest/config';

// Phase 0 test infrastructure: a lightweight runner for app/*.js modules.
// No plugins/bundling needed — app.js is plain, dependency-free JS loaded via
// <script> tags in the browser; Vitest runs it directly under Node for unit
// testing the pure, DOM-free helper functions it exposes (see the "Test
// hook" block at the bottom of app/app.js).
export default defineConfig({
  test: {
    environment: 'node',
    include: ['app/__tests__/**/*.test.js'],
  },
});
