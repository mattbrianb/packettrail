// A wvrun script (see main.go): loads the website in the system WebKit and
// reports which download the page offers this computer. docs/os.js picks it
// from the browser's own description of the system, so this is the check
// that a real Safari-family browser on this OS gets the right button.
//
//   wvrun -lib <webview lib folder> -dir docs -page "index.html?still" -init tools/wvrun/os-test.js
//
// The last line printed is "RESULT: <os> <arch>", for example
// "RESULT: macos x64" on a Mac and "RESULT: linux x64" on a Linux PC.
(function () {
  if (window.__wvStarted) return;
  window.__wvStarted = true;
  // Each report is awaited, so the lines print in order and none is lost
  // when wvDone closes the window.
  const report = s => window.wvReport(String(s));
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  const shown = e => e.offsetParent !== null;
  const text = e => e.textContent.replace(/\s+/g, ' ').trim();
  window.addEventListener('load', async () => {
    try {
      await sleep(1200); // site.js is a module: let it run
      const root = document.documentElement;
      await report('engine: ' + navigator.userAgent);
      await report('navigator.platform: ' + navigator.platform + ', touch points: ' + navigator.maxTouchPoints);
      for (const a of document.querySelectorAll('.cta .btn.primary')) if (shown(a)) await report('hero button: ' + text(a) + ' -> ' + a.getAttribute('href').split('/').pop());
      for (const d of document.querySelectorAll('.dl-main')) {
        if (!shown(d)) continue;
        const parts = [...d.querySelectorAll('.btn, code, .fine')].filter(shown).map(text);
        await report('download section: ' + parts.join(' | '));
      }
      for (const h of document.querySelectorAll('.dl-grid .panel h3')) if (shown(h)) await report('panel: ' + text(h));
      const copy = [...document.querySelectorAll('[data-copy]')].find(shown);
      if (copy) {
        copy.click();
        await sleep(400);
        await report('copy button after a click: ' + text(copy));
      }
      await report('RESULT: ' + root.dataset.os + ' ' + root.dataset.arch);
    } catch (e) { await report('error: ' + e.message); await report('RESULT: could not run'); }
    await sleep(200);
    window.wvDone();
  });
})();
