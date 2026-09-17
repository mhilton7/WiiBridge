const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync('server/host-daemon/web/static/app.js', 'utf8');

function browser({hold = false} = {}) {
  const calls = [];
  const pending = [];
  const intervals = [];
  const events = new Map();
  const panel = (dataset) => ({dataset});
  const panels = {
    'pi-live': panel({statusUrl:'/pi', automaticSwitch:'false'}),
    'pi-dot': {classList:{add(){}, remove(){}}},
    'source-health': panel({statusUrl:'/source'}),
    'compatibility-panel': panel({statusUrl:'/compatibility'}),
    'save-overlay': panel({statusUrl:'/saves'}),
    'performance-panel': panel({summaryUrl:'/performance', refreshMs:'5000'}),
  };
  const document = {
    hidden:false,
    getElementById(id) { return panels[id] || null; },
    querySelector() { return null; },
    querySelectorAll(selector) { return selector === ".library-source" ? [panel({platform:"wii"}), panel({platform:"gamecube"})] : []; },
    addEventListener(name, fn) {
      const listeners = events.get(name) || [];
      listeners.push(fn);
      events.set(name,listeners);
    },
  };
  const window = {
    location:{pathname:'/',href:'https://localhost/',reload(){}},
    sessionStorage:{getItem(){return null;},removeItem(){},setItem(){}},
    addEventListener(){},
    setInterval(fn,ms) {intervals.push({fn,ms});},
    requestAnimationFrame(fn){fn();},
    scrollTo(){},scrollBy(){},
  };
  const response = {ok:true,async json(){return {pi:{},host:{}};}};
  const fetch = async (url) => {
    calls.push(url);
    if (hold) return await new Promise(resolve => pending.push(() => resolve(response)));
    return response;
  };
  vm.runInNewContext(source, {document,window,fetch,history:{},URL,Date,console});
  return {calls,pending,intervals,document,events};
}
async function settle() {
  for (let i=0;i<10;i++) await new Promise(setImmediate);
}
async function minute(b) {
  for (let ms=1000;ms<=60000;ms+=1000) {
    for (const timer of b.intervals) if (ms%timer.ms===0) timer.fn();
    await settle();
  }
}
async function main() {
  const b = browser();
  await settle();
  b.calls.length=0;
  await minute(b);
  const visible = b.calls.length;
  b.calls.length=0;
  b.document.hidden=true;
  await minute(b);
  const hidden = b.calls.length;
  b.calls.length=0;
  b.document.hidden=false;
  for (const fn of b.events.get('visibilitychange') || []) fn();
  await settle();
  const resumed = b.calls.length;
  const slow = browser({hold:true});
  await settle();
  for(let attempt=0;attempt<3;attempt++) {
    for(const timer of slow.intervals) timer.fn();
    await settle();
  }
  const pendingPi = slow.calls.filter(url=>url==='/pi').length;
  assert.equal(visible,44,'foreground polling cadence changed');
  const result = {visible_requests_per_minute:visible,hidden_requests_per_minute:hidden,requests_on_visible:resumed,pi_requests_before_first_response:pendingPi,build_active:false};
  if (process.argv.includes('--expect-optimized')) {
    assert.equal(hidden,0,'hidden tabs should not schedule new polls');
    assert.equal(resumed,6,'visible tabs should refresh all present panels');
    assert.equal(pendingPi,1,'only one refresh per panel may be in flight');
    // Responses can arrive after the page becomes hidden; completing them
    // must clear the guard so later visible refreshes still work.
    slow.document.hidden=true;
    for(const resolve of slow.pending.splice(0)) resolve();
    await settle();
    for(const resolve of slow.pending.splice(0)) resolve();
    await settle();
    slow.document.hidden=false;
    for(const fn of slow.events.get('visibilitychange') || []) fn();
    await settle();
    assert.equal(slow.calls.filter(url=>url==='/pi').length,2,'refresh did not resume');
  }
  console.log(JSON.stringify(result,null,2));
}
main().catch(error=>{console.error(error);process.exitCode=1;});
