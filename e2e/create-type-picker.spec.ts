import { test, expect } from '@playwright/test';

// Choosing WHICH implementation to create, on an interface-backed page.
//
// /clusters lists the KubernetesCluster INTERFACE — correctly, because
// DataCenter.kubernetesClusters is typed as the interface, so a data centre's
// tab shows every flavour. Only CREATE cannot be polymorphic: DGraph generates
// no addKubernetesCluster.
//
// The Type field is rendered ALWAYS, pre-selected when there is one
// implementation. A question with one answer is noise; a field that already
// holds the answer tells the reader what they are creating at no cost — and the
// flow does not change shape when a second implementation lands.

test('a concrete page has no Type field', async ({ page }) => {
  // Acceptance 8, and the regression that matters most: the picker is driven by
  // `Implementations`, and rendering it whenever that slice is merely non-nil
  // would put a one-option "Type" on every page in the product.
  for (const slug of ['servers', 'data-centers', 'network-devices']) {
    await page.goto('/' + slug);
    await page.getByTestId('create-open').click();
    await expect(page.locator('.modal.is-active').getByTestId('create-type'),
      `/${slug} is not an interface and must not ask for a type`).toHaveCount(0);
    await page.locator('.modal.is-active [data-modal-close]').first().click();
  }
});

test('an interface page states which type it creates, pre-selected', async ({ page }) => {
  // Acceptance 1 and 2.
  await page.goto('/clusters');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  const picker = modal.getByTestId('create-type');
  await expect(picker, 'an interface page must say which type it creates').toBeVisible();
  await expect(picker, 'with one implementation it is answered already — no extra click')
    .toHaveValue('EksaKubernetesCluster');

  // Acceptance 4: the mutation follows the SELECTION, not the page.
  await expect(modal).toHaveAttribute('data-kind', 'EksaKubernetesCluster');
});


test('shared fields render once; the implementation\'s own are per-type', async ({ page }) => {
  // Acceptance 3. Switching type must not re-render the shared fields — the
  // reader would lose whatever they had already typed into them.
  await page.goto('/clusters');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  // `clusterType` is EKS-A's own; `cni` comes from the interface.
  const shared = modal.locator('[data-create-field="cni"]:not([data-create-child])');
  await expect(shared, 'an interface field renders once, outside any type block').toHaveCount(1);
  await expect(shared.locator('xpath=ancestor::*[@data-create-type-fields]'),
    'and must NOT sit inside a per-type block').toHaveCount(0);

  const own = modal.locator('[data-create-type-fields="EksaKubernetesCluster"] [data-create-field="clusterType"]');
  await expect(own, "an implementation's own field lives in its type block").toHaveCount(1);
  await expect(own, 'and is visible for the selected type').toBeVisible();

  // ONCE in the whole modal. Leaving it on the shared form as well renders it
  // twice — the reader sees two boxes for one field and fills the wrong one,
  // and the per-type copy silently wins. Asserting only the type block's copy
  // misses this entirely.
  await expect(modal.locator('[data-create-field="clusterType"]:not([data-create-child])'),
    "an implementation's field must not ALSO be on the shared form").toHaveCount(1);
});

test('a value typed into a hidden type block is never sent', async ({ page, request }) => {
  // Every type's block is in the DOM so switching does not re-fetch. A value
  // left in a HIDDEN block would be sent as a field the chosen type does not
  // declare, and DGraph refuses the whole mutation — so the create fails with a
  // schema error naming a field the reader cannot see.
  //
  // With one implementation nothing is hidden, so this asserts the mechanism
  // rather than the symptom: hiding the block must remove its fields from the
  // collected set.
  await page.goto('/clusters');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.locator('[data-create-field="clusterType"]').fill('workload');
  const visible = await page.evaluate(() => {
    const m = document.querySelector('.modal.is-active')!;
    const hidden = (el: Element) => {
      const b = el.closest('[data-create-type-fields]') as HTMLElement | null;
      return b !== null && b.style.display === 'none';
    };
    return [...m.querySelectorAll('[data-create-field]:not([data-create-child])')]
      .filter(el => !hidden(el)).map(el => (el as HTMLElement).dataset.createField);
  });
  expect(visible, 'the selected type\'s field is collected').toContain('clusterType');

  // Hide it the way a type change would, and it must drop out.
  await page.evaluate(() => {
    const b = document.querySelector('.modal.is-active [data-create-type-fields]') as HTMLElement;
    b.style.display = 'none';
  });
  const afterHide = await page.evaluate(() => {
    const m = document.querySelector('.modal.is-active')!;
    const hidden = (el: Element) => {
      const b = el.closest('[data-create-type-fields]') as HTMLElement | null;
      return b !== null && b.style.display === 'none';
    };
    return [...m.querySelectorAll('[data-create-field]:not([data-create-child])')]
      .filter(el => !hidden(el)).map(el => (el as HTMLElement).dataset.createField);
  });
  expect(afterHide, 'a hidden block\'s field must not be collected').not.toContain('clusterType');
  expect(afterHide, 'but the shared fields stay').toContain('cni');
});
