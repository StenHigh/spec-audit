// Dev-only Chromium/CDP acceptance, using Node's built-in WebSocket. No npm install.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {spawn} = require('node:child_process');
const {createHash} = require('node:crypto');
const [base, citations, output] = process.argv.slice(2);
if (!base || !citations || !output || !process.env.SPEC_AUDIT_CHROMIUM) throw new Error('SPEC_AUDIT_CHROMIUM=... node tool/publication_ui_test.cjs BASE_URL CITATIONS_URL OUTPUT');
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
(async () => {
  fs.mkdirSync(output, {recursive:true});
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'spec-audit-cdp-'));
  const chrome = spawn(process.env.SPEC_AUDIT_CHROMIUM, ['--headless','--no-sandbox','--disable-gpu','--disable-background-networking','--remote-debugging-port=0',`--user-data-dir=${profile}`,'about:blank'], {stdio:'ignore'});
  let ws;
  try {
    const portFile = path.join(profile,'DevToolsActivePort');
    for (let i=0;i<200 && !fs.existsSync(portFile);i++) { if(chrome.exitCode!==null) throw new Error('Chromium exited'); await pause(50); }
    const [port, endpoint] = fs.readFileSync(portFile,'utf8').trim().split('\n');
    ws = new WebSocket(`ws://127.0.0.1:${port}${endpoint}`);
    await new Promise((resolve,reject) => {ws.addEventListener('open',resolve,{once:true});ws.addEventListener('error',reject,{once:true});});
    let sequence=0, session;
    const pending=new Map(), events=[];
    ws.addEventListener('message',event => {
      const message=JSON.parse(event.data);
      if (message.id) { const p=pending.get(message.id); if(!p)return;pending.delete(message.id);clearTimeout(p.timer);message.error?p.reject(new Error(JSON.stringify(message.error))):p.resolve(message.result); }
      else events.push(message);
    });
    const send = (method, params={}, target=session) => new Promise((resolve,reject) => {
      const id=++sequence, timer=setTimeout(()=>{pending.delete(id);reject(new Error(`CDP timeout ${method}`));},20000);
      pending.set(id,{resolve,reject,timer});ws.send(JSON.stringify({id,method,params,...(target?{sessionId:target}:{})}));
    });
    const target=await send('Target.createTarget',{url:'about:blank'});
    session=(await send('Target.attachToTarget',{targetId:target.targetId,flatten:true})).sessionId;
    await send('Page.enable');await send('Runtime.enable');await send('Network.enable');
    const evaluate=async expression => {
      const r=await send('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});
      if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;
    };
    const navigate=async url => {
      const start=events.length;const r=await send('Page.navigate',{url});if(r.errorText)throw new Error(r.errorText);
      for(let i=0;i<200;i++){if(events.slice(start).some(e=>e.method==='Page.loadEventFired'))return;await pause(25);}throw new Error('Page load timeout');
    };
    const screenshot=async name => {const r=await send('Page.captureScreenshot',{format:'png',captureBeyondViewport:true});fs.writeFileSync(path.join(output,name),Buffer.from(r.data,'base64'));};
    const getJSON=async url => {const r=await fetch(url);assert(r.ok,`${r.status} ${url}`);return r.json();};
    const checkResult=async reportURL => {
      const result=await getJSON(new URL('result.json',reportURL));assert.equal(result.schema_version,4);assert(!result.report&&!result.decision);
      const raw=Buffer.from(await(await fetch(new URL(result.report_ref.path,reportURL))).arrayBuffer());
      assert.equal(result.report_ref.sha256,createHash('sha256').update(raw).digest('hex'));assert.equal(result.report_ref.bytes,raw.length);
      const report=JSON.parse(raw);assert.equal(report.run_id,result.run_id);assert.equal(report.snapshot_id,result.snapshot_id);assert.equal(report.independent_review.conclusion,'confirmed');
      return report;
    };
    await navigate(new URL('index.html',base).href);
    const view=await getJSON(new URL('overview.json',base));assert.equal(view.scopes.length,3);
    const body=await evaluate('document.body.innerText');
    for(const version of ['fixture-R3','fixture-A','fixture-R2'])assert(body.includes(`проверено на ${version}`),version);
    assert(body.includes('Проверено на редакции спецификации'));
    await screenshot('scopes-three-revisions.png');
    const selected=view.scopes.find(s=>s.name==='fixture-scope');assert.equal(selected.decided.run_id,'B');
    const reportURL=new URL(selected.decided.report,base);const report=await checkResult(reportURL);
    await navigate(reportURL.href);assert.equal(await evaluate("document.querySelectorAll('.requirement').length"),report.requirements.length);
    for(const filter of ['implementation_gap','test_gap','unknown','disagreement']){
      const count=await evaluate(`(()=>{const e=document.querySelector('#requirement-filter');e.value=${JSON.stringify(filter)};e.dispatchEvent(new Event('change'));return [...document.querySelectorAll('.requirement')].filter(x=>!x.hidden&&getComputedStyle(x).display!=='none').length})()`);assert(count>0,filter);
    }
    await evaluate("document.querySelector('#reset-requirements').click()");
    assert((await evaluate("document.querySelectorAll('.evidence pre').length"))>0);
    await screenshot('report-interactive.png');
    const historicalBase=new URL('historical/',base);const historical=await getJSON(new URL('overview.json',historicalBase));
    const oldURL=new URL(historical.scopes[0].decided.report,historicalBase);const old=await checkResult(oldURL);
    assert.equal(old.external.spec_version,'fixture-A');assert.equal(old.run_id,'A');await navigate(oldURL.href);assert((await evaluate('document.body.innerText')).includes('fixture-A'));
    const cv=await getJSON(new URL('overview.json',citations));const citationURL=new URL(cv.scopes[0].decided.report,citations);const cr=await checkResult(citationURL);
    assert.equal(cr.source_retention,'citations');assert.equal(cr.freshness,'unchecked');assert(cr.sources.every(s=>!s.full&&!s.url&&!s.raw_url));
    await send('Emulation.setDeviceMetricsOverride',{width:375,height:812,deviceScaleFactor:1,mobile:true});
    await send('Emulation.setScriptExecutionDisabled',{value:true});
    await navigate(new URL('index.html',base).href);const mobileBody=await evaluate('document.body.innerText');
    for(const version of ['fixture-R3','fixture-A','fixture-R2'])assert(mobileBody.includes(version));
    await screenshot('mobile-scopes-no-js.png');
    await navigate(citationURL.href);
    assert.equal(await evaluate("document.querySelectorAll('.requirement').length"),cr.requirements.length);
    assert.equal(await evaluate("document.querySelectorAll('a[href*=\"sources/\"]').length"),0);
    const text=await evaluate('document.body.textContent');assert(!text.includes('UNQUOTED_SOURCE_MARKER'));assert(text.includes('актуальность не проверена'));
    await screenshot('citations-mobile-no-js.png');
    assert(!events.some(e=>e.method==='Runtime.exceptionThrown'),'browser exception');
    const requests=events.filter(e=>e.method==='Network.requestWillBeSent').map(e=>e.params.request.url);
    assert(requests.every(url=>url.startsWith(new URL(base).origin)),'external resource request');
    console.log(JSON.stringify({passed:true,scope_revisions:3,historical:true,compact_v4_hashes:true,filters:true,quotes:true,citations:true,no_js:true,mobile:true,base_path:new URL(base).pathname,screenshots:fs.readdirSync(output)},null,2));
  } finally { if(ws)ws.close();chrome.kill();await Promise.race([new Promise(resolve=>chrome.once('exit',resolve)),pause(2000)]);fs.rmSync(profile,{recursive:true,force:true}); }
})().catch(error=>{console.error(error);process.exitCode=1;});
