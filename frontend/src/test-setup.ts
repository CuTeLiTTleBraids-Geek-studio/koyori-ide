// G-CI-11: vitest setup — neutralize timers that outlive jsdom teardown.
//
// @wailsio/runtime/dist/drag.js starts a 50ms polling interval as a module
// side effect (up to ~5s, self-clearing). Under coverage runs a test file can
// finish before the poll self-clears; after the jsdom environment is torn
// down the residual interval callback touches `window` and surfaces as an
// unhandled ReferenceError, failing the coverage job with a false positive.
//
// The wails drag handlers are pure window wiring that tests never exercise,
// so tracking and clearing every interval registered during a test file (in a
// global afterAll, i.e. after the file's tests completed) is safe and does
// not change test behavior. fake-timer suites keep working: they replace
// setInterval/clearInterval entirely and restore the wrapped versions.
import { afterAll } from "vitest";

const liveIntervals = new Set<ReturnType<typeof setInterval>>();

const originalSetInterval = globalThis.setInterval.bind(globalThis);
const originalClearInterval = globalThis.clearInterval.bind(globalThis);

globalThis.setInterval = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
  const id = originalSetInterval(handler, timeout, ...args);
  liveIntervals.add(id);
  return id;
}) as typeof globalThis.setInterval;

globalThis.clearInterval = ((id: ReturnType<typeof setInterval>) => {
  liveIntervals.delete(id);
  return originalClearInterval(id);
}) as typeof globalThis.clearInterval;

afterAll(() => {
  for (const id of liveIntervals) {
    originalClearInterval(id);
  }
  liveIntervals.clear();
});
