// Browser acceptance for a restored citation-only publication with two scopes.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.SPEC_AUDIT_PLAYWRIGHT || 'playwright');
const base = process.argv[2], out = process.argv[3];
if (!base || !out) throw new Error('corpus_control_ui_test.cjs BASE_URL SCREENSHOT_DIR');

(async () => {
  fs.mkdirSync(out, {recursive: true});
  const browser = await chromium.launch({headless: true, executablePath: process.env.SPEC_AUDIT_CHROMIUM});
  try {
    const context = await browser.newContext({viewport: {width: 1280, height: 960}});
    const page = await context.newPage(), errors = [], requests = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('request', r => requests.push(r.url()));
    await page.goto(new URL('index.html', base).href);
    const overview = await (await page.request.get(new URL('overview.json', base).href)).json();
    assert.equal(overview.corpus_control.schema_version, 1);
    assert.equal(overview.corpus_control.controls[0].spec_version, 'a-control');
    const a = overview.scopes.find(s => s.name === 'fixture-scope');
    const b = overview.scopes.find(s => s.name === 'second-scope');
    assert.equal(a.corpus_revision.status, 'checked');
    assert.equal(b.corpus_revision.status, 'not_checked');
    assert.equal(b.corpus_revision.audited_version, 'z-old');
    assert.equal(b.corpus_revision.control_version, 'a-control');
    assert.match(await page.locator('#corpus-control').innerText(), /a-control/);
    assert.match(await page.locator('[data-corpus-revision="checked"]').innerText(), /проверено на a-control/);
    assert.match(await page.locator('[data-corpus-revision="not_checked"]').innerText(), /проверено на z-old; на a-control не проверено/);
    assert.doesNotMatch(await page.locator('body').innerText(), /snapshot изменился|источники изменились/);
    for (const scope of overview.scopes) {
      assert.equal(scope.decided.source_availability, 'released');
      assert.equal(scope.decided.host_review_state, 'saved');
      const report = await page.request.get(new URL(scope.decided.report, base).href);
      assert.equal(report.status(), 200);
      assert.match(await report.text(), /Сохранённое решение/);
    }
    await page.screenshot({path: path.join(out, 'corpus-control.png'), fullPage: true});
    const mobile = await browser.newContext({javaScriptEnabled: false, viewport: {width: 390, height: 844}});
    const nojs = await mobile.newPage();
    await nojs.goto(new URL('index.html', base).href);
    assert.equal(await nojs.locator('[data-corpus-revision]').count(), 2);
    assert.match(await nojs.locator('[data-corpus-revision="not_checked"]').innerText(), /на a-control не проверено/);
    assert(await nojs.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'mobile page overflows');
    await nojs.screenshot({path: path.join(out, 'corpus-control-mobile-no-js.png'), fullPage: true});
    assert.deepEqual(errors, []);
    assert(requests.every(url => url.startsWith(new URL(base).origin)), 'unexpected network dependency');
    await mobile.close();
    await context.close();
    console.log(JSON.stringify({control: true, released_scopes: true, reports: true, mobile_no_js: true, errors}, null, 2));
  } finally {
    await browser.close();
  }
})().catch(e => { console.error(e); process.exit(1); });
