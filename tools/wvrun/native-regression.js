window.addEventListener('load', async () => {
 try {
    const panel = document.querySelector('#toolPanel');
    panel.hidden = false;
    document.querySelector('#toolEmpty').hidden = true;
    panel.innerHTML = '<div id="scroll-regression" style="width:500px"></div>';
    const diag = new Diag(document.querySelector('#scroll-regression'), '203.0.113.20');
    const hops = Array.from({length:20}, (_, i) => ({ttl:i+1,addr:'203.0.113.'+(i+1),asn:64500+i,asOrg:'Example Network',sent:10,recv:10,loss:0,last:12,avg:12,best:10,worst:14,stdev:1}));
    for (const kind of ['mtr', 'trace']) {
      diag.setKind(kind);
      diag.data = {kind,target:'203.0.113.20',hops,rounds:10};
      diag.running = true;
      diag.render();
      const path = diag.body.querySelector('.path-strip');
      const table = diag.body.querySelector('.diag-scroll');
      if (path.scrollWidth <= path.clientWidth || table.scrollWidth <= table.clientWidth) throw new Error('Fixture must overflow');
      for (const x of [240, 80, 300, 0]) {
        path.scrollLeft = x; table.scrollLeft = x;
        const before = [path.scrollLeft, table.scrollLeft];
        for (let round=0; round<3; round++) {
          diag.data.rounds++;
          hops.forEach(h => { h.sent++; h.recv++; });
          diag.render();
          if (diag.body.querySelector('.path-strip') !== path || diag.body.querySelector('.diag-scroll') !== table) throw new Error('Live refresh replaced scroll containers');
          if (path.scrollLeft !== before[0] || table.scrollLeft !== before[1]) throw new Error('Live refresh reset horizontal scrolling');
          if (!table.textContent.includes(String(hops[0].sent))) throw new Error('Statistics stopped updating');
        }
      }
      path.scrollLeft = 200; table.scrollLeft = 100;
      diag.finish('Completed');
      if (path.scrollLeft !== 200 || table.scrollLeft !== 100) throw new Error('Completion reset scrolling');
      diag.setKind(kind);
      if (diag.body.querySelector('.path-strip')) throw new Error('New diagnostic retained old results');
    }

 await packetTrailRegression();
 await window.wvReport('RESULT: macOS regressions passed');
 } catch(e) { await window.wvReport('RESULT: FAILED ' + e.stack); }
 window.wvDone();
});
