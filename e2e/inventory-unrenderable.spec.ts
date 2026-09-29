import { test, expect } from '@playwright/test';

// A node keeps its dgraph.type when its type is removed from schema.graphql, so
// queryConfigItem still matches it through the ConfigItem interface while every
// field on it — __typename included — resolves to nothing. DataTables treats a
// missing column as FATAL: it blocks the page with an alert() that names only
// its own documentation, and the whole inventory becomes unreachable.
//
// Driven through the CACHE rather than by planting a node, deliberately. The
// cache is what made this unrecoverable in practice: it survives reloads, so
// once poisoned the page stayed broken even after the offending node was gone.

const POISONED = [
  { uid: '0x1', type: 'Server', orbId: 'ns:server-A', name: 'a', createdBy: '', createdAt: '' },
  // What the query returns for a node whose type left the schema: the `??  ''`
  // fallbacks fill the strings, and type/orbId/uid stay undefined.
  { name: '', createdBy: '', createdAt: '' },
];

test('an item the schema can no longer describe hides the row, not the page', async ({ page }) => {
  const dialogs: string[] = [];
  page.on('dialog', async (d) => { dialogs.push(d.message()); await d.accept(); });

  await page.goto('/inventory');
  await page.evaluate((rows) => {
    sessionStorage.setItem('inventoryCache', JSON.stringify(rows));
  }, POISONED);
  await page.reload();

  // The table must render. Before the guard, this is where the alert fired and
  // nothing below it ran.
  await expect(page.locator('#inventory-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  expect(dialogs, `DataTables blocked the page: ${dialogs[0]}`).toHaveLength(0);

  // And the drop is STATED, naming the TYPE. The notice is rendered
  // server-side: for these rows every field resolves to nothing, __typename
  // included, so the client has a count and no names — a banner saying "1 item
  // is hidden" told nobody which, and said nothing useful at all if there were
  // fifty.
  const notice = page.locator('#inventory-unrenderable');
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('Hidden from this list');
  await expect(notice, 'the notice must NAME the type, not just count it').toContainText('Foo');

  // The cache must not stay poisoned: the next load would break again.
  const cached = await page.evaluate(() => JSON.parse(sessionStorage.getItem('inventoryCache') || '[]'));
  expect(cached.every((r: any) => typeof r.type === 'string' && r.type !== ''),
    'the refetch re-cached an unrenderable row').toBeTruthy();
});

test('the inventory notice agrees with the Schema page', async ({ page }) => {
  // The negative half, expressed as an invariant rather than a fixture.
  //
  // It used to clear the session cache and assert "no notice", which worked
  // while the banner was client-side. The notice is server-rendered from the
  // real graph now, so on any graph that HAS an orphan the old test asserted
  // something false. What must hold either way is that the two pages agree —
  // a warning that fires when nothing is wrong trains people to ignore it, and
  // one that stays silent when something is wrong is worse.
  await page.goto('/schema');
  const orphanBlock = page.getByTestId('schema-orphans');
  const types = await orphanBlock.count()
    ? await orphanBlock.locator('tbody tr td:first-child').allInnerTexts()
    : [];

  await page.goto('/inventory');
  await expect(page.locator('#inventory-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  const notice = page.locator('#inventory-unrenderable');

  if (types.length === 0) {
    await expect(notice, 'Schema reports no orphans, so inventory must not warn').toHaveCount(0);
    return;
  }
  await expect(notice, 'Schema reports orphans, so inventory must say so').toBeVisible();
  // Names the first few, and never silently truncates: past the cap it says
  // how many more.
  for (const t of types.slice(0, 3)) {
    await expect(notice).toContainText(t.trim());
  }
  if (types.length > 3) {
    await expect(notice).toContainText(`${types.length - 3} more`);
  }
});
