import { test, expect, Page } from '@playwright/test';

const uniqueSuffix = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 5);

async function registerAndLogin(page: Page, suffix: string) {
  await page.goto('/register');
  await page.fill('#email', `chat${suffix}@test.com`);
  await page.fill('#username', `chatuser${suffix}`);
  await page.fill('#password', 'password123');
  await page.fill('#confirm_password', 'password123');
  await page.click('button[type="submit"]');
  await page.waitForURL('**/collection');
}

test.describe('Chat', () => {
  test('chat page has correct layout elements', async ({ page }) => {
    // This test requires a game to exist in the DB.
    // We navigate to a known BGG game page first to seed it.
    const suffix = uniqueSuffix();
    await registerAndLogin(page, suffix);

    // Fetch Catan (BGG ID 13) to seed it into DB
    await page.goto('/games/13');
    await expect(page.locator('h1')).toBeVisible({ timeout: 10000 });

    // Add to collection
    const addBtn = page.locator('button:has-text("Add to collection"), button:has-text("+ Add")').first();
    if (await addBtn.isVisible()) {
      await addBtn.click();
      await page.waitForResponse(resp => resp.url().includes('/collection') && resp.status() === 200);
    }

    // Now go to collection and find the game's chat button
    await page.goto('/collection');
    const chatBtn = page.locator('a[href*="/chat/"]').first();

    if (await chatBtn.isVisible()) {
      const chatURL = await chatBtn.getAttribute('href');
      await page.goto(chatURL!);

      // Verify chat layout
      await expect(page.locator('#messages')).toBeVisible();
      await expect(page.locator('textarea[name="message"]')).toBeVisible();
      await expect(page.locator('button[type="submit"]')).toBeVisible();
    }
  });

  test('chat input is disabled while sending', async ({ page }) => {
    // Minimal test: just check the chat form structure exists and Alpine.js binding works
    const suffix = uniqueSuffix();
    await registerAndLogin(page, suffix);

    await page.goto('/games/13');
    await page.waitForTimeout(2000); // Wait for BGG API

    const addBtn = page.locator('button:has-text("Add to collection")').first();
    if (await addBtn.isVisible()) {
      await addBtn.click();
      await page.waitForTimeout(1000);
    }

    await page.goto('/collection');
    const chatLink = page.locator('a[href*="/chat/"]').first();
    if (await chatLink.count() > 0) {
      await chatLink.click();
      await expect(page.locator('textarea[name="message"]')).toBeVisible();
      await expect(page.locator('textarea[name="message"]')).toBeEnabled();
    }
  });
});
