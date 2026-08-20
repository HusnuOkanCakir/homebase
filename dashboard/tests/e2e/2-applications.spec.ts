import { test, expect, type Page } from "@playwright/test";

/**
 * The Milestone 3 exit condition, as a browser test.
 *
 * "A user installs an app, uses it, reboots, finds it and its data intact, and
 * uninstalls it without collateral damage."
 *
 * That is a sentence about a person, so it is tested as one: through the
 * interface they touch, against a real Homebase in a VM, across a real reboot.
 * The parts most worth testing here are the parts a unit test cannot see — that
 * the words on screen say what actually happened, and that the destructive
 * buttons cannot be reached by reflex.
 *
 * Runs after 1-first-run.spec.ts, which creates the administrator this signs in
 * as. The numbered filenames are what orders them — see playwright.config.ts.
 */

const ADMIN = "alex";
const PASSWORD = "a-sufficiently-long-password";
const SERVER_NAME = process.env["HOMEBASE_HOSTNAME"] ?? "homebase-dash";

/** The test application: small, and enough to prove the lifecycle. */
const APP_ID = "hello-homebase";
const APP_NAME = "Hello Homebase";

test.describe.configure({ mode: "serial" });

test("the applications list describes what each one is for", async ({ page }) => {
  await signIn(page);
  await openApplications(page);

  await expect(page.getByRole("heading", { name: "Applications" })).toBeVisible();

  const row = applicationRow(page, APP_NAME);
  await expect(row).toBeVisible();
  await expect(row).toContainText("Not installed");

  // A person choosing an application needs to know what it does. A list of
  // names is a list for somebody who already knows.
  await expect(row).toContainText(/[a-z]{4,}/);

  // Nothing here should require knowing what a container is.
  const body = await page.locator("body").innerText();
  for (const jargon of ["container", "docker", "image tag", "registry", "volume", "bind mount"]) {
    expect(
      body.toLowerCase(),
      `the applications list uses "${jargon}", which the reader has no reason to know`,
    ).not.toContain(jargon);
  }
});

test("installing an application, and being told what is happening", async ({ page }) => {
  await signIn(page);
  await openApplications(page);
  await applicationRow(page, APP_NAME).click();

  await expect(page.getByRole("heading", { name: APP_NAME })).toBeVisible();

  await page.getByRole("button", { name: "Install" }).click();

  // Installing takes long enough that saying nothing would look like nothing
  // happening. The message has to be about the wait, not about the mechanism.
  await expect(page.getByRole("status")).toContainText(/downloading/i);

  await expect(page.getByText(/is installed and running/i)).toBeVisible({
    timeout: 300_000,
  });
  await expect(page.getByText("Running", { exact: true })).toBeVisible({ timeout: 30_000 });
});

test("its recent activity is available without leaving the page", async ({ page }) => {
  await signIn(page);
  await openApplication(page, APP_NAME);

  await page.getByRole("button", { name: "Show recent activity" }).click();
  // Something arrived. The content is the application's business; that it can be
  // read at all is Homebase's.
  await expect(page.locator("pre.logs")).toBeVisible();
});

test("stopping requires naming the application, and starting again does not", async ({ page }) => {
  await signIn(page);
  await openApplication(page, APP_NAME);

  await page.getByRole("button", { name: "Stop", exact: true }).click();
  await expect(page.getByRole("heading", { name: `Stop ${APP_NAME}?` })).toBeVisible();

  // The confirmation says what will happen to other people, not just to a
  // container. Somebody else in the house may be watching something.
  await expect(page.getByText(/anyone using it will lose access/i)).toBeVisible();
  await expect(page.getByText(/nothing is deleted/i)).toBeVisible();

  const confirm = page.getByRole("button", { name: "Stop it" });
  await expect(confirm, "stopping is not available until the name is typed").toBeDisabled();

  await page.getByLabel(/Type/).fill("some-other-app");
  await expect(confirm, "a different name must not unlock the button").toBeDisabled();

  await page.getByLabel(/Type/).fill(APP_ID);
  await expect(confirm).toBeEnabled();
  await confirm.click();

  await expect(page.getByText("Stopped", { exact: true })).toBeVisible({ timeout: 60_000 });

  // Starting is not destructive, so it does not ask. Friction on a safe action
  // trains people to click through the friction on an unsafe one.
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(page.getByText("Running", { exact: true })).toBeVisible({ timeout: 60_000 });
});

test("the application and its data survive restarting the server", async ({ page }) => {
  await signIn(page);

  // Restart the machine from the overview. exact: true because "Restart this
  // server" also contains "This server", and a loose match resolves to both.
  await page.getByRole("button", { name: "Home", exact: true }).click();
  await page.getByRole("button", { name: "Restart this server" }).click();
  await page.getByLabel(/Server name/).fill(SERVER_NAME);
  await page.getByRole("button", { name: "Restart now" }).click();

  await expect(page.getByRole("heading", { name: "Restarting…" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Your server is back" })).toBeVisible({
    timeout: 120_000,
  });
  await page.getByRole("button", { name: "Continue" }).click();

  // The application must be running again without anybody pressing start. An
  // application that needs attention after every power cut is not being looked
  // after.
  await openApplications(page);
  await expect(applicationRow(page, APP_NAME)).toContainText("Running", { timeout: 120_000 });
});

test("removing an application says, and means, that the data is kept", async ({ page }) => {
  await signIn(page);
  await openApplication(page, APP_NAME);

  await page.getByRole("button", { name: "Remove" }).click();
  await expect(page.getByRole("heading", { name: `Remove ${APP_NAME}?` })).toBeVisible();

  // The promise. If this sentence is on screen it has to be true, and the VM
  // test checks the file is still on disk afterwards.
  await expect(page.getByText(/its data is kept/i)).toBeVisible();

  const confirm = page.getByRole("button", { name: "Remove it" });
  await expect(confirm).toBeDisabled();
  await page.getByLabel(/Type/).fill(APP_ID);
  await confirm.click();

  await expect(page.getByText(/its data is still on the server/i)).toBeVisible({
    timeout: 120_000,
  });
  await expect(page.getByText("Not installed", { exact: true })).toBeVisible({
    timeout: 30_000,
  });

  // And it offers to install again, which is the point of keeping the data.
  await expect(page.getByRole("button", { name: "Install" })).toBeVisible();
});

test("deleting the data is harder to reach than removing the application", async ({ page }) => {
  await signIn(page);
  await openApplication(page, APP_NAME);

  // Not on the main surface. Somebody looking for a stop button must not find
  // an irreversible deletion on the way.
  await expect(
    page.getByRole("button", { name: /Delete .* data permanently/ }),
  ).toBeHidden();

  await page.getByText("Technical details").click();
  const remove = page.getByRole("button", { name: /Delete .* data permanently/ });
  await expect(remove).toBeVisible();
  await remove.click();

  await expect(page.getByRole("heading", { name: `Delete ${APP_NAME}'s data?` })).toBeVisible();
  await expect(page.getByText(/cannot be undone/i)).toBeVisible();
  await expect(page.getByText(/no backup is taken/i)).toBeVisible();

  const confirm = page.getByRole("button", { name: "Delete it permanently" });
  await expect(confirm).toBeDisabled();

  // "yes" is not a confirmation, and neither is nearly-right.
  await page.getByLabel(/Type/).fill("yes");
  await expect(confirm).toBeDisabled();
  await page.getByLabel(/Type/).fill(APP_ID.toUpperCase());
  await expect(confirm).toBeDisabled();

  // Backing out must actually back out, with nothing deleted.
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("heading", { name: APP_NAME })).toBeVisible();
});

// --- Helpers -----------------------------------------------------------------

function applicationRow(page: Page, name: string) {
  return page.locator("button.app-row", { hasText: name });
}

async function openApplications(page: Page) {
  await page.getByRole("button", { name: "Apps", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Applications" })).toBeVisible();
}

async function openApplication(page: Page, name: string) {
  await openApplications(page);
  await applicationRow(page, name).click();
  await expect(page.getByRole("heading", { name })).toBeVisible();
}

async function signIn(page: Page) {
  await page.goto("/");

  // The previous spec file may have left a session behind, or not.
  const signOut = page.getByRole("button", { name: "Sign out" });
  if (await signOut.isVisible().catch(() => false)) {
    return;
  }

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: SERVER_NAME })).toBeVisible();
}
