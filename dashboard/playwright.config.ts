import { defineConfig, devices } from "@playwright/test";

/**
 * Browser tests against a real Homebase running in a VM.
 *
 * Not against a mock. The milestone's exit condition is phrased as something a
 * person does — "opens the dashboard, creates an administrator, sees accurate
 * system information, reboots the machine" — and a mocked API would let all of
 * that pass while the real thing was broken. `make vm-test-dashboard` starts the
 * VM and points these tests at its forwarded port.
 */
const baseURL = process.env["HOMEBASE_URL"] ?? "http://127.0.0.1:8080";

export default defineConfig({
  testDir: "./tests/e2e",
  // Serial, and in a deliberate order. These are one continuous journey against
  // one machine: the first file creates the administrator that the rest sign in
  // as, and several of them restart the server, so nothing else can be talking
  // to it at the time.
  //
  // Playwright orders files alphabetically and offers no other cross-file
  // ordering, which is why the names are numbered. That is not decoration —
  // when the applications spec came before the first-run spec it failed on the
  // setup screen, and the reason was invisible.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env["CI"],
  retries: 0,
  reporter: [["list"]],
  timeout: 120_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    // A home server's dashboard is used on a laptop and a phone. The phone case
    // gets its own project once there is enough interface for it to matter.
    ...devices["Desktop Chrome"],
  },
});
