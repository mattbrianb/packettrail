// A wvrun script (see main.go): drives the real PacketTrail page inside WebKit.
// It opens the "curl" app, expands its first connection, starts the MTR,
// scrolls the connection list to the bottom and checks that the scroll
// position holds for 16 seconds of live updates.
//
// WebKit used to throw the scroll back to the top on every refresh, because
// the panel was a CSS size query container. Chrome's engine never showed it.
// The last line printed is "RESULT: scroll held" or "RESULT: scroll JUMPED".
//
// It needs a PacketTrail instance that has seen a long-running curl download.
(function () {
  if (window.__wvStarted) return;
  window.__wvStarted = true;
  const report = s => window.wvReport(String(s));
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  const waitFor = async (fn, ms = 20000) => {
    const t = Date.now();
    while (Date.now() - t < ms) { const v = fn(); if (v) return v; await sleep(150); }
    return null;
  };
  const fail = why => { report(why); report('RESULT: could not run'); window.wvDone(); };
  window.addEventListener('load', async () => {
    try {
      report('engine: ' + navigator.userAgent);
      const appRow = await waitFor(() => [...document.querySelectorAll('#t-apps tbody tr[data-key]')].find(tr => /curl/.test(tr.textContent)));
      if (!appRow) return fail('no curl row among ' + document.querySelectorAll('#t-apps tbody tr[data-key]').length + ' apps');
      appRow.click();
      const flowRow = await waitFor(() => document.querySelector('#t-flows tbody tr[data-key]'));
      if (!flowRow) return fail('the curl app lists no connections');
      flowRow.click();
      const run = await waitFor(() => document.querySelector('#t-flows .diag-run'));
      if (!run) return fail('the expanded connection has no Start button');
      run.click();
      await sleep(2500);
      const sheet = document.getElementById('detail');
      report(`panel ${Math.round(sheet.getBoundingClientRect().width)} px wide, container-type ${getComputedStyle(sheet).containerType}`);
      const box = document.getElementById('wrap-flows');
      box.scrollTop = box.scrollHeight;
      await sleep(120);
      const start = Math.round(box.scrollTop);
      if (start < 100) return fail(`the list is not tall enough to scroll (${start} px)`);
      const samples = [];
      for (let i = 0; i < 64; i++) { await sleep(250); samples.push(Math.round(box.scrollTop)); }
      const status = (document.querySelector('#t-flows .diag-status') || {}).textContent || '';
      report(`scrolled to ${start}; over 16 s it ranged ${Math.min(...samples)} to ${Math.max(...samples)}; test status: ${status}`);
      report(Math.min(...samples) < start - 60 ? 'RESULT: scroll JUMPED' : 'RESULT: scroll held');
    } catch (e) { report('error: ' + e.message); report('RESULT: could not run'); }
    window.wvDone();
  });
})();
