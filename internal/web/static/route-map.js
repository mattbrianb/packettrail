// Offline equirectangular route map. Coordinates are estimates, never device GPS.
class RouteMap {
  constructor(el) {
    this.el = el;
    this.view = [0, 0, 720, 360];
    this.selected = null;
    el.innerHTML = `<div class="map-controls"><button class="btn sm" data-map="in" aria-label="Zoom in">+</button><button class="btn sm" data-map="out" aria-label="Zoom out">−</button><button class="btn sm" data-map="fit">Fit route</button><button class="btn sm" data-map="world">World</button><label>Hop <select aria-label="Select a route hop"></select></label></div>
      <svg class="route-map-svg" viewBox="0 0 720 360" role="group" tabindex="0" aria-label="Estimated route locations. Drag or use arrow keys to pan; use zoom buttons."><image href="world-land.svg" width="720" height="360"/><g class="map-lines"></g><g class="map-points"></g></svg>
      <div class="map-detail" aria-live="polite"></div><p class="map-caption"></p><p class="map-disclaimer">Approximate IP locations · lines connect observed hops, not physical cable routes. Map: Natural Earth · locations: DB-IP.</p>`;
    this.svg = el.querySelector('svg');
    this.select = el.querySelector('select');
    el.addEventListener('click', e => {
      const action = e.target.closest('[data-map]')?.dataset.map;
      if (action === 'fit') this.fit();
      if (action === 'world') { this.view = [0, 0, 720, 360]; this.applyView(); }
      if (action === 'in' || action === 'out') this.zoom(action === 'in' ? .65 : 1.5);
      const point = e.target.closest('[data-hop]');
      if (point) { this.selected = Number(point.dataset.hop); this.details(); }
    });
    el.addEventListener('keydown', e => {
      if ((e.key === 'Enter' || e.key === ' ') && e.target.matches('[data-hop]')) { e.preventDefault(); e.target.dispatchEvent(new MouseEvent('click', {bubbles:true})); }
    });
    this.select.addEventListener('change', () => { this.selected = Number(this.select.value); this.details(); });
    this.svg.addEventListener('keydown', e => {
      if (e.target !== this.svg || !['ArrowLeft','ArrowRight','ArrowUp','ArrowDown'].includes(e.key)) return;
      e.preventDefault();
      this.view[0] += (e.key==='ArrowLeft'?-1:e.key==='ArrowRight'?1:0)*this.view[2]/10;
      this.view[1] += (e.key==='ArrowUp'?-1:e.key==='ArrowDown'?1:0)*this.view[3]/10;
      this.applyView();
    });
    this.svg.addEventListener('pointerdown', e => {
      if (e.button !== 0 || e.target.closest('[data-hop]')) return;
      this.drag = {x:e.clientX,y:e.clientY,view:[...this.view]};
      this.svg.setPointerCapture(e.pointerId);
    });
    this.svg.addEventListener('pointermove', e => {
      if (!this.drag) return;
      const rect = this.svg.getBoundingClientRect(), v = this.drag.view;
      // preserveAspectRatio="xMidYMid meet": use the same scale on both axes.
      const scale = Math.max(v[2]/rect.width, v[3]/rect.height);
      this.view = [v[0]-(e.clientX-this.drag.x)*scale,v[1]-(e.clientY-this.drag.y)*scale,v[2],v[3]];
      this.applyView();
    });
    for (const type of ['pointerup','pointercancel','lostpointercapture']) this.svg.addEventListener(type, () => { this.drag = null; });
  }
  applyView() {
    this.view[0] = Math.max(0,Math.min(720-this.view[2],this.view[0]));
    this.view[1] = Math.max(0,Math.min(360-this.view[3],this.view[1]));
    this.svg.setAttribute('viewBox',this.view.join(' '));
    this.svg.querySelectorAll('[data-hop]').forEach(p=>p.setAttribute('r',this.view[2]/150));
  }
  zoom(factor) {
    const [x,y,w,h] = this.view, nw = Math.max(12,Math.min(720,w*factor)), nh = nw/2;
    this.view = [x+(w-nw)/2,y+(h-nh)/2,nw,nh]; this.applyView();
  }
  fit() {
    if (!this.points?.length) return;
    const xs=this.points.map(p=>p.x), ys=this.points.map(p=>p.y);
    const x0=Math.min(...xs),x1=Math.max(...xs),y0=Math.min(...ys),y1=Math.max(...ys);
    const w=Math.min(720,Math.max(30,x1-x0+24,(y1-y0+24)*2));
    this.view=[(x0+x1-w)/2,(y0+y1-w/2)/2,w,w/2];this.applyView();
  }
  update(d) {
    this.hops=d.hops || [];
    this.points=this.hops.filter(h=>h.addr && !h.scope && Number.isFinite(h.lat) && Number.isFinite(h.lon) && Math.abs(h.lat)<=90 && Math.abs(h.lon)<=180).map(h=>({h,x:(h.lon+180)*2,y:(90-h.lat)*2}));
    const points=this.points;
    let lines='';
    for(let i=1;i<points.length;i++) {
      const a=points[i-1],b=points[i],gap=b.h.ttl-a.h.ttl>1;
      let x=b.x;
      if(x-a.x>360)x-=720;if(a.x-x>360)x+=720;
      const path=`M${a.x},${a.y}L${x},${b.y}`;
      lines+=`<path d="${path}" class="${gap?'map-gap':''}"/>`;
      if(x!==b.x) lines+=`<path d="M${a.x+(x<0?720:-720)},${a.y}L${b.x},${b.y}" class="${gap?'map-gap':''}"/>`;
    }
    this.svg.querySelector('.map-lines').innerHTML=lines;
    const focused=this.svg.contains(document.activeElement) ? document.activeElement.dataset.hop : null;
    const radius=this.view[2]/150;
    this.svg.querySelector('.map-points').innerHTML=points.map((p,i)=>`<circle cx="${p.x}" cy="${p.y}" r="${radius}" tabindex="0" role="button" data-hop="${p.h.ttl}" aria-label="Hop ${p.h.ttl}: ${esc(p.h.city || p.h.cc || p.h.addr)}" class="${p.h.loss>=50?'bad':p.h.loss>0 || (i>0 && p.h.avg-points[i-1].h.avg>=50)?'warn':'ok'}"><title>${esc(hopTitle(p.h))}</title></circle>`).join('');
    if(focused) this.svg.querySelector(`[data-hop="${Number(focused)}"]`)?.focus({preventScroll:true});
    const options=this.hops.map(h=>`<option value="${h.ttl}">${h.ttl} · ${esc(h.city || h.host || h.addr || 'No reply')}${points.some(p=>p.h.ttl===h.ttl)?'':' · unmapped'}</option>`).join('');
    if(this.select._html!==options){this.select.innerHTML=options;this.select._html=options;}
    if(!this.hops.some(h=>h.ttl===this.selected))this.selected=points[0]?.h.ttl ?? this.hops[0]?.ttl;
    const count=points.length, missing=this.hops.length-count;
    this.el.querySelector('.map-caption').textContent=count ? `${count} of ${this.hops.length} hops mapped${missing ? ` · ${missing} private, unanswered or without coordinates` : ''}. Dashed lines span unmapped hops. Yellow: loss or a hop increase of 50 ms or more; red: at least 50% loss.` : 'No public hop locations yet. Run a trace and check that the City database is installed in Settings.';
    if(count && !this.fitted){this.fit();this.fitted=true;}
    this.details();
  }
  details() {
    const h=this.hops?.find(h=>h.ttl===this.selected);
    this.select.value=String(this.selected);
    this.el.querySelector('.map-detail').textContent=h ? `Hop ${h.ttl} · ${h.addr || 'No reply'}${h.asn ? ` · AS${h.asn} ${h.asOrg || ''}`:''} · ${h.city || h.cc || h.scope || 'Location unknown'} · ${h.recv ? fmtMs(h.avg)+' ms' : 'No response'} · ${h.sent ? h.loss.toFixed(1)+'% loss' : 'Waiting'}` : 'Waiting for route results.';
    this.svg.querySelectorAll('[data-hop]').forEach(p=>p.classList.toggle('selected',Number(p.dataset.hop)===this.selected));
  }
}
