import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config';

// Unit tests: happy-dom, the same Vite pipeline as the build (aliases, SFCs).
// Browser checks (real engines, layout, contrast) live in browser/ and run in
// the official Playwright image.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'happy-dom',
      include: ['tests/**/*.test.ts'],
      restoreMocks: true,
    },
  }),
);
