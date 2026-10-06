import { test, expect } from '@playwright/test';

const uniqueSuffix = () => Date.now().toString(36);

test.describe('Authentication', () => {
  test('home page loads and shows CTA', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1')).toContainText('Tabletop');
    await expect(page.getByRole('link', { name: /get started/i })).toBeVisible();
    await expect(page.getByRole('link', { name: /log in/i })).toBeVisible();
  });

  test('register new user and redirect to collection', async ({ page }) => {
    const suffix = uniqueSuffix();
    await page.goto('/register');
    await expect(page.locator('h1')).toContainText('Create account');

    await page.fill('#email', `user${suffix}@test.com`);
    await page.fill('#username', `user${suffix}`);
    await page.fill('#password', 'password123');
    await page.fill('#confirm_password', 'password123');
    await page.click('button[type="submit"]');

    await page.waitForURL('**/collection');
    await expect(page).toHaveURL(/\/collection/);
  });

  test('login with valid credentials', async ({ page }) => {
    // Register first
    const suffix = uniqueSuffix();
    await page.goto('/register');
    await page.fill('#email', `login${suffix}@test.com`);
    await page.fill('#username', `loginuser${suffix}`);
    await page.fill('#password', 'password123');
    await page.fill('#confirm_password', 'password123');
    await page.click('button[type="submit"]');
    await page.waitForURL('**/collection');

    // Logout
    await page.click('button:has-text("Log out"), form[action="/logout"] button');
    await page.waitForURL('/');

    // Login again
    await page.goto('/login');
    await page.fill('#email', `login${suffix}@test.com`);
    await page.fill('#password', 'password123');
    await page.click('button[type="submit"]');
    await page.waitForURL('**/collection');
    await expect(page).toHaveURL(/\/collection/);
  });

  test('login with wrong password shows error', async ({ page }) => {
    const suffix = uniqueSuffix();
    await page.goto('/register');
    await page.fill('#email', `wrong${suffix}@test.com`);
    await page.fill('#username', `wronguser${suffix}`);
    await page.fill('#password', 'password123');
    await page.fill('#confirm_password', 'password123');
    await page.click('button[type="submit"]');
    await page.waitForURL('**/collection');
    await page.click('button:has-text("Log out"), form[action="/logout"] button');
    await page.waitForURL('/');

    await page.goto('/login');
    await page.fill('#email', `wrong${suffix}@test.com`);
    await page.fill('#password', 'wrongpassword');
    await page.click('button[type="submit"]');

    // Should stay on login page with error
    await expect(page).toHaveURL(/\/login/);
    await expect(page.locator('body')).toContainText(/invalid|password|error/i);
  });

  test('protected routes redirect to login when unauthenticated', async ({ page }) => {
    await page.goto('/collection');
    await expect(page).toHaveURL(/\/login/);

    await page.goto('/games/search');
    await expect(page).toHaveURL(/\/login/);
  });

  test('register with mismatched passwords shows client-side error', async ({ page }) => {
    await page.goto('/register');
    await page.fill('#email', 'test@test.com');
    await page.fill('#username', 'testuser');
    await page.fill('#password', 'password123');
    await page.fill('#confirm_password', 'different456');

    // Alpine.js should show error and disable button
    await expect(page.locator('p:has-text("don\'t match")')).toBeVisible();
    const submitBtn = page.locator('button[type="submit"]');
    await expect(submitBtn).toBeDisabled();
  });
});
