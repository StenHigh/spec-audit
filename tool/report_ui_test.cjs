// Dev-only: use an already installed Playwright; the binary has no JS dependency.
// Input must be the NEW synthetic page from TestReportUIFixture, not a real audit.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {chromium} = require(process.env.SPEC_AUDIT_PLAYWRIGHT || 'playwright');
const html = fs.readFileSync(process.argv[2], 'utf8');
assert(html.includes('<title>spec-audit — ui-fixture</title>'), 'Expected synthetic UI fixture');

(async () => {
  const browser = await chromium.launch({headless:true, executablePath:process.env.SPEC_AUDIT_CHROMIUM});
  try {
    const context = await browser.newContext({viewport:{width:1280,height:1000}});
    const errors = [], requests = [];
    async function pageWithStorage(values = null, denied = false, content = html) {
      const page = await context.newPage();
      page.on('pageerror', e => errors.push(e.message));
      page.on('request', r => requests.push(r.url()));
      // about:blank has no persistent origin. Exercise both storage outcomes explicitly.
      await page.evaluate(({values,denied}) => {
        window.savedPreferences = values || {};
        Object.defineProperty(window, 'localStorage', {get() {
          if (denied) throw new DOMException('denied', 'SecurityError');
          return {getItem:k => window.savedPreferences[k] ?? null, setItem:(k,v) => {window.savedPreferences[k] = v;}};
        }});
      }, {values,denied});
      await page.setContent(content);
      return page;
    }
    const page = await pageWithStorage();
    assert.equal(await page.evaluate(() => window.injected), undefined, 'source markup executed');
    assert.match(await page.locator('#req-REQ-UI-001 summary').innerText(), /проверка частичная или слабая/);
    assert.equal(await page.locator('.editor-link[href]').count(), 0);
    assert.equal(await page.locator('#code-root').inputValue(), '/workspace/My Project');
    assert.equal(await page.locator('#spec-root').inputValue(), '/workspace/My Project/TZ');
    await page.locator('#preferences summary').click();
    const background = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    await page.emulateMedia({colorScheme:'dark'});
    const dark = await background();
    await page.emulateMedia({colorScheme:'light'});
    const light = await background();
    assert.notEqual(dark, light, 'system theme did not react');
    await page.selectOption('#theme', 'dark');
    assert.equal(await background(), dark, 'explicit dark lost to system light');
    await page.selectOption('#theme', 'light');
    await page.emulateMedia({colorScheme:'dark'});
    assert.equal(await background(), light, 'explicit light lost to system dark');

    await page.locator('#code-root').fill('/copy/Код #1');
    await page.locator('#spec-root').fill('/copy/ТЗ & 2');
    for (const editor of ['zed','phpstorm','cursor','vscode']) {
      await page.selectOption('#editor', editor);
      const links = await page.locator('.editor-link').evaluateAll(es => es.map(e => ({path:e.dataset.path,line:e.dataset.line,href:e.getAttribute('href'),hidden:e.hidden})));
      assert(links.length > 10);
      for (const link of links) {
        assert(!link.hidden && link.href);
        const url = new URL(link.href);
        assert.equal(url.protocol, editor + ':');
        const expectedPath = link.path.startsWith('TZ/') ? '/copy/ТЗ & 2/' + link.path.slice(3) : '/copy/Код #1/' + link.path;
        if (editor === 'phpstorm') {
          assert.equal(url.hostname, 'open');
          assert.equal(url.searchParams.get('file'), expectedPath);
          assert.equal(url.searchParams.get('line'), link.line);
          assert.equal([...url.searchParams.keys()].length, 2);
        } else {
          assert.equal(url.hostname, 'file');
          assert.equal(decodeURIComponent(url.pathname), expectedPath + ':' + link.line + ':1');
          assert.equal(url.search, '');
        }
        assert.equal(url.hash, '', 'source # became URL fragment');
      }
    }
    const stored = await page.evaluate(() => window.savedPreferences);
    const restored = await pageWithStorage(stored);
    assert.equal(await restored.locator('#editor').inputValue(), 'vscode');
    assert.equal(await restored.locator('#spec-root').inputValue(), '/copy/ТЗ & 2');
    assert.equal(await restored.locator('#theme').inputValue(), 'light');

    for (const root of ['relative', '~/project', 'https://host/project', '/copy/../escape', '//host/share', '/copy/./src', '/copy\\src']) {
      await page.locator('#code-root').fill(root);
      assert.equal(await page.locator('#code-root').getAttribute('aria-invalid'), 'true');
      assert.equal(await page.locator('.source-file[data-kind="code"] .editor-link[href]').count(), 0);
      assert.match(await page.locator('#preferences-status').innerText(), /Исправьте путь/);
    }
    await page.locator('#code-root').fill('/');
    assert.equal(await page.locator('#code-root').getAttribute('aria-invalid'), 'false');
    // Stored data and DOM paths are untrusted, even when the generator normally validates them.
    const corrupt = await pageWithStorage({'spec-audit:theme':'"javascript:bad"', 'spec-audit:editor:/workspace/My Project':JSON.stringify({editor:'javascript', codeRoot:'/../bad', specRoot:42})});
    assert.equal(await corrupt.locator('#editor').inputValue(), '');
    assert.equal(await corrupt.locator('#theme').inputValue(), 'system');
    assert.equal(await corrupt.locator('#code-root').inputValue(), '/workspace/My Project');
    const oversized = await pageWithStorage({'spec-audit:editor:/workspace/My Project':JSON.stringify({editor:'zed', codeRoot:'/'+'x'.repeat(4096)})});
    assert.equal(await oversized.locator('#code-root').inputValue(), '/workspace/My Project', 'unbounded stored path');
    const denied = await pageWithStorage(null, true);
    await denied.locator('#preferences summary').click();
    await denied.selectOption('#theme', 'dark');
    await denied.selectOption('#editor', 'zed');
    assert((await denied.locator('.editor-link[href]').count()) > 0);
    assert.match(await denied.locator('#preferences-status').innerText(), /не разрешил его сохранить/);
    const damaged = await pageWithStorage({'spec-audit:theme':'{bad json'});
    assert.equal(await damaged.locator('#requirement-count').innerText(), '2 / 2 норм');

    await page.locator('button[data-section="TZ/other.md"]').click();
    assert.equal(await page.locator('#requirement-count').innerText(), '1 / 2 норм');
    await page.selectOption('#requirement-filter', 'partial_test');
    assert.equal(await page.locator('#requirement-count').innerText(), '0 / 2 норм');
    assert(await page.locator('#no-requirements').isVisible());
    await page.locator('#reset-requirements').click();
    await page.locator('[data-filter="partial_test"]').click();
    assert.equal(await page.locator('#requirement-count').innerText(), '1 / 2 норм');
    await page.evaluate(() => {location.hash = '#req-REQ-UI-002';});
    await page.waitForFunction(() => document.getElementById('req-REQ-UI-002').open);
    assert.equal(await page.locator('#requirement-count').innerText(), '2 / 2 норм');
    await page.locator('#reset-preferences').click();
    assert.equal(await page.locator('#editor').inputValue(), '');
    assert.equal(await page.locator('#theme').inputValue(), 'system');
    assert.equal(await page.locator('#code-root').inputValue(), '/workspace/My Project');
    await page.bringToFront();
    await page.locator('#editor').focus();
    await page.keyboard.press('Tab');
    assert.equal(await page.evaluate(() => document.activeElement.id), 'theme', 'keyboard focus order failed');
    // macOS headless native select does not commit arrow selection even on an empty page.
    // selectOption tests the real input/change behavior; popup keyboard acceptance is manual.

    await page.setViewportSize({width:375,height:900});
    await page.evaluate(() => {for (const e of document.querySelectorAll('details')) e.open = true;});
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'page overflows mobile viewport');
    await page.selectOption('#theme', 'dark');
    assert.equal(await background(), dark);
    await page.locator('#charts').screenshot({path:process.env.SPEC_AUDIT_UI_SCREENSHOT || '/private/tmp/spec-audit-ui-preview.png'});

    const noJS = await browser.newContext({javaScriptEnabled:false});
    const staticPage = await noJS.newPage();
    await staticPage.setContent(html);
    assert.equal(await staticPage.locator('#charts meter').count(), 9);
    await staticPage.locator('#req-REQ-UI-001 summary').click();
    assert(await staticPage.getByText('Проверяем код и существенные результаты отдельно.').first().isVisible());
    assert.equal(await staticPage.locator('.editor-link:visible').count(), 0);
    assert.equal(errors.length, 0, errors.join('\n'));
    assert.equal(requests.length, 0, 'offline page requested external resources');
    console.log('PASS: synthetic HTML — themes, storage/denial, 4 editor URI profiles, encoding, filters, anchors, keyboard focus, mobile, no-JS, no network');
  } finally { await browser.close(); }
})().catch(error => {console.error(error); process.exitCode = 1;});
