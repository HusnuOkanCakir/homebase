import { test, expect } from "@playwright/test";

/**
 * Somebody joining a household, in a browser.
 *
 * This used to be the recovery screen, and it greeted a new arrival with a page
 * headed "Use your recovery code" warning that everything signed in would be
 * signed out. Nothing had been. The test is here because the fault was entirely
 * in what the person was shown, and no API test can see that.
 */

const ADMIN = process.env["HOMEBASE_ADMIN"] ?? "alex";
const PASSWORD = process.env["HOMEBASE_PASSWORD"] ?? "a-sufficiently-long-password";
const THEIRS = "a-password-of-their-own";

/**
 * A name nobody on this server has yet.
 *
 * Fixed names made this pass once and fail every time afterwards: the second
 * run tried to invite somebody who already existed, got "somebody already has
 * that name", and failed thirty seconds later waiting for a screen that was
 * never going to appear. These specs run against a server that keeps its state.
 */
function newcomer(): string {
  return "guest" + Math.random().toString(36).slice(2, 8).replace(/[^a-z]/g, "x");
}

test.describe.configure({ mode: "serial" });

test("an administrator invites somebody, and they join", async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto("/");

  const setupName = page.getByLabel("Your name");
  if (await setupName.isVisible().catch(() => false)) {
    const again = page.getByLabel("Password again");
    await setupName.fill(ADMIN);
    await page.getByLabel("Password", { exact: true }).fill(PASSWORD);
    if (await again.isVisible().catch(() => false)) {
      await again.fill(PASSWORD);
      await page.getByRole("button", { name: /set up this server/i }).click();
      await expect(page.getByRole("heading", { name: "Write this down" })).toBeVisible();
      await page.getByRole("checkbox").first().check();
      await page.getByRole("button", { name: /continue|done|finish/i }).first().click();
    } else {
      await page.getByRole("button", { name: /sign in/i }).click();
    }
  }

  await page.getByRole("button", { name: "Settings" }).click();
  const joiner = newcomer();
  await page.getByText("Add somebody").click();
  await page.getByLabel("Their name").fill(joiner);
  await page.getByRole("button", { name: /add them/i }).click();

  // The code takes over the screen. It is the one thing here that cannot be
  // done again tomorrow.
  await expect(page.getByRole("heading", { name: "Write this down" })).toBeVisible({
    timeout: 30_000,
  });
  const code = (await page.getByTestId("recovery-code").innerText()).trim();
  expect(code).toMatch(/^[A-Z0-9]{5}(-[A-Z0-9]{5}){4}$/);

  // And it says the code expires, which is the difference between this and a
  // recovery code and the thing somebody has to know before putting it away.
  await expect(page.getByText(/week/i).first()).toBeVisible();

  await page.getByRole("checkbox").first().check();
  await page.getByRole("button", { name: /continue|done|finish/i }).first().click();

  // Now as the person who was invited, in a browser that has never seen this
  // server.
  const theirs = await page.context().browser()!.newContext();
  const them = await theirs.newPage();
  await them.goto(page.url().split("#")[0]);

  await them.getByRole("button", { name: /i have a joining code/i }).click();
  await expect(them.getByRole("heading", { name: "Join this server" })).toBeVisible();

  // Nothing on this screen tells a new arrival that they are recovering
  // anything, or that they are about to be signed out of everywhere.
  const body = (await them.locator("body").innerText()).toLowerCase();
  expect(body).not.toContain("signed out");
  expect(body).not.toContain("forgotten");

  await them.getByLabel("Your name").fill(joiner);
  await them.getByLabel("Joining code").fill(code);
  await them.getByLabel("Choose a password").fill(THEIRS);
  await them.getByLabel("The same password again").fill(THEIRS);
  await them.getByRole("button", { name: "Join", exact: true }).click();

  // A recovery code of their own, shown once, before they go anywhere.
  await expect(them.getByRole("heading", { name: "Write this down" })).toBeVisible({
    timeout: 30_000,
  });
  const recovery = (await them.getByTestId("recovery-code").innerText()).trim();
  expect(recovery).toMatch(/^[A-Z0-9]{5}(-[A-Z0-9]{5}){4}$/);
  expect(recovery).not.toEqual(code);

  await them.getByRole("checkbox").first().check();
  await them.getByRole("button", { name: /continue|done|finish/i }).first().click();

  // And they are in, as themselves, with the Files screen a Member gets.
  await expect(them.getByRole("button", { name: "Files" })).toBeVisible();
  await expect(them.getByText(joiner).first()).toBeVisible();

  await theirs.close();
});

test("a joining code works once", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");

  await page.getByRole("button", { name: /i have a joining code/i }).click();
  await page.getByLabel("Your name").fill(newcomer());
  await page.getByLabel("Joining code").fill("AAAAA-BBBBB-CCCCC-DDDDD-EEEEE");
  await page.getByLabel("Choose a password").fill("another-password-entirely");
  await page.getByLabel("The same password again").fill("another-password-entirely");
  await page.getByRole("button", { name: "Join", exact: true }).click();

  await expect(page.getByText(/joining code is not right/i)).toBeVisible();

  // And it says what to do about it, because an expired code and a mistyped one
  // look identical to the person holding it.
  await expect(page.getByText(/ask for another|week/i).first()).toBeVisible();
});
