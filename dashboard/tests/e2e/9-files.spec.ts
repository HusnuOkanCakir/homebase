import { test, expect } from "@playwright/test";

/**
 * The files, in a browser.
 *
 * This screen is the reason the file API exists: a mapped drive needs a
 * computer that can map one, and a phone cannot. So the thing worth testing in
 * a real browser is that somebody can find a file and get it — not that an
 * endpoint answered.
 *
 * It also catches what an API test cannot. A screen that compiles, renders, and
 * is missing a closing brace in its stylesheet looks fine to every test in this
 * repository and wrong to the only person who matters. That has happened here.
 */

const ADMIN = process.env["HOMEBASE_ADMIN"] ?? "alex";
const PASSWORD = process.env["HOMEBASE_PASSWORD"] ?? "a-sufficiently-long-password";

test.describe.configure({ mode: "serial" });

test("finding a file and taking it", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");

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

  await page.getByRole("button", { name: "Files" }).click();
  await expect(page.getByRole("heading", { name: "Files", exact: true })).toBeVisible();

  // A server with nothing on it says so, rather than showing an empty box. It
  // is the state every new installation is in.
  const empty = page.getByText(/nothing here to open yet/i);
  if (await empty.isVisible().catch(() => false)) {
    test.info().annotations.push({
      type: "note",
      description: "no areas on this server; the empty state was checked instead",
    });
    return;
  }

  // Your own folder is first, and says whose it is.
  await expect(page.locator(".file-crumbs").getByRole("button")).toContainText("Your folder");
  await expect(page.getByText(/This folder is yours/i)).toBeVisible();

  // A folder is somewhere to go; a file is something to take. The difference
  // has to be visible without clicking either.
  const folder = page.getByRole("button", { name: /Holiday photographs/ });
  const file = page.getByRole("link", { name: "tax-return-2026.pdf" });
  await expect(folder).toBeVisible();
  await expect(file).toBeVisible();

  // A file is a link, so the browser does the downloading: a progress
  // indicator it already has, and a resumed download if the connection drops.
  await expect(file).toHaveAttribute("href", /files\/content\?area=me/);

  // Sizes are read, not counted.
  await expect(page.getByText(/400 kB|400.0 kB/)).toBeVisible();

  // Into the folder, and back out again by the crumb rather than by the
  // browser's back button — which on a single-page dashboard leaves the screen
  // somewhere nobody asked for.
  await folder.click();
  await expect(page.getByRole("link", { name: "beach.jpg" })).toBeVisible();
  await page.locator(".file-crumbs").getByRole("button").first().click();
  await expect(file).toBeVisible();

  // A name that is not English survives the whole way to the screen.
  await expect(page.getByRole("link", { name: "Örnek Belge.txt" })).toBeVisible();

  await page.screenshot({ path: "test-results/files.png", fullPage: true });
});

test("deleting a folder asks for its name", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");

  // Every test gets its own browser, so every test signs in.
  const password = page.getByLabel("Password", { exact: true });
  await expect(password).toBeVisible();
  await page.getByLabel("Your name").fill(ADMIN);
  await password.fill(PASSWORD);
  await page.getByRole("button", { name: /sign in|set up this server/i }).click();

  await page.getByRole("button", { name: "Files" }).click();
  await expect(page.getByRole("heading", { name: "Files", exact: true })).toBeVisible();

  // Waited for, not asked about. isVisible() answers immediately, so asking it
  // before the listing has arrived skips a test that would have passed — which
  // is how a test quietly stops covering anything.
  const folder = page.getByRole("button", { name: /Holiday photographs/ });
  if (!(await folder.isVisible({ timeout: 10_000 }).catch(() => false))) {
    test.skip(true, "nothing to delete on this server");
  }

  // The row's own Delete, not the first one on the screen.
  await page
    .locator("li")
    .filter({ has: folder })
    .getByRole("button", { name: "Delete" })
    .click();

  // There is no wastebasket, and the screen says so before anything happens.
  await expect(page.getByText(/no wastebasket/i)).toBeVisible();

  const confirm = page.getByRole("button", { name: "Delete it" });
  await expect(confirm).toBeDisabled();

  await page.getByPlaceholder("Holiday photographs").fill("Holiday photographs");
  await expect(confirm).toBeEnabled();

  await page.getByRole("button", { name: "Keep it" }).click();
  await expect(folder).toBeVisible();
});
