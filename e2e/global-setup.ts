import { chromium } from '@playwright/test';

export default async function globalSetup() {
  const browser = await chromium.launch();
  const page = await browser.newPage();

  await page.goto('http://localhost:8001/');

  // Open login modal (use first match — navbar and page both have the trigger)
  await page.locator('a.js-modal-trigger[data-target="login-modal"]').first().click();
  await page.locator('#login-modal').waitFor({ state: 'visible' });

  await page.locator('#form-login input[name="email"]').fill('admin@armada.ai');
  await page.locator('#form-login input[name="password"]').fill('admin');
  await page.locator('#form-login button[type="submit"]').click();

  // HTMX handles HX-Redirect — wait for the page to settle after redirect
  await page.waitForURL('**/\?fresh=1');
  await page.waitForSelector('#datacenter-table');

  await page.context().storageState({ path: 'e2e/.auth.json' });
  await browser.close();
}
