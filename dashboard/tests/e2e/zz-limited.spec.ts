import { test } from "@playwright/test";
test("what a limited account sees", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto("/");
  await page.getByLabel("Your name").fill("brother");
  await page.getByLabel("Password", { exact: true }).fill("another-long-password");
  await page.getByRole("button", { name: /sign in/i }).click();
  await page.waitForTimeout(4000);
  await page.screenshot({ path: process.env["SHOTDIR"] + "/limited-home.png", fullPage: true });
  console.log("TABS " + JSON.stringify(
    await page.locator("nav.tabs button").allInnerTexts()));
  console.log("ERRORS " + JSON.stringify(errors.slice(0, 3)));
});
