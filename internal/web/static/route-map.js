// Curated service addresses, not whole ASNs: most networks also use unicast.
const ROUTE_ANYCAST = new Set(['8.8.8.8','8.8.4.4','1.1.1.1','1.0.0.1','9.9.9.9','149.112.112.112','2001:4860:4860::8888','2001:4860:4860::8844','2606:4700:4700::1111','2606:4700:4700::1001','2620:fe::fe','2620:fe::9']);
const routeRadians = value => value*Math.PI/180;
function routeDistance(a,b) {
  const p=routeRadians(a.lat),q=routeRadians(b.lat),dl=routeRadians(a.lon-b.lon);
  return 6371*2*Math.asin(Math.min(1,Math.sqrt(Math.sin((p-q)/2)**2+Math.cos(p)*Math.cos(q)*Math.sin(dl/2)**2)));
}
function routeLocations(hops) {
  const accepted=[];
  return hops.map(h=>{
    const database=[h.city,h.cc].filter(Boolean).join(', ') || 'Unknown';
    const valid=h.addr && !h.scope && Number.isFinite(h.lat) && Number.isFinite(h.lon) && Math.abs(h.lat)<=90 && Math.abs(h.lon)<=180;
    const timed=h.recv>=3 && Number.isFinite(h.best) && h.best>0;
    const anycast=ROUTE_ANYCAST.has((h.addr || '').toLowerCase());
    // These are independently measured RTTs from the same origin. Never use
    // their difference as link latency. Vacuum light speed plus 500 km slack
    // makes this a conservative contradiction check, not a geolocation fix.
    const conflict=valid && timed && accepted.find(a=>a.recv>=3 && Number.isFinite(a.best) && a.best>0 && routeDistance(a,h)>150*(a.best+h.best)+500);
    const nearby=accepted.filter(a=>/^[A-Z]{2}$/.test(a.cc || '') && a.recv>=3 && Number.isFinite(a.best) && a.best>0 && a.best<30);
    const country=nearby.length>=2 && nearby.every(a=>a.cc===nearby[0].cc) && timed && h.best<30 ? nearby[0].cc : '';
    let status='database',label=database+' · database estimate',reason='Database estimate; physical location is not verified.';
    if(h.scope || !h.addr || !valid){status='unknown';label=h.scope || 'Location unknown';reason='No public coordinates available.';}
    if(anycast || conflict) {
      status=anycast?'anycast':'conflict';
      label=country ? `Likely ${new Intl.DisplayNames(['en'],{type:'region'}).of(country)} · inferred region` : anycast?'Anycast · site unknown':'Location uncertain';
      reason=(anycast?'Known anycast service; one IP can answer from multiple sites. ':'')+(conflict?'Database coordinates conflict with earlier hop estimates and measured timing; either database location may be wrong. ':'')+(country?'Earlier low-latency hops suggest this country, but no exact site is established. ':'')+`Database reports: ${database}. Precise pin withheld.`;
    }
    const plotted=valid && status==='database';
    if(plotted)accepted.push(h);
    return {h,status,label,reason,plotted};
  });
}

// Offline equirectangular route map. Coordinates are estimates, never device GPS.
class RouteMap {
  constructor(el) {
    this.el = el;
    this.view = [0, 0, 720, 360];
    this.mode = 'flat';
    this.selected = null;
    el.innerHTML = `<div class="map-controls"><button class="btn sm" data-map="flat" aria-pressed="true">Flat map</button><button class="btn sm" data-map="globe" aria-pressed="false">Globe</button><button class="btn sm" data-map="in" aria-label="Zoom in">+</button><button class="btn sm" data-map="out" aria-label="Zoom out">−</button><button class="btn sm" data-map="fit">Fit route</button><button class="btn sm" data-map="world">World</button><label>Hop <select aria-label="Select a route hop"></select></label></div>
      <svg class="route-map-svg" viewBox="0 0 720 360" role="group" tabindex="0" aria-label="Estimated route locations. Drag or use arrow keys to pan; use zoom buttons."><image href="world-land.svg" width="720" height="360"/><g class="map-lines"></g><g class="map-points"></g></svg>
      <canvas class="route-globe" width="360" height="360" tabindex="0" role="img" aria-label="Globe of estimated hop locations. Drag or use arrow keys to rotate. Select hops using the Hop menu." hidden></canvas><div class="map-detail" aria-live="polite"></div><p class="map-caption"></p><p class="map-disclaimer">Approximate IP locations · lines connect observed hops, not physical cable routes. Map: Natural Earth · locations: DB-IP.</p>`;
    this.svg = el.querySelector('svg');
    this.select = el.querySelector('select');
    el.addEventListener('click', e => {
      const action = e.target.closest('[data-map]')?.dataset.map;
      if (action === 'flat' || action === 'globe') this.setMode(action);
      if (action === 'fit') this.fit();
      if (action === 'world') { if(this.mode==='globe'){this.globe.reset();return;} this.view = [0, 0, 720, 360]; this.applyView(); }
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
  setMode(mode) {
    this.mode=mode;this.svg.toggleAttribute('hidden',mode!=='flat');
    this.el.querySelector('canvas').hidden=mode!=='globe';
    this.el.querySelectorAll('[data-map="flat"],[data-map="globe"]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.map===mode)));
    if(mode==='globe') {if(!this.globe)this.globe=new RouteGlobe(this.el.querySelector('canvas'),this);this.globe.draw();}
  }
  applyView() {
    this.view[0] = Math.max(0,Math.min(720-this.view[2],this.view[0]));
    this.view[1] = Math.max(0,Math.min(360-this.view[3],this.view[1]));
    this.svg.setAttribute('viewBox',this.view.join(' '));
    this.svg.querySelectorAll('[data-hop]').forEach(p=>p.setAttribute('r',this.view[2]/150));
  }
  zoom(factor) {
    if(this.mode==='globe'){this.globe.zoom(factor);return;}
    const [x,y,w,h] = this.view, nw = Math.max(12,Math.min(720,w*factor)), nh = nw/2;
    this.view = [x+(w-nw)/2,y+(h-nh)/2,nw,nh]; this.applyView();
  }
  fit() {
    if(this.mode==='globe'){this.globe.reset();return;}
    if (!this.points?.length) return;
    const xs=this.points.map(p=>p.x), ys=this.points.map(p=>p.y);
    const x0=Math.min(...xs),x1=Math.max(...xs),y0=Math.min(...ys),y1=Math.max(...ys);
    const w=Math.min(720,Math.max(30,x1-x0+24,(y1-y0+24)*2));
    this.view=[(x0+x1-w)/2,(y0+y1-w/2)/2,w,w/2];this.applyView();
  }
  update(d) {
    this.hops=d.hops || [];
    this.locations=routeLocations(this.hops);
    this.points=this.locations.filter(l=>l.plotted).map(l=>({h:l.h,x:(l.h.lon+180)*2,y:(90-l.h.lat)*2}));
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
    const options=this.hops.map(h=>`<option value="${h.ttl}">${h.ttl} · ${esc(this.locations.find(l=>l.h===h)?.label || h.addr || 'No reply')}${points.some(p=>p.h.ttl===h.ttl)?'':' · unmapped'}</option>`).join('');
    if(this.select._html!==options){this.select.innerHTML=options;this.select._html=options;}
    if(!this.hops.some(h=>h.ttl===this.selected))this.selected=points[0]?.h.ttl ?? this.hops[0]?.ttl;
    const count=points.length, missing=this.hops.length-count;
    this.el.querySelector('.map-caption').textContent=count ? `${count} of ${this.hops.length} hops mapped${missing ? ` · ${missing} private, unknown, anycast or conflicting locations` : ''}. Dashed lines span unmapped hops. Yellow: loss or a hop increase of 50 ms or more; red: at least 50% loss.` : this.locations.some(l=>l.status==='anycast' || l.status==='conflict') ? 'No reliable pins for this route. Anycast or conflicting database locations are withheld; select a hop for the evidence.' : 'No public hop locations yet. Run a trace and check that the City database is installed in Settings.';
    if(count && !this.fitted){this.fit();this.fitted=true;}
    this.details();
  }
  details() {
    const h=this.hops?.find(h=>h.ttl===this.selected);
    const location=this.locations?.find(l=>l.h===h);
    this.select.value=String(this.selected);
    this.el.querySelector('.map-detail').textContent=h ? `Hop ${h.ttl} · ${h.addr || 'No reply'}${h.asn ? ` · AS${h.asn} ${h.asOrg || ''}`:''} · ${location?.label || 'Location unknown'} · ${h.recv ? fmtMs(h.avg)+' ms' : 'No response'} · ${h.sent ? h.loss.toFixed(1)+'% loss' : 'Waiting'}` + (location ? ' · '+location.reason : '') : 'Waiting for route results.';
    this.svg.querySelectorAll('[data-hop]').forEach(p=>p.classList.toggle('selected',Number(p.dataset.hop)===this.selected));
    if(this.mode==='globe')this.globe.draw();
  }
}

// Small CPU-rendered orthographic globe: bundled texture, no WebGL/CDN required.
class RouteGlobe {
  constructor(canvas,owner) {
    this.canvas=canvas;canvas._routeGlobe=this;this.owner=owner;this.ctx=canvas.getContext('2d');this.scale=1;this.lon=20;this.lat=10;this.hits=[];
    const image=new Image();
    image.onload=()=>{const c=document.createElement('canvas');c.width=720;c.height=360;const x=c.getContext('2d');x.drawImage(image,0,0,720,360);this.texture=x.getImageData(0,0,720,360).data;this.draw();};
    image.onerror=()=>{this.failed=true;this.draw();};
    image.src='world-land.svg';
    canvas.addEventListener('pointerdown',e=>{if(e.button!==0)return;this.drag={x:e.clientX,y:e.clientY,lon:this.lon,lat:this.lat};canvas.setPointerCapture(e.pointerId);});
    canvas.addEventListener('pointermove',e=>{if(!this.drag)return;this.lon=this.drag.lon-(e.clientX-this.drag.x)*.5/this.scale;this.lat=Math.max(-85,Math.min(85,this.drag.lat+(e.clientY-this.drag.y)*.5/this.scale));this.schedule();});
    canvas.addEventListener('pointerup',e=>{
      if(this.drag && Math.hypot(e.clientX-this.drag.x,e.clientY-this.drag.y)<5){const r=canvas.getBoundingClientRect(),x=(e.clientX-r.left)*360/r.width,y=(e.clientY-r.top)*360/r.height;const hit=this.hits.find(p=>Math.hypot(p.x-x,p.y-y)<10);if(hit){owner.selected=hit.h.ttl;owner.details();}}
      this.drag=null;
    });
    for(const type of ['pointercancel','lostpointercapture'])canvas.addEventListener(type,()=>{this.drag=null;});
    canvas.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','ArrowUp','ArrowDown'].includes(e.key))return;e.preventDefault();this.lon+=(e.key==='ArrowLeft'?-10:e.key==='ArrowRight'?10:0);this.lat=Math.max(-85,Math.min(85,this.lat+(e.key==='ArrowUp'?10:e.key==='ArrowDown'?-10:0)));this.draw();});
    this.reset();
  }
  schedule(){if(this.frame)return;this.frame=requestAnimationFrame(()=>{this.frame=null;this.draw();});}
  reset(){const p=this.owner.points?.[0]?.h;this.lon=p?.lon ?? 20;this.lat=p?.lat ?? 10;this.scale=1;this.draw();}
  zoom(f){this.scale=Math.max(1,Math.min(3,this.scale/f));this.draw();}
  project(lat,lon){const a=routeRadians(lat),d=routeRadians(lon-this.lon),c=routeRadians(this.lat),r=158*this.scale;return {x:180+r*Math.cos(a)*Math.sin(d),y:180-r*(Math.cos(c)*Math.sin(a)-Math.sin(c)*Math.cos(a)*Math.cos(d)),z:Math.sin(c)*Math.sin(a)+Math.cos(c)*Math.cos(a)*Math.cos(d)};}
  draw(){
    if(!this.ctx || this.canvas.hidden || this.owner.el.hidden)return;
    const ctx=this.ctx,r=158*this.scale,c=routeRadians(this.lat),sin=Math.sin(c),cos=Math.cos(c),lon0=routeRadians(this.lon);
    const style=getComputedStyle(this.canvas),rgb=s=>(s.match(/[\d.]+/g)||[30,35,40]).slice(0,3).map(Number);
    const ocean=rgb(style.backgroundColor),land=rgb(style.color),pixels=ctx.createImageData(360,360);
    for(let py=0;py<360;py++)for(let px=0;px<360;px++){
      const x=(px-180)/r,y=(180-py)/r,rho=x*x+y*y;if(rho>1)continue;
      const z=Math.sqrt(1-rho),lat=Math.asin(z*sin+y*cos),lon=lon0+Math.atan2(x,z*cos-y*sin);
      const tx=((Math.floor((lon/Math.PI+1)*360)%720)+720)%720,ty=Math.max(0,Math.min(359,Math.floor((.5-lat/Math.PI)*360)));
      const isLand=this.texture && this.texture[(ty*720+tx)*4+3]>100;
      const base=isLand?land:ocean,k=(py*360+px)*4,shade=.65+.35*z;
      for(let j=0;j<3;j++)pixels.data[k+j]=base[j]*shade;
      pixels.data[k+3]=255;
    }
    ctx.clearRect(0,0,360,360);ctx.putImageData(pixels,0,0);
    ctx.save();ctx.beginPath();ctx.arc(180,180,r,0,Math.PI*2);ctx.clip();ctx.strokeStyle=style.color;ctx.lineWidth=1.2;
    const points=this.owner.points || [];
    for(let i=1;i<points.length;i++){
      const a=points[i-1].h,b=points[i].h,vec=h=>{const p=routeRadians(h.lat),q=routeRadians(h.lon);return [Math.cos(p)*Math.cos(q),Math.cos(p)*Math.sin(q),Math.sin(p)];};
      const va=vec(a),vb=vec(b),angle=Math.acos(Math.max(-1,Math.min(1,va.reduce((s,n,j)=>s+n*vb[j],0))));
      if(angle<.00001 || Math.abs(Math.sin(angle))<.00001)continue;
      ctx.setLineDash(b.ttl-a.ttl>1?[3,3]:[]);ctx.beginPath();let started=false;
      for(let n=0;n<=64;n++){const t=n/64,u=Math.sin((1-t)*angle)/Math.sin(angle),v=Math.sin(t*angle)/Math.sin(angle),vv=va.map((value,j)=>value*u+vb[j]*v),p=this.project(Math.atan2(vv[2],Math.hypot(vv[0],vv[1]))*180/Math.PI,Math.atan2(vv[1],vv[0])*180/Math.PI);if(p.z<0){started=false;continue;}if(started)ctx.lineTo(p.x,p.y);else ctx.moveTo(p.x,p.y);started=true;}ctx.stroke();
    }
    ctx.setLineDash([]);this.hits=[];
    for(const point of points){const p=this.project(point.h.lat,point.h.lon);if(p.z<0)continue;this.hits.push({...p,h:point.h});const svgPoint=this.owner.svg.querySelector(`[data-hop="${point.h.ttl}"]`);ctx.fillStyle=svgPoint?getComputedStyle(svgPoint).fill:style.color;ctx.beginPath();ctx.arc(p.x,p.y,3.5,0,Math.PI*2);ctx.fill();if(point.h.ttl===this.owner.selected){ctx.strokeStyle=style.color;ctx.lineWidth=2;ctx.beginPath();ctx.arc(p.x,p.y,6,0,Math.PI*2);ctx.stroke();}}
    ctx.restore();
    if(!this.texture){ctx.fillStyle=style.color;ctx.font='12px sans-serif';ctx.textAlign='center';ctx.fillText(this.failed?'Map outlines unavailable':'Loading map outlines…',180,345);}
  }
}

function redrawRouteGlobes(){document.querySelectorAll('.route-globe').forEach(c=>c._routeGlobe?.draw());}
