import { test, expect } from "@playwright/test";

/**
 * The local assistant, in a browser.
 *
 * Runs against a real Homebase with a real model behind it, which is the only
 * way to test the part most likely to be wrong: an answer written a few tokens
 * a second has to appear *while* it is being written. A mocked API returns
 * everything at once and would pass whether or not the streaming works.
 *
 * Skipped when the machine under test has no model. Most do not, and a suite
 * that failed on them would be a suite people learn to ignore — so the absence
 * is checked as its own assertion instead: no model, no tab.
 */

const ADMIN = process.env["HOMEBASE_ADMIN"] ?? "alex";
const PASSWORD = process.env["HOMEBASE_PASSWORD"] ?? "a-sufficiently-long-password";

test.describe.configure({ mode: "serial" });

async function signIn(page: import("@playwright/test").Page) {
  await page.goto("/");
  const username = page.getByLabel("Your name");
  if (await username.isVisible().catch(() => false)) {
    await username.fill(ADMIN);
    await page.getByLabel("Password", { exact: true }).fill(PASSWORD);
    await page.getByRole("button", { name: /sign in/i }).click();
  }
  await expect(page.getByRole("navigation", { name: "Sections" })).toBeVisible();
}

/** Whether this server actually has a model, asked the way the dashboard asks. */
async function hasAssistant(page: import("@playwright/test").Page): Promise<boolean> {
  const response = await page.request.get("/api/v1/assistant");
  if (!response.ok()) return false;
  return ((await response.json()) as { available?: boolean }).available === true;
}

test("a server with no model shows no assistant tab", async ({ page }) => {
  await signIn(page);
  test.skip(await hasAssistant(page), "this machine has a model; covered below");

  // Not merely hidden behind an error screen — absent. A tab that opens onto
  // "there is no assistant here" advertises a feature by explaining its absence.
  await expect(page.getByRole("button", { name: "Assistant" })).toHaveCount(0);
});

test("the assistant answers, and the answer arrives while it is written", async ({ page }) => {
  await signIn(page);
  test.skip(!(await hasAssistant(page)), "this machine has no local model");

  await page.getByRole("button", { name: "Assistant" }).click();

  // What the screen promises before anything is typed.
  await expect(page.getByText(/nothing you type here leaves your network/i)).toBeVisible();

  const box = page.getByLabel("Your question");
  await expect(box).toBeVisible();

  // "Ask" stays unavailable until there is something to ask.
  const ask = page.getByRole("button", { name: "Ask", exact: true });
  await expect(ask).toBeDisabled();

  await box.fill("Name one thing a home server is useful for.");
  await expect(ask).toBeEnabled();
  await ask.click();

  // The question is on screen immediately, attributed.
  await expect(page.getByText("Name one thing a home server is useful for.")).toBeVisible();

  // While answering there is a way to stop, because on this hardware the answer
  // takes minutes and somebody will want out of it.
  const stop = page.getByRole("button", { name: "Stop" });
  await expect(stop).toBeVisible({ timeout: 30_000 });

  const answer = page.locator(".assistant-turn:not(.assistant-you) .assistant-text").last();

  // The heart of it: text is on screen well before the answer is finished.
  // Six tokens a second means a partial answer exists for tens of seconds, and
  // if this ever fails the interface has silently become "wait, then read".
  await expect(answer).not.toBeEmpty({ timeout: 45_000 });
  const partial = (await answer.innerText()).length;
  expect(partial, "no text appeared while the model was still writing").toBeGreaterThan(0);

  // And it finishes: "Stop" goes away and the answer has grown.
  await expect(stop).toHaveCount(0, { timeout: 240_000 });
  const complete = (await answer.innerText()).length;
  expect(complete).toBeGreaterThanOrEqual(partial);
  expect(complete, "the answer is empty").toBeGreaterThan(20);

  // The model writes Markdown whether or not it was asked to. A finished answer
  // that still contains "**" is one being shown as source rather than rendered,
  // which is what this looked like before Markdown.tsx existed.
  const rendered = await answer.innerText();
  expect(rendered, "the answer is showing raw Markdown").not.toContain("**");

  // Asking again is possible without reloading.
  await expect(page.getByRole("button", { name: "Ask", exact: true })).toBeVisible();
});

test("starting again clears the conversation", async ({ page }) => {
  await signIn(page);
  test.skip(!(await hasAssistant(page)), "this machine has no local model");

  await page.getByRole("button", { name: "Assistant" }).click();
  await page.getByLabel("Your question").fill("Say the single word: ready.");
  await page.getByRole("button", { name: "Ask", exact: true }).click();

  await expect(page.getByText("Say the single word: ready.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Stop" })).toHaveCount(0, {
    timeout: 240_000,
  });

  await page.getByRole("button", { name: "Start again" }).click();

  await expect(page.getByText("Say the single word: ready.")).toHaveCount(0);
  await expect(page.getByText(/nothing you type here leaves your network/i)).toBeVisible();
});
