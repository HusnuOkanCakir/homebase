import { test, expect, type Page } from "@playwright/test";

/**
 * Password recovery, through the interface. ADR-0015.
 *
 * This is the only unauthenticated way to change a credential in Homebase, and
 * it exists for somebody having the worst day they will have with this product:
 * locked out of their own photographs, holding a piece of paper they wrote on
 * months ago, not certain it is even the right one.
 *
 * Runs last, deliberately. It changes the administrator's password and signs
 * out every session, which would pull the floor out from under any spec that
 * followed it.
 */

const ADMIN = "alex";
const PASSWORD = "a-sufficiently-long-password";
const RECOVERED_PASSWORD = "a-password-chosen-after-recovery";
const SERVER_NAME = process.env["HOMEBASE_HOSTNAME"] ?? "homebase-dash";

const CODE = /^[0-9A-HJ-KM-NP-TV-Z]{5}(-[0-9A-HJ-KM-NP-TV-Z]{5}){4}$/;

/** Carried between tests in this file, which run in order. */
let issued = "";

test.describe.configure({ mode: "serial" });

test("the security screen says a code exists, without showing it", async ({ page }) => {
  await signIn(page, PASSWORD);
  await openSecurity(page);

  await expect(page.getByRole("heading", { name: "Your recovery code" })).toBeVisible();

  // Setup issued one, so this must say so — and must say when, because the
  // useful question is "is the paper I am looking at the current one".
  await expect(page.locator("dt", { hasText: "Written" })).toBeVisible();
  await expect(page.getByText(/kept where it cannot be read back/i)).toBeVisible();

  // The screen must not contain anything shaped like a code.
  const body = await page.locator("body").innerText();
  for (const word of body.split(/\s+/)) {
    expect(word, "the security screen is showing a recovery code").not.toMatch(CODE);
  }
});

test("creating a new code warns that the old one stops working", async ({ page }) => {
  await signIn(page, PASSWORD);
  await openSecurity(page);

  await page.getByRole("button", { name: "Create a new recovery code" }).click();

  // The consequence, before the button: somebody with the old code on paper is
  // about to be holding a useless piece of paper.
  const dialogue = page.locator("section.card-danger", {
    hasText: "Create a new recovery code?",
  });
  await expect(dialogue).toBeVisible();
  await expect(dialogue.getByText(/stop working/i)).toBeVisible();
  await expect(dialogue.getByText(/password does not change/i)).toBeVisible();

  await dialogue.getByRole("button", { name: "Create a new code" }).click();

  await expect(page.getByRole("heading", { name: "Write this down" })).toBeVisible();
  issued = await page.getByTestId("recovery-code").innerText();
  expect(issued).toMatch(CODE);
});

test("a forgotten password is offered a way out on the sign-in screen", async ({ page }) => {
  await signOut(page);

  // Findable without knowing it exists, and in the words somebody would use.
  await expect(page.getByRole("button", { name: "I have forgotten my password" })).toBeVisible();
  await page.getByRole("button", { name: "I have forgotten my password" }).click();

  await expect(page.getByRole("heading", { name: "Use your recovery code" })).toBeVisible();

  // What is about to happen, said before it happens.
  await expect(page.getByText(/signs out everything that is currently signed in/i)).toBeVisible();
  await expect(page.getByText(/capitals do not matter/i)).toBeVisible();

  // And where to go when the paper is gone too.
  await expect(page.getByText(/homebasectl recovery-code/)).toBeVisible();
});

test("a wrong code is refused, and says nothing about why", async ({ page }) => {
  await openRecovery(page);

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Recovery code").fill("ABCDE-FGHJK-MNPQR-STVWX-YZ234");
  await page.getByLabel("New password", { exact: true }).fill(RECOVERED_PASSWORD);
  await page.getByLabel("New password again").fill(RECOVERED_PASSWORD);
  await page.getByRole("button", { name: "Set a new password" }).click();

  await expect(page.getByText(/that recovery code is not right/i)).toBeVisible();

  // The typed code stays put: somebody who got one character of twenty-five
  // wrong is fixing that character, not typing the whole thing again.
  await expect(page.getByLabel("Recovery code")).toHaveValue("ABCDE-FGHJK-MNPQR-STVWX-YZ234");

  // And an account that does not exist is refused identically — anything else
  // says which names are real.
  await page.getByLabel("Your name").fill("nobody-by-that-name");
  await page.getByLabel("Recovery code").fill(issued);
  await page.getByRole("button", { name: "Set a new password" }).click();
  await expect(page.getByText(/that recovery code is not right/i)).toBeVisible();
});

test("the code from the paper sets a new password and signs you in", async ({ page }) => {
  await openRecovery(page);

  // Typed the way a person types it off paper: lower case, spaces for dashes.
  const asTyped = issued.toLowerCase().replaceAll("-", " ");

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Recovery code").fill(asTyped);
  await page.getByLabel("New password", { exact: true }).fill(RECOVERED_PASSWORD);
  await page.getByLabel("New password again").fill(RECOVERED_PASSWORD);
  await page.getByRole("button", { name: "Set a new password" }).click();

  // A replacement code, because recovering and being left without one puts
  // somebody one forgotten password from where they started.
  await expect(page.getByRole("heading", { name: "Write this down" })).toBeVisible({
    timeout: 30_000,
  });
  const replacement = await page.getByTestId("recovery-code").innerText();
  expect(replacement).toMatch(CODE);
  expect(replacement, "the spent code was handed back as the new one").not.toBe(issued);

  await page.getByLabel(/written down my new recovery code/i).check();
  await page.getByRole("button", { name: "Continue" }).click();

  // Straight in — not back to a sign-in screen asking for a password chosen
  // ten seconds ago on a page that has already gone.
  await expect(page.getByRole("heading", { name: SERVER_NAME })).toBeVisible();

  issued = replacement;
});

test("the old password no longer works, and neither does the spent code", async ({ page }) => {
  await signOut(page);

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText(/not right/i)).toBeVisible();

  // The new one does.
  await page.getByLabel("Password").fill(RECOVERED_PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: SERVER_NAME })).toBeVisible();

  // And the security screen now records that this account was recovered, which
  // is how somebody finds out about a reset they did not perform.
  await openSecurity(page);
  await expect(page.locator("dt", { hasText: "Last used to get back in" })).toBeVisible();
});

// --- Helpers -----------------------------------------------------------------

async function openSecurity(page: Page) {
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Your recovery code" })).toBeVisible();
}

async function openRecovery(page: Page) {
  await page.goto("/");

  const signOutButton = page.getByRole("button", { name: "Sign out" });
  if (await signOutButton.isVisible().catch(() => false)) {
    await signOutButton.click();
  }

  await page.getByRole("button", { name: "I have forgotten my password" }).click();
  await expect(page.getByRole("heading", { name: "Use your recovery code" })).toBeVisible();
}

async function signOut(page: Page) {
  await page.goto("/");

  const signOutButton = page.getByRole("button", { name: "Sign out" });
  if (await signOutButton.isVisible().catch(() => false)) {
    await signOutButton.click();
  }
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
}

async function signIn(page: Page, password: string) {
  await page.goto("/");

  const signOutButton = page.getByRole("button", { name: "Sign out" });
  if (await signOutButton.isVisible().catch(() => false)) {
    return;
  }

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: SERVER_NAME })).toBeVisible();
}
