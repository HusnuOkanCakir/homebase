import { test, expect, type Page } from "@playwright/test";

/**
 * The Milestone 2 exit condition, as a browser test.
 *
 * First in the numbered sequence: it creates the administrator that every later
 * spec signs in as.
 *
 * "A user opens the dashboard, creates an administrator, sees accurate system
 * information, reboots the machine, and everything comes back by itself."
 *
 * Every step here is that sentence. It runs against a real Homebase in a VM, so
 * the reboot is a real reboot — which is the only way to find out whether the
 * dashboard handles the server going away underneath it.
 */

const ADMIN = "alex";
const PASSWORD = "a-sufficiently-long-password";

/** The VM's hostname, which is also what the reboot confirmation requires. */
const SERVER_NAME = process.env["HOMEBASE_HOSTNAME"] ?? "homebase-dash";

test.describe.configure({ mode: "serial" });

test("first-run setup claims the server", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Set up your server" })).toBeVisible();

  // Nothing about this screen should assume the reader knows what a daemon is.
  const body = await page.locator("body").innerText();
  for (const jargon of ["daemon", "systemd", "sudo", "root", "localhost", "socket"]) {
    expect(
      body.toLowerCase(),
      `the setup screen uses the word "${jargon}", which the reader has no reason to know`,
    ).not.toContain(jargon);
  }

  // The button stays disabled until the form is actually usable.
  const submit = page.getByRole("button", { name: /set up this server/i });
  await expect(submit).toBeDisabled();

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Password", { exact: true }).fill("short");
  await page.getByLabel("Password again").fill("short");
  await expect(submit, "a too-short password should not be submittable").toBeDisabled();

  await page.getByLabel("Password", { exact: true }).fill(PASSWORD);
  await page.getByLabel("Password again").fill("different-password-entirely");
  await expect(page.getByText("These two do not match.")).toBeVisible();
  await expect(submit).toBeDisabled();

  await page.getByLabel("Password again").fill(PASSWORD);
  await expect(submit).toBeEnabled();
  await submit.click();

  // Before the dashboard: the recovery code, which is the only moment it is
  // ever visible. ADR-0015 — it is stored the way a password is, so a user who
  // clicks past this screen has lost it, and finds out at the worst moment.
  await expect(page.getByRole("heading", { name: "Write this down" })).toBeVisible();

  const code = await page.getByTestId("recovery-code").innerText();
  expect(code, "the code should be five groups of five characters").toMatch(
    /^[0-9A-HJ-KM-NP-TV-Z]{5}(-[0-9A-HJ-KM-NP-TV-Z]{5}){4}$/,
  );

  // And it cannot be walked past. Not a "next" button: an explicit claim to
  // have done the one thing that makes the code worth anything.
  const carryOn = page.getByRole("button", { name: "Continue" });
  await expect(carryOn).toBeDisabled();
  await page.getByLabel(/written down my recovery code/i).check();
  await expect(carryOn).toBeEnabled();
  await carryOn.click();

  // Then it signs you straight in.
  await expect(page.getByRole("heading", { name: SERVER_NAME })).toBeVisible();
});

test("the overview shows real information about this machine", async ({ page }) => {
  await signIn(page);

  await expect(page.getByRole("heading", { name: "This server" })).toBeVisible();

  // These values came from /proc, through hostd's typed operation, over the
  // Unix socket, through core, to this browser. Asserting they are plausible is
  // what makes that path tested rather than assumed.
  //
  // Scoped to the value beside each label rather than searching the page: a
  // loose match finds the same words in unrelated prose and passes for the
  // wrong reason.
  await expect(fact(page, "Running for")).toHaveText(
    /^(less than a minute|\d+ (minute|hour|day)s?)/,
  );
  await expect(fact(page, "Operating system")).toContainText("Ubuntu");
  await expect(fact(page, "Memory")).toContainText(
    /\d+(\.\d+)? [kMG]B of \d+(\.\d+)? [kMG]B in use/,
  );
  await expect(fact(page, "Busyness")).toContainText(/\d+\.\d\d/);

  // Uptime is rendered in words, not seconds. "259384 seconds" is the same fact
  // expressed so the reader has to do arithmetic to use it.
  await expect(page.locator("body")).not.toContainText(/\d{4,} seconds/);

  // The values a technical person wants are present but out of the way.
  await expect(page.getByText("Kernel")).toBeHidden();
  await page.getByText("Technical details").click();
  await expect(fact(page, "Kernel")).toContainText(/\d+\.\d+/);
  await expect(fact(page, "Hardware")).toContainText("Virtual machine");
});

test("restarting requires naming the machine", async ({ page }) => {
  await signIn(page);

  await page.getByRole("button", { name: "Restart this server" }).click();
  await expect(page.getByRole("heading", { name: `Restart ${SERVER_NAME}?` })).toBeVisible();

  const confirm = page.getByRole("button", { name: "Restart now" });
  await expect(confirm, "restart is not available until the name is typed").toBeDisabled();

  await page.getByLabel(/Server name/).fill("some-other-server");
  await expect(confirm, "a different name must not unlock the button").toBeDisabled();

  await page.getByLabel(/Server name/).fill(SERVER_NAME);
  await expect(confirm).toBeEnabled();

  // Backing out must actually back out.
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("heading", { name: "This server" })).toBeVisible();
});

test("restarting the server, and the dashboard noticing it came back", async ({ page }) => {
  await signIn(page);

  await page.getByRole("button", { name: "Restart this server" }).click();
  await page.getByLabel(/Server name/).fill(SERVER_NAME);
  await page.getByRole("button", { name: "Restart now" }).click();

  // The server goes away underneath the page. This is the screen that has to
  // handle it, and "the connection failed" would be the wrong thing to show.
  await expect(page.getByRole("heading", { name: "Restarting…" })).toBeVisible();
  await expect(page.getByText(/this page will notice when it is back/i)).toBeVisible();

  // Waiting for a real machine to reboot.
  await expect(page.getByRole("heading", { name: "Your server is back" })).toBeVisible({
    timeout: 100_000,
  });
  // This message is the proof that the machine really restarted, and it is
  // worth knowing why. core cannot watch a reboot finish — the connection dies
  // with the machine — so it records the kernel's boot_id when the job starts
  // and compares it on the next boot. This text only appears when those two
  // differ, which nothing but a genuine restart produces.
  //
  // Comparing displayed uptime before and after was tried instead and is
  // useless: on a VM that has been up for thirty seconds, both readings say
  // "less than a minute".
  await expect(page.getByText(/restarted successfully/i)).toBeVisible();

  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "This server" })).toBeVisible();

  // And the server is genuinely usable again, not merely answering.
  await expect(fact(page, "Operating system")).toContainText("Ubuntu");
});

test("signing out ends the session", async ({ page }) => {
  await signIn(page);

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();

  // Reloading must not restore it.
  await page.reload();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});

test("a wrong password does not reveal whether the name exists", async ({ page }) => {
  await page.goto("/");
  await ensureSignedOut(page);

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Password").fill("not-the-right-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  const wrongPassword = await page.getByRole("alert").innerText();

  await page.getByLabel("Your name").fill("nobody-by-that-name");
  await page.getByLabel("Password").fill("not-the-right-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  const noSuchUser = await page.getByRole("alert").innerText();

  expect(
    noSuchUser,
    "the two messages differ, which tells an attacker which names are real",
  ).toBe(wrongPassword);
});

// --- Helpers -----------------------------------------------------------------

/** The value shown beside a label in the facts list. */
function fact(page: Page, label: string) {
  return page.locator(`dt:text-is("${label}") + dd`);
}

async function signIn(page: Page) {
  await page.goto("/");
  await ensureSignedOut(page);

  await page.getByLabel("Your name").fill(ADMIN);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: SERVER_NAME })).toBeVisible();
}

/** Each test starts from a known place, whatever the last one left behind. */
async function ensureSignedOut(page: Page) {
  const signOut = page.getByRole("button", { name: "Sign out" });
  if (await signOut.isVisible().catch(() => false)) {
    await signOut.click();
  }
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
}
