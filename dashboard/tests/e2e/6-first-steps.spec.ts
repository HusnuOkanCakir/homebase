import { test, expect, type Page } from "@playwright/test";

/**
 * Milestone 6's other half: what a server says to somebody who has just claimed
 * it and does not know what to do next.
 *
 * Runs last. It renames the machine, which changes the answer the restart
 * confirmation demands and the heading every other spec waits for — so anything
 * following it would be looking for a server that no longer goes by that name.
 */

const ADMIN = "alex";
const PASSWORD = "a-password-chosen-after-recovery";
const SERVER_NAME = process.env["HOMEBASE_HOSTNAME"] ?? "homebase-dash";
const NEW_NAME = "living-room";

test.describe.configure({ mode: "serial" });

test("the list says what is left, and does not invent a sequence", async ({ page }) => {
  await signIn(page);

  const steps = page.locator("section.card", { hasText: "Getting started" });
  await expect(steps).toBeVisible();

  await expect(steps.getByText(/no hurry/i)).toBeVisible();
  await expect(steps.getByText(/nothing here has to be done in order/i)).toBeVisible();

  // Whatever is left says *why*, not only what — an instruction is something to
  // obey, a reason is something to weigh. Only unfinished steps say anything:
  // a list that explains what you have already done is a list nobody reads.
  const unfinished = steps.locator("li.step:not(.step-done)");
  await expect(unfinished.first()).toBeVisible();
  for (const step of await unfinished.all()) {
    await expect(
      step.locator("p.muted"),
      "a step with no reason given is an instruction to obey",
    ).toBeVisible();
  }
});

test("the steps reflect what the server has actually got", async ({ page }) => {
  await signIn(page);

  const steps = page.locator("section.card", { hasText: "Getting started" });

  // By this point in the journey a disk is set up and a backup exists, so those
  // steps are done and the list stops asking. Nothing is remembered to work
  // that out — it is what the server currently reports, which is why removing
  // the disk would bring the step back.
  for (const done of ["Set up a disk for your files", "Make a backup"]) {
    await expect(
      steps.locator("li.step-done", { hasText: done }),
      `"${done}" is already true of this server and should not still be asked for`,
    ).toBeVisible();
  }

  // And the application was installed and then removed earlier in this journey,
  // so this one is genuinely outstanding again.
  await expect(steps.locator("li.step:not(.step-done)", { hasText: "Install something" }))
    .toBeVisible();
});

test("a name the machine cannot have is refused before the button", async ({ page }) => {
  await signIn(page);
  await openRename(page);

  const field = page.getByLabel("What would you like to call it?");
  const submit = page.getByRole("button", { name: "Use this name" });

  await expect(submit).toBeDisabled();

  for (const bad of ["my server", "kitchen.local", "-leading", "café"]) {
    await field.fill(bad);
    await expect(submit, `"${bad}" cannot be a machine's name`).toBeDisabled();
  }

  await expect(page.getByText(/no spaces or accents/i)).toBeVisible();

  await field.fill(NEW_NAME);
  await expect(submit).toBeEnabled();
});

test("renaming the server, and the whole dashboard agreeing", async ({ page }) => {
  await signIn(page);
  await openRename(page);

  await page.getByLabel("What would you like to call it?").fill(NEW_NAME);
  await page.getByRole("button", { name: "Use this name" }).click();

  await expect(page.getByText(new RegExp(`now called ${NEW_NAME}`, "i"))).toBeVisible({
    timeout: 30_000,
  });

  // The machine's own name, everywhere it is shown. The heading is read from
  // the server rather than from what was typed, so this is the machine
  // agreeing rather than the page remembering.
  await expect(page.getByRole("heading", { name: NEW_NAME })).toBeVisible({
    timeout: 30_000,
  });

  await page.reload();
  await expect(page.getByRole("heading", { name: NEW_NAME })).toBeVisible();

  // And the machine reports it under Technical details, which is where somebody
  // goes to check what a server is actually called. Opened by clicking the
  // summary rather than the group: a closed <details> is not reliably a
  // clickable target, and waiting for one that never resolves is a two-minute
  // timeout rather than a failed assertion.
  await page.locator("summary", { hasText: "Technical details" }).click();
  await expect(page.locator("details.details")).toContainText(NEW_NAME);
});

test("restarting now asks for the new name, not the old one", async ({ page }) => {
  await signIn(page);

  await page.getByRole("button", { name: /restart this server/i }).click();

  const confirm = page.getByRole("button", { name: "Restart now" });
  await expect(confirm).toBeDisabled();

  // The name the machine had when it was installed is no longer its name, and
  // the confirmation has to follow — otherwise renaming quietly breaks the one
  // question that stops an accidental restart.
  await page.getByLabel(/Server name/).fill(SERVER_NAME);
  await expect(confirm, "the old name should no longer confirm anything").toBeDisabled();

  await page.getByLabel(/Server name/).fill(NEW_NAME);
  await expect(confirm).toBeEnabled();

  await page.getByRole("button", { name: "Cancel" }).click();
});

// --- Helpers -----------------------------------------------------------------

/**
 * Open the rename form under "This server".
 *
 * Deliberately not the one in the getting-started list. That one is only
 * offered while the machine still has the name the installer gave it, and this
 * machine does not — which is exactly the case worth testing, because renaming
 * a server somebody has been running for a year is the harder promise to keep.
 */
async function openRename(page: Page) {
  const field = page.getByLabel("What would you like to call it?");

  // `isVisible` reports the page as it is *now* and does not wait, so asking it
  // before the overview has rendered answers "no" and skips the click — which
  // is how this first failed, waiting fifteen seconds for a form nothing had
  // opened. `expect` waits; that is the difference.
  const rename = page.getByRole("button", { name: "Rename this server" });
  await expect(rename).toBeVisible();
  await rename.click();

  await expect(field).toBeVisible();
}

async function signIn(page: Page) {
  await page.goto("/");

  const signOut = page.getByRole("button", { name: "Sign out" });
  if (await signOut.isVisible().catch(() => false)) {
    return;
  }

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}
