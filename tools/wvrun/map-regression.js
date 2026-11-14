// Same deterministic checks run in Chromium and native macOS WebKit.
async function packetTrailRegression() {
  const panel=document.querySelector('#toolPanel');
  document.querySelector('#toolEmpty').hidden=true; panel.hidden=false;
  panel.innerHTML='<div class="card" id="map-regression" style="max-width:900px;padding:16px"></div>';
  const diag=new Diag(document.querySelector('#map-regression'),'203.0.113.9');
  diag.setKind('mtr');
  const make=(ttl,lat,lon,extra={})=>({ttl,addr:'203.0.113.'+ttl,lat,lon,asn:64500+ttl,asOrg:'Example Transit',city:['','Cape Town','Unknown','London','New York'][ttl]||'Transit',sent:10,recv:10,last:12,avg:12,best:10,worst:14,stdev:1,loss:0,...extra});
  diag.data={kind:'mtr',target:'203.0.113.9',hops:[make(1,-33.9,18.4),make(2,null,null,{scope:'private'}),make(3,51.5,-.1,{loss:10}),make(4,40.7,-74)]};
  diag.render();
  diag.body.querySelector('[data-route-view="map"]').click();
  const map=diag.routeMap;
  const assert=(ok,message)=>{if(!ok)throw new Error(message);};
  assert(map.points.length===3,'Private/unknown hop was placed on map');
  assert(map.svg.querySelector('.map-gap'),'Unknown-hop gap was not marked');
  map.el.querySelector('[data-hop="3"]').dispatchEvent(new MouseEvent('click',{bubbles:true}));
  assert(map.el.querySelector('.map-detail').textContent.includes('AS64503'),'Hop details missing');
  map.zoom(.65); const view=map.svg.getAttribute('viewBox');
  diag.data.hops[2].avg=24;diag.render();
  assert(map.svg.getAttribute('viewBox')===view,'Map jumped on refresh');
  assert(map.el.querySelector('.map-detail').textContent.includes('24'),'Live details stopped updating');
  diag.body.querySelector('[data-route-view="path"]').click();
  diag.body.querySelector('[data-route-view="map"]').click();
  assert(diag.routeMap===map && map.svg.getAttribute('viewBox')===view,'View toggle lost zoom');
  const normal=diag.data;
  map.update({hops:[make(1,0,0),make(2,0,179),make(3,0,-179),make(4,95,0)]});
  assert(map.points.length===3,'Zero or invalid coordinates mishandled');
  assert(map.svg.querySelectorAll('.map-lines path').length===3,'Dateline was not split');
  map.update({hops:[make(1,null,null)]});
  assert(map.points.length===0 && map.el.querySelector('.map-caption').textContent.includes('No public'),'Empty map misleading');
  map.update(normal);map.fit();
  const originalAPI=api, previousPeer=peerState.data;
  try {
    const failure='PeeringDB connection timed out; retrying';
    const row={asn:64500,org:'Example Network',cc:'',share:100,total:100,in:100,out:0,common:[],pending:true,pdb:null};
    const response={own:{asns:[64501],source:'manual'},ownPdb:[],networks:[row],offNet:100,onNet:0,local:0,pdbError:failure};
    api=async path=>path.startsWith('/api/peering?')?response:originalAPI(path);
    await loadPeering();
    assert(document.querySelector('#peerOwn').textContent.includes(failure),'Own network hides timeout');
    assert(renderPeer(row).sections.facts.includes(failure),'Network panel hides timeout');
    response.pdbError='';row.pending=false;row.pdb={found:true,name:'Example Network',policy:'Open',ixes:[]};
    await loadPeering();
    assert(renderPeer(row).sections.facts.includes('Example Network'),'Recovery did not display data');
    assert(!document.querySelector('#pdbNote').textContent.includes(failure),'Stale error after recovery');
  } finally {api=originalAPI;peerState.data=previousPeer;}
  return {map:true,missingLocations:true,dateline:true,liveZoom:true,peeringError:true,peeringRecovery:true};
}
