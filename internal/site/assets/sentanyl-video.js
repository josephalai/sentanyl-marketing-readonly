/* sentanyl-video.js — Wistia-style runtime player for published Sentanyl
 *
 * Activates any <video data-sentanyl> element on the page with:
 *   - chapters menu (seek on click)
 *   - mid-video turnstile (lead-capture overlay that pauses the video)
 *   - end-of-video CTA card (button + URL)
 *   - watch tracking → POST /api/video/events (play, progress, complete,
 *     turnstile_submit, cta_click, chapter_click)
 *
 * The element reads its config from data-* attributes:
 *   data-media-public-id   — Media.PublicId
 *   data-config-url        — fetch URL returning {chapters, interactions, badge_rules, ...}
 *   data-tenant-id         — tenant ObjectId hex (required by /api/video/events)
 *   data-domain            — request domain (defaults to location.host)
 *   data-funnel-public-id  — optional: stamps every event with funnel context
 *   data-stage-public-id   — optional: same for stage
 *
 * Once a session is created (first 'play' event), the runtime stores the
 * viewer_id + session_id in localStorage so reloads of the same media
 * resume the same session, and exposes them on
 *   window.__sentanylVideoSession = { mediaId, viewerId, sessionId }
 * so other blocks (Squeeze/Sales sections) can stamp the active session
 * onto their own submissions for cross-block conversion attribution.
 *
 * No deps. No build step. ES2018. Strict mode. ~12 KB hand-written.
 */
(function () {
  'use strict';

  if (window.__sentanylVideoLoaded) return;
  window.__sentanylVideoLoaded = true;

  var EVENT_URL = '/api/video/events';
  var STORAGE_PREFIX = 'sntl_video_';

  /* ─── tiny helpers ──────────────────────────────────────────────────── */

  function el(tag, attrs, kids) {
    var n = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (k === 'style' && typeof attrs[k] === 'object') {
          for (var s in attrs[k]) n.style[s] = attrs[k][s];
        } else if (k === 'on' && typeof attrs[k] === 'object') {
          for (var ev in attrs[k]) n.addEventListener(ev, attrs[k][ev]);
        } else if (k === 'class') {
          n.className = attrs[k];
        } else if (k === 'html') {
          n.innerHTML = attrs[k];
        } else {
          n.setAttribute(k, attrs[k]);
        }
      }
    }
    if (kids) {
      (Array.isArray(kids) ? kids : [kids]).forEach(function (kid) {
        if (kid == null) return;
        n.appendChild(typeof kid === 'string' ? document.createTextNode(kid) : kid);
      });
    }
    return n;
  }

  function randId() {
    return 'v_' + Math.random().toString(36).slice(2, 10) + Date.now().toString(36);
  }

  function getOrCreateSessionKey(mediaId) {
    var key = STORAGE_PREFIX + 'session_' + mediaId;
    var existing = null;
    try { existing = localStorage.getItem(key); } catch (e) {}
    if (existing) return existing;
    var fresh = randId();
    try { localStorage.setItem(key, fresh); } catch (e) {}
    return fresh;
  }

  function setViewerId(mediaId, viewerId) {
    try { localStorage.setItem(STORAGE_PREFIX + 'viewer_' + mediaId, viewerId); } catch (e) {}
  }

  function getViewerId(mediaId) {
    try { return localStorage.getItem(STORAGE_PREFIX + 'viewer_' + mediaId) || ''; } catch (e) { return ''; }
  }

  function fetchJSON(url, init) {
    return fetch(url, init).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    });
  }

  /* ─── per-element controller ────────────────────────────────────────── */

  function attach(video) {
    var mediaId = video.getAttribute('data-media-public-id') || '';
    var configUrl = video.getAttribute('data-config-url') || '';
    var tenantId = video.getAttribute('data-tenant-id') || '';
    var domain = video.getAttribute('data-domain') || location.host;
    var funnelId = video.getAttribute('data-funnel-public-id') || '';
    var stageId = video.getAttribute('data-stage-public-id') || '';

    if (!mediaId || !tenantId) return; // missing required wiring; bail silently

    var sessionId = getOrCreateSessionKey(mediaId);
    var viewerId = getViewerId(mediaId);

    // expose to other blocks on the same page
    window.__sentanylVideoSession = {
      mediaId: mediaId,
      viewerId: viewerId,
      sessionId: sessionId,
    };

    var config = { chapters: [], interactions: [], badge_rules: [] };
    var milestonesFired = {};
    var lastProgressPostAt = 0;
    var paused = true;
    var ended = false;
    var firedTurnstiles = {};
    var firedCTAs = {};
    var inTurnstile = false;

    /* config fetch — non-blocking; the player still works without it.
       The config response carries a signed player_token that the events
       endpoint requires (it derives tenant/media identity from the token),
       so event posts wait for the fetch to settle before sending. */
    var playerToken = '';
    var configSettled = Promise.resolve();
    if (configUrl) {
      configSettled = fetchJSON(configUrl).then(function (cfg) {
        config = Object.assign(config, cfg || {});
        if (config.player_token) {
          playerToken = config.player_token;
        }
        // The renderer emits an empty <source> when only mediaPublicId is
        // known server-side — adopt the config's playback_url so the video
        // actually has a playable source.
        if (config.playback_url && !video.currentSrc) {
          video.src = config.playback_url;
        }
        if (config.poster_url && !video.poster) {
          video.poster = config.poster_url;
        }
        renderChrome();
      }).catch(function () { /* swallow — degrade to bare player */ });
    }

    /* event POSTer */
    function post(eventName, extra) {
      var body = {
        tenant_id: tenantId,
        media_id: mediaId,
        viewer_id: viewerId || sessionId,
        session_id: sessionId,
        event_name: eventName,
        current_second: Math.floor(video.currentTime || 0),
        progress_percent: video.duration ? Math.floor((video.currentTime / video.duration) * 100) : 0,
        page_url: location.href,
        domain: domain,
        referrer: document.referrer || '',
        data: extra || {},
      };
      if (funnelId) body.data.funnel_public_id = funnelId;
      if (stageId) body.data.stage_public_id = stageId;
      // Wait for the config fetch so the signed player_token rides along;
      // use fetch with keepalive so unload events still fire.
      return configSettled.then(function () {
        if (playerToken) body.player_token = playerToken;
        return fetch(EVENT_URL, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
          keepalive: true,
        });
      }).then(function (r) {
        if (!r.ok) return null;
        return r.json();
      }).then(function (resp) {
        if (resp && resp.viewer_id && !viewerId) {
          viewerId = resp.viewer_id;
          setViewerId(mediaId, viewerId);
          window.__sentanylVideoSession.viewerId = viewerId;
        }
        return resp;
      }).catch(function () { return null; });
    }

    /* ── DOM scaffold ──────────────────────────────────────────────── */

    // Wrap the video in a positioned container so we can overlay things.
    var parent = video.parentNode;
    var wrap = el('div', { class: 'sntl-video-wrap', style: { position: 'relative', display: 'inline-block', maxWidth: '100%' } });
    parent.insertBefore(wrap, video);
    wrap.appendChild(video);

    var overlay = el('div', { class: 'sntl-overlay', style: {
      position: 'absolute', inset: '0', display: 'none', alignItems: 'center', justifyContent: 'center',
      background: 'rgba(0,0,0,0.7)', color: '#fff', textAlign: 'center', padding: '24px', zIndex: '5',
    }});
    wrap.appendChild(overlay);

    var chaptersBtn = el('button', {
      class: 'sntl-chapters-btn',
      style: {
        position: 'absolute', bottom: '8px', right: '8px',
        padding: '6px 10px', background: 'rgba(0,0,0,0.6)', color: '#fff',
        border: 'none', borderRadius: '4px', fontSize: '12px',
        cursor: 'pointer', zIndex: '4', display: 'none',
      },
    }, 'Chapters');
    wrap.appendChild(chaptersBtn);

    var chaptersPanel = el('div', { class: 'sntl-chapters-panel', style: {
      position: 'absolute', top: '0', right: '0', bottom: '0', width: '260px',
      background: 'rgba(0,0,0,0.85)', color: '#fff', padding: '12px',
      overflowY: 'auto', zIndex: '4', display: 'none',
    }});
    wrap.appendChild(chaptersPanel);

    chaptersBtn.addEventListener('click', function () {
      chaptersPanel.style.display = chaptersPanel.style.display === 'none' ? 'block' : 'none';
    });

    function renderChrome() {
      // Chapters
      if (config.chapters && config.chapters.length) {
        chaptersBtn.style.display = '';
        chaptersPanel.innerHTML = '';
        chaptersPanel.appendChild(el('div', { style: { fontWeight: '600', marginBottom: '8px' } }, 'Chapters'));
        config.chapters.forEach(function (ch) {
          var btn = el('div', {
            class: 'sntl-chapter',
            style: { padding: '8px 6px', borderBottom: '1px solid rgba(255,255,255,0.1)', cursor: 'pointer', fontSize: '13px' },
            on: { click: function () {
              video.currentTime = ch.start_sec || 0;
              chaptersPanel.style.display = 'none';
              post('chapter_click', { chapter_public_id: ch.public_id, chapter_title: ch.title });
              if (paused) video.play();
            } },
          }, [
            el('div', { style: { color: '#aaa', fontSize: '11px' } }, formatTime(ch.start_sec || 0)),
            el('div', null, ch.title || 'Chapter'),
          ]);
          chaptersPanel.appendChild(btn);
        });
      }
    }

    function formatTime(sec) {
      var m = Math.floor(sec / 60), s = Math.floor(sec % 60);
      return m + ':' + (s < 10 ? '0' : '') + s;
    }

    /* ── overlay renderers ─────────────────────────────────────────── */

    function showTurnstile(interaction) {
      if (firedTurnstiles[interaction.public_id]) return;
      firedTurnstiles[interaction.public_id] = true;
      inTurnstile = true;
      video.pause();
      overlay.innerHTML = '';
      overlay.style.display = 'flex';

      var card = el('div', { style: {
        background: '#fff', color: '#111', padding: '24px', borderRadius: '8px',
        maxWidth: '420px', width: '90%', textAlign: 'center',
      }});
      var cfg = (interaction.config || {});
      card.appendChild(el('h3', { style: { margin: '0 0 12px', fontSize: '20px' } }, cfg.headline || 'Keep watching'));
      if (cfg.subhead) card.appendChild(el('p', { style: { margin: '0 0 16px', color: '#555' } }, cfg.subhead));

      var form = el('form', {
        on: {
          submit: function (e) {
            e.preventDefault();
            var email = input.value.trim();
            if (!email) return;
            post('turnstile_submit', { email: email, interaction_public_id: interaction.public_id });
            // Identify so future events tie to this email.
            viewerId = email;
            setViewerId(mediaId, viewerId);
            window.__sentanylVideoSession.viewerId = email;
            overlay.style.display = 'none';
            inTurnstile = false;
            video.play();
          },
        },
      });
      var input = el('input', {
        type: 'email', required: 'true', placeholder: cfg.placeholder || 'you@example.com',
        style: { padding: '10px', width: '100%', boxSizing: 'border-box', border: '1px solid #ccc', borderRadius: '4px', fontSize: '15px' },
      });
      var submit = el('button', {
        type: 'submit',
        style: { marginTop: '12px', padding: '10px 20px', background: '#111', color: '#fff', border: 'none', borderRadius: '4px', cursor: 'pointer', fontWeight: '600' },
      }, cfg.button_text || 'Continue');
      form.appendChild(input);
      form.appendChild(el('div', null, submit));
      card.appendChild(form);
      overlay.appendChild(card);
      input.focus();
    }

    function showCTA(interaction) {
      if (firedCTAs[interaction.public_id]) return;
      firedCTAs[interaction.public_id] = true;
      overlay.innerHTML = '';
      overlay.style.display = 'flex';
      var cfg = (interaction.config || {});
      var card = el('div', { style: {
        background: '#fff', color: '#111', padding: '24px', borderRadius: '8px',
        maxWidth: '420px', width: '90%', textAlign: 'center',
      }});
      card.appendChild(el('h3', { style: { margin: '0 0 12px', fontSize: '20px' } }, cfg.headline || cfg.text || 'Ready for the next step?'));
      if (cfg.subhead) card.appendChild(el('p', { style: { margin: '0 0 16px' } }, cfg.subhead));
      var btn = el('a', {
        href: cfg.url || '#',
        target: cfg.target || '_self',
        style: { display: 'inline-block', padding: '12px 24px', background: '#111', color: '#fff', textDecoration: 'none', borderRadius: '4px', fontWeight: '600' },
        on: { click: function () { post('cta_click', { interaction_public_id: interaction.public_id, target: cfg.url }); } },
      }, cfg.button_text || cfg.text || 'Continue');
      card.appendChild(btn);
      var dismiss = el('div', {
        style: { marginTop: '12px', fontSize: '12px', color: '#666', cursor: 'pointer', textDecoration: 'underline' },
        on: { click: function () { overlay.style.display = 'none'; } },
      }, 'No thanks');
      card.appendChild(dismiss);
      overlay.appendChild(card);
    }

    /* ── timeline tick ─────────────────────────────────────────────── */

    function evaluateInteractions() {
      var t = video.currentTime;
      (config.interactions || []).forEach(function (i) {
        if (!i || (i.status && i.status !== 'active')) return;
        var inWindow = t >= (i.start_sec || 0) && t <= (i.end_sec || (i.start_sec || 0) + 3);
        if (!inWindow) return;
        if (i.kind === 'turnstile') showTurnstile(i);
        else if (i.kind === 'cta') showCTA(i);
        // 'annotation' is silent on the runtime; reserved for future overlay rendering.
      });
    }

    function maybeFireMilestone() {
      if (!video.duration) return;
      var pct = Math.floor((video.currentTime / video.duration) * 100);
      [25, 50, 75, 95].forEach(function (m) {
        if (pct >= m && !milestonesFired[m]) {
          milestonesFired[m] = true;
          post('progress', { milestone: m });
        }
      });
    }

    /* ── video event wiring ────────────────────────────────────────── */

    video.addEventListener('play', function () {
      if (paused) {
        post('play');
        paused = false;
      }
    });

    video.addEventListener('pause', function () {
      if (!ended && !inTurnstile) {
        post('pause');
        paused = true;
      }
    });

    video.addEventListener('timeupdate', function () {
      var now = Date.now();
      if (now - lastProgressPostAt > 5000) {
        lastProgressPostAt = now;
        post('progress');
      }
      maybeFireMilestone();
      evaluateInteractions();
    });

    video.addEventListener('ended', function () {
      ended = true;
      post('complete');
      // Fire end-CTA if config has any 'cta' interactions that weren't shown.
      (config.interactions || []).forEach(function (i) {
        if (i.kind === 'cta' && !firedCTAs[i.public_id]) showCTA(i);
      });
    });
  }

  /* ─── form-submit attribution shim ─────────────────────────────────── */
  // When a Squeeze/Sales section coexists with a video on the page, decorate
  // any form that POSTs to /api/marketing/site/form/submit or
  // /api/marketing/site/checkout/start to include the active video session.

  var origFetch = window.fetch;
  window.fetch = function (input, init) {
    try {
      var url = typeof input === 'string' ? input : (input && input.url) || '';
      if (init && init.method && init.method.toUpperCase() === 'POST' &&
          (url.indexOf('/api/marketing/site/form/submit') >= 0 ||
           url.indexOf('/api/marketing/site/checkout/start') >= 0 ||
           url.indexOf('/api/customer/newsletters/') >= 0) &&
          window.__sentanylVideoSession && init.body && typeof init.body === 'string') {
        try {
          var parsed = JSON.parse(init.body);
          if (!parsed.video_session_id) {
            parsed.video_session_id = window.__sentanylVideoSession.sessionId;
            init.body = JSON.stringify(parsed);
          }
        } catch (e) { /* not JSON; leave it */ }
      }
    } catch (e) {}
    return origFetch.apply(this, arguments);
  };

  /* ─── boot ──────────────────────────────────────────────────────────── */

  function init() {
    var vids = document.querySelectorAll('video[data-sentanyl]');
    Array.prototype.forEach.call(vids, attach);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
