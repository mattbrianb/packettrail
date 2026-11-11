// Works out which download fits the visitor's computer, so the page shows the
// right one from the first paint. It sets data-os ("windows", "macos" or
// "linux") and data-arch ("x64" or "arm64") on <html>; site.css shows the
// matching [data-for] / [data-arch-for] blocks.
//
// Windows x64 is the default. It is what the markup says with scripts off,
// and what anything unrecognised keeps: phones, tablets, ChromeOS and other
// systems have no PacketTrail build, so they see the Windows button and the full
// list of downloads below it.
(function (root) {
  'use strict';

  // detect takes a navigator-like object and returns { os, arch }.
  function detect(nav) {
    nav = nav || {};
    var data = nav.userAgentData || null;
    var ua = String(nav.userAgent || '');
    var plat = String((data && data.platform) || nav.platform || '');
    var handheld = (data && data.mobile === true) ||
      /Android|iPhone|iPad|iPod|Mobile|CrOS/i.test(ua) || /Android|iOS|Chrome ?OS/i.test(plat);
    var os = 'windows';
    if (!handheld) {
      if (/mac/i.test(plat) || /Macintosh|Mac OS X/.test(ua)) {
        // An iPad asks for desktop sites as a "Macintosh"; its touch screen gives it away.
        os = (nav.maxTouchPoints || 0) > 1 ? 'windows' : 'macos';
      } else if (/linux/i.test(plat) || /Linux/.test(ua)) {
        os = 'linux';
      }
    }
    // Macs need no guess: the Terminal installer picks the build itself, and
    // the zip links name both chips (a browser can't tell them apart).
    var arch = os === 'linux' && /aarch64|arm64|armv8/i.test(ua + ' ' + plat) ? 'arm64' : 'x64';
    return { os: os, arch: arch };
  }

  // refine asks Chromium browsers for the processor type, which the plain
  // user-agent string hides. It resolves to the (possibly corrected) guess.
  function refine(nav, guess) {
    var data = nav && nav.userAgentData;
    if (guess.os !== 'linux' || !data || !data.getHighEntropyValues) return Promise.resolve(guess);
    return data.getHighEntropyValues(['architecture']).then(function (v) {
      var arch = v.architecture === 'arm' ? 'arm64' : v.architecture === 'x86' ? 'x64' : guess.arch;
      return { os: guess.os, arch: arch };
    }, function () { return guess; });
  }

  function apply(doc, r) {
    doc.documentElement.setAttribute('data-os', r.os);
    doc.documentElement.setAttribute('data-arch', r.arch);
  }

  var api = { detect: detect, refine: refine, apply: apply };
  if (typeof module !== 'undefined' && module.exports) { module.exports = api; return; } // tests
  root.PacketTrailOS = api;
  try {
    var nav = root.navigator;
    // ?os=macos (or windows, linux) previews another system's view.
    var forced = new root.URLSearchParams(root.location.search).get('os');
    var guess = /^(windows|macos|linux)$/.test(forced || '') ? { os: forced, arch: 'x64' } : detect(nav);
    apply(root.document, guess);
    if (!forced) refine(nav, guess).then(function (r) { apply(root.document, r); });
  } catch (e) { /* keep the Windows default */ }
})(typeof window !== 'undefined' ? window : this);
