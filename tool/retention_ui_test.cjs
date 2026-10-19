// Development-only browser acceptance on deterministic citation-only output.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.SPEC_AUDIT_PLAYWRIGHT || 'playwright');
const base = process.argv[2], out = process.argv[3];
if (!base || !out) throw new Error('retention_ui_test.cjs BASE_URL SCREENSHOT_DIR');

(async () => {
  fs.mkdirSync(out, {recursive: true});
  const browser = await chromium.launch({headless: true, executablePath: process.env.SPEC_AUDIT_CHROMIUM});
  try {
    const context = await browser.newContext({viewport: {width: 1280, height: 960}});
    const errors = [], requests = [];
    const page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => requests.push(request.url()));
    await page.goto(new URL('index.html', base).href);
    const overview = await (await page.request.get(new URL('overview.json', base).href)).json();
    assert.equal(overview.scopes.length, 1);
    const scope = overview.scopes[0];
    assert.equal(scope.name, 'fixture-scope');
    assert.equal(scope.history.length, 1);
    assert.equal(scope.decided.freshness, 'unchecked');
    assert.equal(scope.decided.host_review_state, 'saved');
    assert.equal(scope.decided.source_availability, 'released');
    assert.doesNotMatch(await page.locator('body').innerText(), /snapshot изменился|источники изменились/);
    assert(scope.drift_unchecked, 'absent source currency must remain unchecked');
    assert.match(await page.locator('body').innerText(), /Вопросы к ТЗ/);
    await page.screenshot({path: path.join(out, 'citation-corpus.png'), fullPage: true});

    const reportURL = new URL(scope.decided.report, base);
    const report = await (await page.request.get(new URL('report.json', reportURL).href)).json();
    const result = await (await page.request.get(new URL('result.json', reportURL).href)).json();
    assert.equal(result.schema_version, 3);
    assert.equal(result.source_retention, 'citations');
    assert.equal(result.snapshot_id, report.snapshot_id);
    assert.equal(report.source_retention, 'citations');
    assert.equal(report.freshness, 'unchecked');
    assert.equal(report.host_review_state, 'saved');
    assert.equal(report.source_availability, 'released');
    assert.equal(report.independent_review.conclusion, 'confirmed');
    assert(report.host_review.history.length > 0);
    assert(report.sources.length > 0);
    for (const source of report.sources) {
      assert.equal(source.full, false, source.path);
      assert(!source.url && !source.raw_url, source.path);
      assert(source.limitation, source.path);
    }
    assert(!JSON.stringify(result).includes('UNQUOTED_SOURCE_MARKER'));
    await page.goto(reportURL.href);
    assert.equal(await page.locator('.requirement').count(), report.requirements.length);
    assert.equal(await page.locator('a[href*="sources/"]').count(), 0);
    assert.match(await page.locator('body').innerText(), /актуальность не проверена/);
    assert.doesNotMatch(await page.locator('body').innerText(), /источники изменились|Snapshot изменился|нужен новый run/);
    assert(await page.locator('.evidence pre').count() > 0, 'saved evidence quotes');
    assert(!await page.locator('body').textContent().then(text => text.includes('UNQUOTED_SOURCE_MARKER')));

    for (const query of ['REQ-DEMO-002', 'Удвоение', 'rules.md']) {
      await page.fill('#requirement-search', query);
      assert(await page.locator('.requirement:visible').count() > 0, query);
    }
    await page.click('#reset-requirements');
    for (const filter of ['implementation_gap', 'test_gap', 'unknown', 'disagreement']) {
      await page.selectOption('#requirement-filter', filter);
      assert(await page.locator('.requirement:visible').count() > 0, filter);
    }
    await page.click('#reset-requirements');
    await page.locator('#req-REQ-DEMO-003 summary').click();
    assert.match(await page.locator('#req-REQ-DEMO-003').innerText(), /contradicted/);
    const evidenceLinks = page.locator('#req-REQ-DEMO-003 a[href^="#e-"]');
    assert(await evidenceLinks.count() >= 2, 'both sides of contradiction');
    const anchor = await evidenceLinks.first().getAttribute('href');
    assert.equal(await page.locator(anchor).count(), 1, 'evidence anchor resolves');
    await page.selectOption('#file-kind', 'code');
    assert(await page.locator('.source-file:visible').count() > 0);
    await page.click('#reset-files');
    await page.screenshot({path: path.join(out, 'citation-report.png'), fullPage: true});

    const mobile = await browser.newContext({javaScriptEnabled: false, viewport: {width: 375, height: 812}});
    const noJS = await mobile.newPage();
    noJS.on('pageerror', error => errors.push(error.message));
    noJS.on('request', request => requests.push(request.url()));
    await noJS.goto(reportURL.href);
    assert.equal(await noJS.locator('.requirement').count(), report.requirements.length);
    await noJS.locator('#req-REQ-DEMO-003 summary').click();
    assert.match(await noJS.locator('#req-REQ-DEMO-003').innerText(), /contradicted/);
    await noJS.locator('.source-file:has(.evidence) > summary').first().click();
    assert(await noJS.locator('.evidence pre:visible').count() > 0, 'quotes readable without JS');
    assert.equal(await noJS.locator('a[href*="sources/"]').count(), 0);
    assert.match(await noJS.locator('noscript').innerText(), /все нормы, файлы и цитаты доступны/);
    await noJS.screenshot({path: path.join(out, 'citation-mobile-no-js.png'), fullPage: true});
    await mobile.close();
    assert.deepEqual(errors, []);
    assert(requests.every(url => url.startsWith(new URL(base).origin)), 'external network request');
    assert(requests.every(url => !url.includes('/sources/')), 'full source resource requested');
    console.log(JSON.stringify({passed: true, base_path: new URL(base).pathname, schema: 3,
      citation_only: true, unchecked: true, history: true, filters: true, search: true,
      contradiction_sides: true, anchors: true, no_js: true, mobile: true,
      external_requests: false, screenshots: fs.readdirSync(out)}, null, 2));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
