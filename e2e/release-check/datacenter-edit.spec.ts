import { test, expect, type Page } from '@playwright/test';
import { openEditor, editorState, setEditorState, saveEditor } from '../helpers/generic';

// The UI smoke half of release-check: prove a mutation written through the
// containerised orbital reaches DGraph and comes back on the next render.
//
// Retargeted at the generic page. It used to drive #datacenter-table,
// [data-dc-edit-id] and window.dcEditors — all of which died with the bespoke
// DataCenter page, leaving this spec referencing DOM that no longer exists. It
// was excluded from the main suite, so nothing caught it; `make release-check`
// would have.

const DC_NAME = 'colo-galleon';
const DC_ORB_ID = 'colo:colo-galleon';
const EDITED_NAME = 'colo-galleon-smoke';

async function rename(page: Page, from: string, to: string) {
  const domId = await openEditor(page, 'datacenters', DC_ORB_ID);
  const state = await editorState(page, domId);
  expect(state.name, `expected the editor to open on ${from}`).toBe(from);
  await setEditorState(page, domId, { ...state, name: to });

  await Promise.all([
    page.waitForResponse(r => r.url().includes('/graphql') && r.status() === 200),
    saveEditor(page, domId),
  ]);
  await expect(page.locator(`#edit-modal-generic-${domId}`)).not.toHaveClass(/is-active/, { timeout: 10_000 });
}

// Unconditional restore, not a second half of the test.
//
// Release-check specs share one orbital and run serially, so a failure BETWEEN
// the rename and the rename-back leaves every later spec looking at
// "colo-galleon-smoke" — and the failure they then report is not the one that
// happened. Learned the hard way: the first version of this rewrite asserted
// against the wrong element, and the graph stayed renamed.
test.afterEach(async ({ page }) => {
  await page.goto(`/datacenters/${encodeURIComponent(DC_ORB_ID)}`);
  const heading = page.getByTestId('page-heading');
  if ((await heading.innerText()).trim() === DC_NAME) return;
  await rename(page, EDITED_NAME, DC_NAME);
});

test('data center name round-trips through the editor and is persisted', async ({ page }) => {
  // Through the list page, because reaching a detail view from the list is the
  // path a person takes and the one worth smoking.
  // Exact match, not hasText: "colo-galleon" is a substring of
  // "colo-galleon-smoke", so a hasText precondition passes on exactly the
  // dirty state it is supposed to catch.
  await page.goto('/datacenters');
  await expect(page.getByRole('link', { name: DC_NAME, exact: true })).toBeVisible({ timeout: 10_000 });

  await rename(page, DC_NAME, EDITED_NAME);

  // Read back through a FRESH render, not the modal that was just closed — the
  // claim is that it persisted, and the open editor holds the new value either
  // way. The name is the page heading; the fields table carries the type's own
  // fields and never the ConfigItem name.
  await page.goto(`/datacenters/${encodeURIComponent(DC_ORB_ID)}`);
  await expect(page.getByTestId('page-heading')).toHaveText(EDITED_NAME);

  await rename(page, EDITED_NAME, DC_NAME);
  await page.goto(`/datacenters/${encodeURIComponent(DC_ORB_ID)}`);
  await expect(page.getByTestId('page-heading')).toHaveText(DC_NAME);
});
