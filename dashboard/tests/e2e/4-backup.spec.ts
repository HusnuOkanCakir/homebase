import { test, expect, type Page } from "@playwright/test";

/**
 * Milestone 5, through the interface.
 *
 * The VM test proves the machine half — that a backup restores onto a different
 * machine and the files come back byte for byte. This is the half about the
 * person, and for this milestone that half is unusually important: somebody
 * restoring a backup is anxious, in a hurry, and about to do something
 * irreversible to the data they are trying to save.
 *
 * So most of what is asserted here is about what they are *told* before the
 * button becomes available.
 *
 * Runs after 3-storage.spec.ts, which gives the first spare disk to File
 * Browser. This spec sets up the second one, because a backup may not live on
 * the disk whose files it is backing up.
 */

const ADMIN = "alex";
const PASSWORD = "a-sufficiently-long-password";
const SERVER_NAME = process.env["HOMEBASE_HOSTNAME"] ?? "homebase-dash";

/** The disk 3-storage.spec.ts set up and gave to File Browser. */
const DATA_DISK = "Films drive";
/** The one this spec sets up, to keep backups on. */
const BACKUP_DISK = "Backup drive";

test.describe.configure({ mode: "serial" });

test("backing up needs somewhere to put it, and says so", async ({ page }) => {
  await signIn(page);
  await openBackup(page);

  // The two choices are offered with what they cost, because "Everything" on a
  // large collection is hours and nobody should discover that afterwards.
  await expect(page.getByRole("button", { name: "Settings only" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Everything" })).toBeVisible();
  await expect(page.getByText(/takes a few seconds/i)).toBeVisible();
  await expect(page.getByText(/can take hours/i)).toBeVisible();

  // And where to keep the disk, which is the part people get wrong.
  await expect(page.getByText(/somewhere other than next to the server/i)).toBeVisible();
});

test("the only disk available is the one the files are on, and it is refused", async ({ page }) => {
  await signIn(page);
  await openBackup(page);

  // At this point the machine has exactly one location: the disk File Browser
  // keeps its files on. Backing up onto it would put both copies on the same
  // hardware, so Homebase refuses — and this is the moment to check it says so
  // in terms of the application and the disk, not of paths.
  await page.getByRole("button", { name: "Everything" }).click();

  const refusal = page.getByRole("alert");
  await expect(refusal).toBeVisible({ timeout: 60_000 });
  await expect(refusal).toContainText(new RegExp(`${DATA_DISK} is where File Browser`, "i"));
  await expect(refusal, "the reason a second copy on one disk is not a backup").toContainText(
    /disks fail as a whole/i,
  );

  // And nothing was made.
  await expect(page.getByText("None yet.")).toBeVisible();
});

test("setting up a second disk to keep backups on", async ({ page }) => {
  test.setTimeout(360_000);

  await signIn(page);
  await page.getByRole("button", { name: "Storage", exact: true }).click();

  // The disk still offering to be erased is the spare one; the other is in use.
  const spare = page
    .locator("li.storage-row", { hasText: "USB" })
    .filter({ has: page.getByRole("button", { name: "Erase and prepare" }) })
    .first();

  await spare.getByRole("button", { name: "Erase and prepare" }).click();

  const identifier = await page.locator("label[for='erase-confirm'] code").innerText();
  await page.getByLabel("What would you like to call it?").fill("Backups");
  await page.getByLabel(/Type/).fill(identifier);
  await page.getByRole("button", { name: "Erase it permanently" }).click();

  await expect(page.getByText(/The disk is ready to use/i)).toBeVisible({ timeout: 300_000 });

  const prepared = page
    .locator("li.storage-row", { hasText: "USB" })
    .filter({ has: page.getByRole("button", { name: "Use this" }) })
    .first();
  await expect(prepared).toBeVisible({ timeout: 30_000 });
  await prepared.getByRole("button", { name: "Use this" }).click();

  await page.getByLabel("What would you like to call it?").fill(BACKUP_DISK);
  await page.getByRole("button", { name: "Use this disk" }).click();

  await expect(page.getByText(new RegExp(`${BACKUP_DISK} is ready`, "i"))).toBeVisible({
    timeout: 120_000,
  });
});

test("making a backup", async ({ page }) => {
  test.setTimeout(360_000);

  await signIn(page);
  await openBackup(page, BACKUP_DISK);

  await page.getByRole("button", { name: "Everything" }).click();

  await expect(page.getByRole("status")).toBeVisible();
  await expect(page.getByText(/backup is finished|have been backed up/i)).toBeVisible({
    timeout: 300_000,
  });

  // It appears in the list, with what it is and where it came from.
  const backup = page.locator("li.storage-row").first();
  await expect(backup).toBeVisible({ timeout: 30_000 });
  await expect(backup).toContainText(/Settings and files/i);
  await expect(backup).toContainText(new RegExp(`from ${SERVER_NAME}`, "i"));
});

test("a backup can be checked, and says what it does not contain", async ({ page }) => {
  await signIn(page);
  await openBackup(page, BACKUP_DISK);

  const backup = page.locator("li.storage-row").first();

  // What is deliberately absent is on the screen, not only in the manifest —
  // somebody must not believe they have something they do not.
  await backup.getByText("What this backup does and does not contain").click();
  await expect(backup.getByText(/passwords are not included/i)).toBeVisible();

  await backup.getByRole("button", { name: "Check this backup" }).click();
  await expect(page.getByText(/present and unchanged/i)).toBeVisible({ timeout: 300_000 });
});

test("restoring shows what it would do before offering the button", async ({ page }) => {
  await signIn(page);
  await openBackup(page, BACKUP_DISK);

  const backup = page.locator("li.storage-row").first();
  await backup.getByRole("button", { name: "See what restoring would do" }).click();

  const dialogue = page.locator("section.card-danger", { hasText: "Restore the backup" });
  await expect(dialogue).toBeVisible({ timeout: 300_000 });

  // The facts, from the server having looked at this backup and this machine.
  await expect(fact(page, "Taken from")).toContainText(SERVER_NAME);
  await expect(fact(page, "Files to restore")).toContainText(/\d/);
  // The number that actually matters before agreeing.
  await expect(fact(page, "Would be replaced")).toBeVisible();

  // The reassurance that makes it safe to say yes.
  await expect(dialogue.getByText(/Nothing else is deleted/i)).toBeVisible();
  await expect(dialogue.getByText(/not reinstalled automatically/i)).toBeVisible();

  // And the button is unreachable without typing the backup's own id.
  const confirm = dialogue.getByRole("button", { name: "Restore this backup" });
  await expect(confirm).toBeDisabled();

  await page.getByLabel(/Type/).fill("yes");
  await expect(confirm, "a word anybody would type is not a confirmation").toBeDisabled();

  await page.getByLabel(/Type/).fill("restore");
  await expect(confirm).toBeDisabled();

  // Backing out must actually back out, with nothing restored.
  await dialogue.getByRole("button", { name: "Cancel" }).click();
  await expect(dialogue).toBeHidden();
});

test("deleting a backup is separate from restoring, and asks", async ({ page }) => {
  await signIn(page);
  await openBackup(page, BACKUP_DISK);

  const backup = page.locator("li.storage-row").first();
  await backup.getByRole("button", { name: "Delete" }).click();

  const dialogue = page.locator("section.card-danger", { hasText: "Delete the backup" });
  await expect(dialogue).toBeVisible();
  // It has to be clear this affects the disk and not the server.
  await expect(dialogue.getByText(/nothing on this server changes/i)).toBeVisible();
  await expect(dialogue.getByText(/other backups are not affected/i)).toBeVisible();

  const confirm = dialogue.getByRole("button", { name: "Delete it" });
  await expect(confirm).toBeDisabled();

  await dialogue.getByRole("button", { name: "Cancel" }).click();
  await expect(dialogue).toBeHidden();

  // Still there.
  await expect(page.locator("li.storage-row").first()).toBeVisible();
});

test("the backup screen speaks about disks and files, not about archives", async ({ page }) => {
  await signIn(page);
  await openBackup(page, BACKUP_DISK);

  const body = (await page.locator("body").innerText()).toLowerCase();

  for (const jargon of ["tarball", "archive", "sqlite", "checksum", "manifest", "rsync"]) {
    expect(body, `the backup screen uses "${jargon}"`).not.toContain(jargon);
  }
});

// --- Helpers -----------------------------------------------------------------

function fact(page: Page, label: string) {
  return page.locator(`dt:text-is("${label}") + dd`);
}

async function openBackup(page: Page, disk?: string) {
  await page.getByRole("button", { name: "Storage", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Make a backup" })).toBeVisible();

  // With more than one disk set up, Homebase asks which one and picks none by
  // default — so every test that wants a particular disk has to say so, exactly
  // as a person would.
  if (disk) {
    await page.getByRole("button", { name: disk }).click();
  }
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
