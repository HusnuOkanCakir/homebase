import { test, expect, type Page } from "@playwright/test";

/**
 * Milestone 4's exit condition, as a browser test.
 *
 * "A USB disk can be added as Jellyfin's media storage, removed and reconnected
 * without corrupting anything."
 *
 * The VM test proves the machine half — that the disk comes back, that the file
 * survives, that not even root can write to a bare mountpoint. This is the half
 * about the person: whether they are told what is happening, and whether the
 * things that destroy data can be reached by accident.
 *
 * Storage is the screen where a mistake is unrecoverable, so most of what is
 * asserted here is a refusal.
 *
 * Runs after 1-first-run.spec.ts, which creates the administrator.
 */

const ADMIN = "okan";
const PASSWORD = "a-sufficiently-long-password";
const SERVER_NAME = process.env["HOMEBASE_HOSTNAME"] ?? "homebase-dash";

/** What the location is called once set up. */
const DISK_NAME = "Films drive";
const DISK_ID = "films-drive";

test.describe.configure({ mode: "serial" });

test("the disk the server runs from cannot be touched", async ({ page }) => {
  await signIn(page);
  await openStorage(page);

  const systemDisk = page.locator("li.storage-row", { hasText: "The server itself" });
  await expect(systemDisk).toBeVisible();

  // Shown, labelled, and given no buttons. Not "disabled" — absent.
  await expect(systemDisk.getByRole("button", { name: /Erase/ })).toHaveCount(0);
  await expect(systemDisk.getByRole("button", { name: /Use this/ })).toHaveCount(0);
  await expect(systemDisk).toContainText(/cannot be changed/i);
});

test("erasing a disk says what is on it, and cannot be done by reflex", async ({ page }) => {
  await signIn(page);
  await openStorage(page);

  const blank = usbDisk(page);
  await expect(blank).toBeVisible();

  await blank.getByRole("button", { name: "Erase and prepare" }).click();

  await expect(page.getByRole("heading", { name: "Erase this disk?" })).toBeVisible();
  await expect(page.getByText(/cannot be undone/i)).toBeVisible();
  await expect(page.getByText(/no backup is taken/i)).toBeVisible();

  const confirm = page.getByRole("button", { name: "Erase it permanently" });
  await expect(confirm, "erasing is not available until the disk is named").toBeDisabled();

  // A word anybody would type is not a confirmation.
  await page.getByLabel(/Type/).fill("yes");
  await expect(confirm).toBeDisabled();
  await page.getByLabel(/Type/).fill("/dev/sdz");
  await expect(confirm).toBeDisabled();

  // Backing out must actually back out.
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("heading", { name: "Erase this disk?" })).toBeHidden();
});

test("preparing a blank disk, and setting it up", async ({ page }) => {
  await signIn(page);
  await openStorage(page);

  const blank = usbDisk(page);
  await blank.getByRole("button", { name: "Erase and prepare" }).click();

  // A blank disk is described as blank — not as unreadable, which is what a
  // disk Homebase cannot open would say, and which is never offered for
  // erasing at all.
  await expect(page.getByText(/found nothing on this disk/i)).toBeVisible();

  const identifier = await page.locator("label[for='erase-confirm'] code").innerText();
  await page.getByLabel("What would you like to call it?").fill("Media");
  await page.getByLabel(/Type/).fill(identifier);
  await page.getByRole("button", { name: "Erase it permanently" }).click();

  await expect(page.getByText(/The disk is ready to use/i)).toBeVisible({ timeout: 300_000 });

  // Now set it up as a location, which is a separate decision.
  await expect(usbDisk(page).getByRole("button", { name: "Use this" })).toBeVisible({
    timeout: 30_000,
  });
  await usbDisk(page).getByRole("button", { name: "Use this" }).click();

  await expect(page.getByText(/Nothing on it is changed/i)).toBeVisible();
  await page.getByLabel("What would you like to call it?").fill(DISK_NAME);
  await page.getByRole("button", { name: "Use this disk" }).click();

  await expect(page.getByText(new RegExp(`${DISK_NAME} is ready`, "i"))).toBeVisible({
    timeout: 120_000,
  });

  // And it appears under the user's own storage, with space reported.
  const location = page.locator("li.storage-row", { hasText: DISK_NAME });
  await expect(location).toBeVisible();
  await expect(location).toContainText("Ready");
  await expect(location).toContainText(/free of/);
});

test("an application says it needs a disk, and takes the one it is given", async ({ page }) => {
  await signIn(page);

  await page.getByRole("button", { name: "Applications", exact: true }).click();
  await page.locator("button.app-row", { hasText: "File Browser" }).click();

  // Before anything else on the screen: it cannot run yet, and why.
  await expect(page.getByText(/needs somewhere to keep your files/i)).toBeVisible();

  // The choice is offered, and nothing is preselected — Homebase does not pick
  // a disk for somebody even when only one is plausible.
  const chooser = page.locator("section", { hasText: "Where File Browser keeps your files" });
  await expect(chooser).toBeVisible();
  await expect(chooser).toContainText(/No disk chosen yet/i);

  await chooser.getByRole("button", { name: DISK_NAME }).click();

  await expect(page.getByText(/will use that disk/i)).toBeVisible({ timeout: 60_000 });
  // Said on screen, not only in a job that scrolls away: existing files do not
  // move, and it takes effect at the next start.
  await expect(page.getByText(/stays where it is/i)).toBeVisible();

  await expect(page.getByText(/needs somewhere to keep your files/i)).toBeHidden({
    timeout: 30_000,
  });
});

test("stopping using a disk says nothing on it is deleted", async ({ page }) => {
  await signIn(page);
  await openStorage(page);

  const location = page.locator("li.storage-row", { hasText: DISK_NAME });
  await location.getByRole("button", { name: "Stop using it" }).click();

  // The promise that makes this different from erasing.
  await expect(page.getByText(/left exactly as it is/i)).toBeVisible();
  await expect(page.getByText(/nothing is deleted/i)).toBeVisible();

  const confirm = page.getByRole("button", { name: "Stop using it", exact: true }).last();
  await expect(confirm).toBeDisabled();

  await page.getByLabel(/Type/).fill(DISK_ID);
  await expect(confirm).toBeEnabled();

  // Not carried out: the VM test covers that, and the next spec file may want
  // the disk. What matters here is that the words are right and the button is
  // unreachable without typing.
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(location).toBeVisible();
});

test("the storage screen speaks about disks, not about mount points", async ({ page }) => {
  await signIn(page);
  await openStorage(page);

  const body = (await page.locator("body").innerText()).toLowerCase();

  // A person holding a USB drive is asking whether their films are on it.
  for (const jargon of ["mount point", "unmount", "filesystem", "block device", "/dev/sd"]) {
    expect(body, `the storage screen uses "${jargon}"`).not.toContain(jargon);
  }
});

// --- Helpers -----------------------------------------------------------------

/** The removable disk QEMU plugged in, as opposed to the system disk. */
function usbDisk(page: Page) {
  return page.locator("li.storage-row", { hasText: "USB" });
}

async function openStorage(page: Page) {
  await page.getByRole("button", { name: "Storage", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Your storage" })).toBeVisible();
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
  await expect(page.getByRole("heading", { name: SERVER_NAME })).toBeVisible();
}
