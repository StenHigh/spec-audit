// Development-only HTTP/browser acceptance on deterministic synthetic output.
'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require(process.env.SPEC_AUDIT_PLAYWRIGHT||'playwright');
const base=process.argv[2],out=process.argv[3];
if(!base||!out)throw new Error('external_ui_test.cjs BASE_URL SCREENSHOT_DIR');
(async()=>{
 fs.mkdirSync(out,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:process.env.SPEC_AUDIT_CHROMIUM});
 try{
  const context=await browser.newContext({viewport:{width:1280,height:960}});
  const errors=[],requests=[];const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>requests.push(r.url()));
  await page.goto(new URL('index.html',base).href);
  assert.match(await page.locator('body').innerText(),/Вопросы к ТЗ/);
  assert.match(await page.locator('body').innerText(),/Динамика/);
  assert.match(await page.locator('body').innerText(),/fixture-B/);
  const overview=await (await page.request.get(new URL('overview.json',base).href)).json();
  assert.equal(overview.scopes.length,2);
  const scope=overview.scopes.find(s=>s.name==='fixture-scope');assert(scope);assert.equal(scope.history.length,2);assert(scope.delta.incomparable.length>0);assert.equal(scope.external.freshness.status,'changed');
  await page.screenshot({path:path.join(out,'corpus.png'),fullPage:true});
  const reportURL=new URL(scope.decided.report,base);
  const report=await (await page.request.get(new URL('report.json',reportURL).href)).json();
  const result=await (await page.request.get(new URL('result.json',reportURL).href)).json();
  assert.equal(result.snapshot_id,report.snapshot_id);assert.equal(result.basis.spec_version,'fixture-B');assert.equal(result.report.independent_review.conclusion,'confirmed');
  await page.goto(reportURL.href);
  assert.equal(await page.locator('.requirement').count(),report.requirements.length);
  for(const [value,min] of [['implementation_gap',1],['test_gap',1],['unknown',1],['spec_question',1],['disagreement',1]]){
   await page.selectOption('#requirement-filter',value);assert(await page.locator('.requirement:visible').count()>=min,value);
  }
  await page.click('#reset-requirements');
  for(const query of ['REQ-DEMO-002','Удвоение','rules.md']){
   await page.fill('#requirement-search',query);assert(await page.locator('.requirement:visible').count()>0,query);
  }
  await page.click('#reset-requirements');await page.locator('#req-REQ-DEMO-003 summary').click();
  assert.match(await page.locator('#req-REQ-DEMO-003').innerText(),/contradicted/);
  assert(await page.locator('#req-REQ-DEMO-003 a[href^="#e-"]').count()>=2,'both sides of evidence');
  await page.screenshot({path:path.join(out,'report.png'),fullPage:true});
  const source=report.sources.find(s=>s.path==='refs/context.md');assert(source&&source.full&&source.url&&source.raw_url);
  await page.goto(new URL(source.url,reportURL).href);
  assert.equal(await page.locator('#L1.selected').count(),1);assert.equal(await page.locator('#L2.selected').count(),0);
  assert.match(await page.locator('#L1').innerText(),/Первая строка/);
  const raw=await (await page.request.get(new URL(source.raw_url,reportURL).href)).body();assert(raw.subarray(0,3).equals(Buffer.from([239,187,191])));assert(raw.includes(Buffer.from('\r\n')));assert(raw.includes(Buffer.from('е\u0308')));
  await page.screenshot({path:path.join(out,'source-line-1.png'),fullPage:true});
  const sourceRange=new URL(source.url,reportURL);sourceRange.search='?start=2&end=3';sourceRange.hash='L2';await page.goto(sourceRange.href);assert.equal(await page.locator('.line.selected').count(),2);
  const old=scope.history.find(r=>r.run_id==='A');assert(old);const oldReportURL=new URL(old.report,base);const oldReport=await (await page.request.get(new URL('report.json',oldReportURL).href)).json();assert.equal(oldReport.external.spec_version,'fixture-A');
  const oldContext=oldReport.sources.find(s=>s.path==='context.md');assert(oldContext);await page.goto(new URL(oldContext.url,oldReportURL).href);assert.match(await page.locator('h1').innerText(),/context.md/);
  assert.equal(oldReport.external.freshness.status,'unknown');
  const conflictBase=new URL('conflict/',base);const conflictOverview=await (await page.request.get(new URL('overview.json',conflictBase).href)).json();assert(conflictOverview.totals.questions>0);
  const conflictURL=new URL(conflictOverview.scopes[0].decided.report,conflictBase);const conflict=await(await page.request.get(new URL('report.json',conflictURL).href)).json();
  const requirement=conflict.requirements[0].requirement;assert.equal(requirement.accepted.citations.length,2);assert(requirement.accepted.unresolved.length>0);
  await page.goto(conflictURL.href);await page.locator('.requirement summary').first().click();assert.match(await page.locator('.requirement').first().innerText(),/10 МиБ/);
  for(const citation of requirement.accepted.citations){const source=conflict.sources.find(s=>s.path===citation.path);assert(source&&source.full);const url=new URL(source.url,conflictURL);url.search=`?start=${citation.line_start}&end=${citation.line_end}`;url.hash=`L${citation.line_start}`;await page.goto(url.href);assert(await page.locator('.selected').count()>0)}
  const offline=await browser.newContext({javaScriptEnabled:false,viewport:{width:375,height:812}});const noJS=await offline.newPage();await noJS.goto(reportURL.href);assert.equal(await noJS.locator('.requirement').count(),5);await noJS.locator('.requirement summary').first().click();assert(await noJS.locator('a').filter({hasText:'Полный сохранённый исходник'}).count()>0);await noJS.screenshot({path:path.join(out,'mobile-no-js.png'),fullPage:true});await offline.close();
  assert.deepEqual(errors,[]);assert(requests.every(url=>url.startsWith(new URL(base).origin)),'external network request');
  console.log(JSON.stringify({passed:true,base_path:new URL(base).pathname,scopes:2,history:true,filters:true,search:true,exact_sources:true,bom_crlf_unicode:true,contradiction_sides:true,questions:true,roles:true,independent_review:true,baseline_incomparable:true,no_js:true,screenshots:fs.readdirSync(out)},null,2));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
