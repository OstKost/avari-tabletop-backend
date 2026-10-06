import { test, expect, Page } from '@playwright/test';

const uniqueSuffix = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 5);

async function registerAndLogin(page: Page, suffix: string) {
  await page.goto('/register');
  await page.fill('#email', `coll${suffix}@test.com`);
  await page.fill('#username', `colluser${suffix}`);
  await page.fill('#password', 'password123');
  await page.fill('#confirm_password', 'password123');
  await page.click('button[type="submit"]');
  await page.waitForURL('**/collection');
}

test.describe('Collection', () => {
  test('empty collection shows search CTA', async ({ page }) => {
    const suffix = uniqueSuffix();
    await registerAndLogin(page, suffix);

    await expect(page.locator('#game-list')).toContainText(/No games|search/i);
    await expect(page.getByRole('link', { name: /add game/i })).toBeVisible();
  });

  test('search for a game shows results', async ({ page }) => {
    const suffix = uniqueSuffix();
    await registerAndLogin(page, suffix);

    await page.goto('/games/search');
    await expect(page.locator('input[name="q"]')).toBeVisible();

    // Type a search query - results come from BGG API (may be slow in CI)
    const searchResponse = page.waitForResponse(resp => resp.url().includes('/games/search') && resp.status() === 200, { timeout: 15000 });
    await page.fill('input[name="q"]', 'Catan');
    await searchResponse;
    await expect(page.locator('#results')).not.toBeEmpty();
  });

  test('filter tabs change displayed games', async ({ page }) => {
    const suffix = uniqueSuffix();
    await registerAndLogin(page, suffix);

    await page.goto('/collection');

    const statusNavigation = page.getByRole('navigation', { name: 'Collection status' });
    const wishlist = statusNavigation.getByRole('link', { name: /Wishlist/ });
    const wishlistResponse = page.waitForResponse(resp => {
      const url = new URL(resp.url());
      return url.pathname === '/collection' && url.searchParams.get('status') === 'wishlist' && resp.status() === 200;
    });
    await wishlist.click();
    await wishlistResponse;
    await expect(wishlist).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#game-list')).toContainText('No games match these filters');

    const all = statusNavigation.getByRole('link', { name: /All/ });
    const allResponse = page.waitForResponse(resp => {
      const url = new URL(resp.url());
      return url.pathname === '/collection' && (url.searchParams.get('status') ?? '') === '' && resp.status() === 200;
    });
    await all.click();
    await allResponse;
    await expect(all).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#game-list')).toContainText('No games here yet');
  });

  test('collection page loads with correct nav', async ({ page }) => {
    const suffix = uniqueSuffix();
    await registerAndLogin(page, suffix);

    await page.goto('/collection');
    await expect(page.locator('h1')).toContainText('My Collection');
    await expect(page.getByRole('navigation').filter({ has: page.locator('a[href="/"]') })).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Collection status' })).toBeVisible();
  });
});
