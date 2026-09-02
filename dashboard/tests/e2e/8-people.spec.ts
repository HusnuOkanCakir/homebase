import { test, expect } from "@playwright/test";

/**
 * Adding somebody to the household, in a browser.
 *
 * The joining code is the reason this test exists. It is shown once, and a
 * screen that loses it leaves a person unable to sign in with nothing to say
 * so — the kind of failure that is invisible to an API test and obvious in a
 * browser.
 */

const ADMIN = process.env["HOMEBASE_ADMIN"] ?? "alex";
const PASSWORD = process.env["HOMEBASE_PASSWORD"] ?? "a-sufficiently-long-password";

test.describe.configure({ mode: "serial" });

test("adding somebody, and the code that is shown once", async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto("/");

  // Either a server nobody has claimed, or one this suite already claimed.
  const setupName = page.getByLabel("Your name");
  if (await setupName.isVisible().catch(() => false)) {
    await setupName.fill(ADMIN);
    await page.getByLabel("Password", { exact: true }).fill(PASSWORD);
    const again = page.getByLabel("Password again");
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
  await expect(page.getByRole("heading", { name: "People" })).toBeVisible();

  // The administrator is not described as never having signed in. They set the
  // server up and are reading this screen — first-run setup issues a session
  // without going through the login path, and that used to leave the flag unset.
  //
  // Their row, not the whole page. Somebody who has been invited and has not
  // arrived yet is *correctly* described that way, and asserting across the
  // screen made this pass on a server with one account and fail on any server
  // that had ever been used.
  await expect(
    page.locator("li").filter({ hasText: ADMIN }).first(),
  ).not.toContainText("has not signed in yet");

  // A name nobody here has yet, for the same reason: these specs run against a
  // server that keeps what previous runs put in it.
  const invited = "father" + Math.random().toString(36).slice(2, 6).replace(/[^a-z]/g, "x");
  await page.getByText("Add somebody").click();
  await page.getByLabel("Their name").fill(invited);

  // Roles are chosen by reading a sentence about each, not by decoding a word.
  await expect(page.getByText(/can reach every file on it/i)).toBeVisible();

  // The screen says what a role does not decide, which is the thing people
  // assume it does.
  await expect(page.getByText(/folder of their own/i)).toBeVisible();

  await page.getByRole("button", { name: /add them/i }).click();

  // The code takes over the screen, with the same treatment as a recovery code.
  await expect(page.getByRole("heading", { name: "Write this down" })).toBeVisible({
    timeout: 30_000,
  });
  const code = (await page.getByTestId("recovery-code").innerText()).trim();
  expect(code.length, "no joining code was shown").toBeGreaterThan(10);

  // There is no way past it without saying the code has been taken down.
  await page.getByRole("checkbox").first().check();
  await page.getByRole("button", { name: /continue|done|finish/i }).first().click();

  const row = page.locator("li").filter({ hasText: invited }).first();
  await expect(row).toBeVisible();
  // Somebody who has been invited and has not arrived is described that way.
  await expect(row).toContainText("has not signed in yet");

  // Removing somebody says what it does not do.
  await row.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByText(/Their files are kept/)).toBeVisible();
  const confirm = page.getByRole("button", { name: new RegExp(`^Remove ${invited}$`) });
  await expect(confirm, "removal is possible without typing the name").toBeDisabled();
  await page.getByLabel(/Type .* to confirm/).fill(invited);
  await expect(confirm).toBeEnabled();
});
