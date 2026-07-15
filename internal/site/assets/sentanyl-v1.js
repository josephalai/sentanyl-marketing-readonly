/* sentanyl.js — browser SDK for Sentanyl frontend channels.
 *
 * Lets any coded (tenant-hosted) website use Sentanyl primitives — forms,
 * checkout, products/offers, newsletter signup, coaching booking, video
 * tracking — through the stable /api/public/* contract. The same script can
 * be used by builder-rendered pages.
 *
 * Load + configure:
 *   <script src="https://YOUR-SENTANYL-HOST/static/sentanyl.js"
 *           data-sentanyl-key="pub_xxx"
 *           data-sentanyl-domain="josephalai.net"
 *           data-sentanyl-auto></script>
 * or:
 *   Sentanyl.init({ publicKey: "pub_xxx", domain: "josephalai.net", apiBase: "" });
 *   Sentanyl.mountAll();
 *
 * Declarative attributes scanned by mountAll():
 *   <form data-sentanyl-form="form_public_id">…</form>
 *   <button data-sentanyl-checkout="offer_public_id">Buy</button>
 *   <form data-sentanyl-newsletter="newsletter_product_public_id">…</form>
 *   <div data-sentanyl-products></div>
 *   <div data-sentanyl-offers></div>
 *   <div data-sentanyl-coaching="program_public_id"></div>
 *   <video data-sentanyl-video="media_public_id" src="…"></video>
 *   <div data-sentanyl-quiz="quiz_public_id" data-sentanyl-lead></div>
 *   <a data-sentanyl-portal-link>My account</a>
 *
 * Programmatic API:
 *   Sentanyl.forms.submit(formId, payload)
 *   Sentanyl.checkout.start(offerId, {email, successUrl, cancelUrl})
 *   Sentanyl.newsletter.subscribe(productId, {email, tierId})
 *   Sentanyl.coaching.slots(programId)
 *   Sentanyl.coaching.book(programId, {starts_at, email, name, timezone})
 *   Sentanyl.video.track(mediaId, eventName, extra)
 *   Sentanyl.products.list() / Sentanyl.offers.list()
 *   Sentanyl.channel()
 *
 * Events dispatched on the source element (and document):
 *   sentanyl:form:success / sentanyl:form:error
 *   sentanyl:checkout:redirect / sentanyl:checkout:error
 *   sentanyl:newsletter:success / sentanyl:newsletter:error
 *   sentanyl:coaching:booked / sentanyl:coaching:error
 *   sentanyl:quiz:submitted / sentanyl:quiz:error
 *
 * No deps. No build step. ES2018. Strict mode. Hand-written.
 */
(function () {
  'use strict';

  if (window.Sentanyl) return;

  var SDK_VERSION = '1.0.0';

  var cfg = {
    apiBase: '',
    publicKey: '',
    domain: '',
  };
  var channelPromise = null;

  /* ─── bootstrap config from the <script> tag ────────────────────────── */

  var scriptEl = document.currentScript;
  if (scriptEl) {
    cfg.publicKey = scriptEl.getAttribute('data-sentanyl-key') || '';
    cfg.domain = scriptEl.getAttribute('data-sentanyl-domain') || '';
    cfg.apiBase = scriptEl.getAttribute('data-sentanyl-api') || '';
    if (!cfg.apiBase && scriptEl.src) {
      // Default the API base to wherever the script was loaded from, so a
      // coded site on another host talks back to its Sentanyl edge.
      try {
        var u = new URL(scriptEl.src);
        if (u.origin !== location.origin) cfg.apiBase = u.origin;
      } catch (e) {}
    }
  }

  /* ─── transport ─────────────────────────────────────────────────────── */

  function headers(json) {
    var h = {};
    if (json) h['Content-Type'] = 'application/json';
    if (cfg.publicKey) h['X-Sentanyl-Public-Key'] = cfg.publicKey;
    return h;
  }

  function withContext(payload) {
    payload = payload || {};
    if (cfg.domain && !payload.domain) payload.domain = cfg.domain;
    // Cross-block conversion attribution: if the video runtime is live on
    // this page, stamp its session onto the submission.
    if (window.__sentanylVideoSession && !payload.video_session_id) {
      payload.video_session_id = window.__sentanylVideoSession.sessionId;
    }
    return payload;
  }

  function eventId() {
    try { if (crypto && crypto.randomUUID) return crypto.randomUUID(); } catch (e) {}
    return 'evt_' + Date.now().toString(36) + '_' + Math.random().toString(36).slice(2);
  }

  function query() {
    var params = [];
    if (cfg.domain) params.push('domain=' + encodeURIComponent(cfg.domain));
    return params.length ? '?' + params.join('&') : '';
  }

  function get(path) {
    return fetch(cfg.apiBase + path + query(), { headers: headers(false) })
      .then(parseResponse);
  }

  function post(path, payload) {
    return fetch(cfg.apiBase + path, {
      method: 'POST',
      headers: headers(true),
      body: JSON.stringify(withContext(payload)),
    }).then(parseResponse);
  }

  function parseResponse(r) {
    return r.json().catch(function () { return {}; }).then(function (body) {
      if (!r.ok) {
        var err = new Error((body && body.error) || ('HTTP ' + r.status));
        err.status = r.status;
        err.body = body;
        throw err;
      }
      return body;
    });
  }

  function emit(el, name, detail) {
    var ev;
    try {
      ev = new CustomEvent(name, { detail: detail, bubbles: true });
    } catch (e) {
      return;
    }
    (el || document).dispatchEvent(ev);
  }

  /* ─── public API ────────────────────────────────────────────────────── */

  function channel() {
    if (!channelPromise) {
      channelPromise = get('/api/public/channel').catch(function (e) {
        channelPromise = null;
        throw e;
      });
    }
    return channelPromise;
  }

  var api = {
    version: SDK_VERSION,
    init: function (opts) {
      opts = opts || {};
      if (opts.publicKey) cfg.publicKey = opts.publicKey;
      if (opts.domain) cfg.domain = opts.domain;
      if (opts.apiBase != null) cfg.apiBase = String(opts.apiBase).replace(/\/$/, '');
      channelPromise = null;
      return api;
    },

    config: function () { return { apiBase: cfg.apiBase, publicKey: cfg.publicKey, domain: cfg.domain }; },

    channel: channel,

    products: {
      list: function () { return get('/api/public/products').then(function (b) { return b.products || []; }); },
      get: function (id) { return get('/api/public/products/' + encodeURIComponent(id)).then(function (b) { return b.product; }); },
    },

    offers: {
      list: function () { return get('/api/public/offers').then(function (b) { return b.offers || []; }); },
      get: function (id) { return get('/api/public/offers/' + encodeURIComponent(id)).then(function (b) { return b.offer; }); },
    },

    forms: {
      submit: function (formId, payload) {
        return post('/api/public/forms/' + encodeURIComponent(formId), payload);
      },
      // Whitelisted definition (name + fields incl. select options) for
      // rendering a form dynamically on a coded site.
      get: function (formId) {
        return get('/api/public/forms/' + encodeURIComponent(formId));
      },
    },

    checkout: {
      // Starts Stripe checkout and (by default) redirects the browser.
      start: function (offerId, payload) {
        payload = payload || {};
        var redirect = payload.redirect !== false;
        delete payload.redirect;
        var body = {
          email: payload.email,
          success_url: payload.successUrl || payload.success_url,
          cancel_url: payload.cancelUrl || payload.cancel_url,
        };
        return post('/api/public/checkout/' + encodeURIComponent(offerId), body)
          .then(function (resp) {
            if (redirect && resp && resp.checkout_url) {
              emit(document, 'sentanyl:checkout:redirect', resp);
              location.href = resp.checkout_url;
            }
            return resp;
          })
          .catch(function (err) {
            // already_purchased comes back as 409 with a portal redirect.
            if (err.status === 409 && err.body && err.body.redirect_url && redirect) {
              location.href = err.body.redirect_url;
              return err.body;
            }
            throw err;
          });
      },
    },

    newsletter: {
      subscribe: function (productId, payload) {
        return post('/api/public/newsletters/' + encodeURIComponent(productId) + '/subscribe', payload);
      },
    },

    coaching: {
      slots: function (programId) {
        return get('/api/public/coaching/' + encodeURIComponent(programId) + '/slots');
      },
      book: function (programId, payload) {
        return post('/api/public/coaching/' + encodeURIComponent(programId) + '/book', payload);
      },
    },

    quizzes: {
      get: function (quizId) {
        return get('/api/public/quizzes/' + encodeURIComponent(quizId)).then(function (b) { return b.quiz; });
      },
      submit: function (quizId, payload) {
        return post('/api/public/quizzes/' + encodeURIComponent(quizId) + '/submit', payload);
      },
    },

    portal: {
      // Resolves the customer-portal URL for this channel (course/library
      // handoff from a coded site). Falls back to /portal on the channel's
      // domain when no explicit portal base is configured.
      url: function (path) {
        return channel().then(function (ch) {
          var base = ch.portal_base_url || ('https://' + (ch.domain || cfg.domain || location.host) + '/portal');
          return base.replace(/\/$/, '') + (path || '');
        });
      },
    },

    video: {
      // Low-level event tracker for custom players. The declarative
      // <video data-sentanyl-video> path delegates to sentanyl-video.js.
      track: function (mediaId, eventName, extra) {
        extra = extra || {};
        return channel().then(function (ch) {
          return post('/api/video/events', {
            event_id: extra.event_id || eventId(),
            tenant_id: ch.tenant_id,
            media_id: mediaId,
            player_token: extra.player_token,
            session_id: extra.session_id,
            event_name: eventName,
            current_second: extra.current_second || 0,
            progress_percent: extra.progress_percent || 0,
            page_url: location.href,
            domain: cfg.domain || location.host,
            referrer: document.referrer || '',
            data: extra.data || {},
          });
        });
      },
    },

    mountAll: mountAll,
  };

  /* ─── declarative mounting ──────────────────────────────────────────── */

  function serializeForm(form) {
    var fields = {};
    var flat = {};
    Array.prototype.forEach.call(form.elements, function (input) {
      if (!input.name || input.disabled) return;
      if ((input.type === 'checkbox' || input.type === 'radio') && !input.checked) return;
      var v = input.value;
      switch (input.name) {
        case 'email': case 'name': case 'phone': case 'message': case 'next_url':
          flat[input.name] = v; break;
        default:
          // Repeated names (multiselect checkboxes) accumulate
          // comma-separated — the server splits multiselect on commas.
          fields[input.name] = fields[input.name] ? fields[input.name] + ',' + v : v;
      }
    });
    flat.fields = fields;
    return flat;
  }

  function mountForms(root) {
    each(root, '[data-sentanyl-form]', function (form) {
      form.addEventListener('submit', function (e) {
        e.preventDefault();
        var payload = serializeForm(form);
        api.forms.submit(form.getAttribute('data-sentanyl-form'), payload)
          .then(function (resp) {
            emit(form, 'sentanyl:form:success', resp);
            if (resp.status === 'pending_confirmation' || resp.pending_confirmation) {
              showInlineNote(form, 'Almost done — check your email to confirm.');
            } else if (resp.redirect_url) location.href = resp.redirect_url;
            else showInlineNote(form, form.getAttribute('data-sentanyl-success') || 'Thanks — you’re in!');
          })
          .catch(function (err) {
            emit(form, 'sentanyl:form:error', err);
            showInlineNote(form, (err.body && err.body.error) || 'Something went wrong. Please try again.', true);
          });
      });
    });
  }

  function mountCheckout(root) {
    each(root, '[data-sentanyl-checkout]', function (btn) {
      btn.addEventListener('click', function (e) {
        e.preventDefault();
        btn.disabled = true;
        var email = btn.getAttribute('data-sentanyl-email') || valueOf(btn.getAttribute('data-sentanyl-email-from')) || undefined;
        api.checkout.start(btn.getAttribute('data-sentanyl-checkout'), {
          email: email,
          successUrl: btn.getAttribute('data-sentanyl-success-url') || undefined,
          cancelUrl: btn.getAttribute('data-sentanyl-cancel-url') || undefined,
        }).catch(function (err) {
          btn.disabled = false;
          emit(btn, 'sentanyl:checkout:error', err);
          showInlineNote(btn, (err.body && err.body.error) || 'Checkout is unavailable right now.', true);
        });
      });
    });
  }

  function mountNewsletter(root) {
    each(root, '[data-sentanyl-newsletter]', function (form) {
      form.addEventListener('submit', function (e) {
        e.preventDefault();
        var payload = serializeForm(form);
        api.newsletter.subscribe(form.getAttribute('data-sentanyl-newsletter'), {
          email: payload.email || (payload.fields && payload.fields.email),
          tier_id: form.getAttribute('data-sentanyl-tier') || undefined,
          source: 'coded_website',
        }).then(function (resp) {
          emit(form, 'sentanyl:newsletter:success', resp);
          var msg = resp.status === 'pending_confirmation'
            ? 'Almost done — check your email to confirm.'
            : 'Subscribed. Welcome!';
          showInlineNote(form, form.getAttribute('data-sentanyl-success') || msg);
        }).catch(function (err) {
          emit(form, 'sentanyl:newsletter:error', err);
          showInlineNote(form, (err.body && err.body.error) || 'Subscription failed. Please try again.', true);
        });
      });
    });
  }

  function mountProducts(root) {
    each(root, '[data-sentanyl-products]', function (host) {
      api.products.list().then(function (products) {
        renderCards(host, products.map(function (p) {
          return { title: p.name, body: p.description, meta: p.product_type, img: p.thumbnail_url };
        }), 'sntl-product-card');
      }).catch(function () {});
    });
    each(root, '[data-sentanyl-offers]', function (host) {
      api.offers.list().then(function (offers) {
        renderCards(host, offers.map(function (o) {
          return { title: o.title, body: '', meta: formatAmount(o.amount, o.currency), checkout: o.public_id };
        }), 'sntl-offer-card');
        mountCheckout(host);
      }).catch(function () {});
    });
  }

  function mountCoaching(root) {
    each(root, '[data-sentanyl-coaching]', function (host) {
      var programId = host.getAttribute('data-sentanyl-coaching');
      api.coaching.slots(programId).then(function (resp) {
        host.innerHTML = '';
        if (resp.provider !== 'native') {
          var link = document.createElement('a');
          link.className = 'sntl-coaching-external';
          link.href = resp.custom_url || resp.event_type_uri || '#';
          link.textContent = host.getAttribute('data-sentanyl-cta') || 'Book a session';
          host.appendChild(link);
          return;
        }
        var slots = resp.slots || [];
        if (!slots.length) {
          host.appendChild(textDiv('sntl-coaching-empty', 'No open times right now — check back soon.'));
          return;
        }
        var list = document.createElement('div');
        list.className = 'sntl-coaching-slots';
        slots.slice(0, 30).forEach(function (slot) {
          var btn = document.createElement('button');
          btn.type = 'button';
          btn.className = 'sntl-coaching-slot';
          var when = new Date(slot.starts_at || slot.StartsAt);
          btn.textContent = when.toLocaleString();
          btn.addEventListener('click', function () {
            var email = prompt('Your email to confirm this booking:');
            if (!email) return;
            api.coaching.book(programId, {
              starts_at: (slot.starts_at || slot.StartsAt),
              email: email,
              timezone: (Intl.DateTimeFormat().resolvedOptions().timeZone || ''),
            }).then(function (resp) {
              emit(host, 'sentanyl:coaching:booked', resp);
              host.innerHTML = '';
              host.appendChild(textDiv('sntl-coaching-confirmed', 'Booked! Check your email for details.'));
            }).catch(function (err) {
              emit(host, 'sentanyl:coaching:error', err);
              showInlineNote(host, (err.body && err.body.error) || 'That time was just taken — pick another.', true);
            });
          });
          list.appendChild(btn);
        });
        host.appendChild(list);
      }).catch(function () {
        host.appendChild(textDiv('sntl-coaching-empty', 'Booking is unavailable right now.'));
      });
    });
  }

  function mountVideos(root) {
    var vids = (root || document).querySelectorAll('video[data-sentanyl-video]');
    if (!vids.length) return;
    channel().then(function (ch) {
      Array.prototype.forEach.call(vids, function (v) {
        // Adapt to the sentanyl-video.js runtime contract, then load it.
        v.setAttribute('data-sentanyl', '');
        v.setAttribute('data-media-public-id', v.getAttribute('data-sentanyl-video'));
        v.setAttribute('data-tenant-id', ch.tenant_id);
        if (cfg.domain) v.setAttribute('data-domain', cfg.domain);
      });
      if (!window.__sentanylVideoLoaded) {
        var s = document.createElement('script');
        s.src = cfg.apiBase + '/static/sentanyl-video-v1.js';
        document.head.appendChild(s);
      }
    }).catch(function () {});
  }

  function mountQuizzes(root) {
    each(root, '[data-sentanyl-quiz]', function (host) {
      var quizId = host.getAttribute('data-sentanyl-quiz');
      var wantLead = host.hasAttribute('data-sentanyl-lead');
      api.quizzes.get(quizId).then(function (quiz) {
        host.innerHTML = '';
        var form = document.createElement('form');
        form.className = 'sntl-quiz';
        form.appendChild(textDiv('sntl-quiz-title', quiz.title || 'Quiz'));
        (quiz.questions || []).forEach(function (q, qi) {
          var block = document.createElement('div');
          block.className = 'sntl-quiz-question';
          block.appendChild(textDiv('sntl-quiz-question-title', (qi + 1) + '. ' + q.title));
          if (q.type === 'text' || !(q.options || []).length) {
            var input = document.createElement('input');
            input.type = 'text';
            input.className = 'sntl-quiz-text';
            input.setAttribute('data-sntl-q', q.slug);
            block.appendChild(input);
          } else {
            q.options.forEach(function (opt, oi) {
              var label = document.createElement('label');
              label.className = 'sntl-quiz-option';
              var radio = document.createElement('input');
              radio.type = 'radio';
              radio.name = 'sntl-q-' + q.slug;
              radio.value = String(oi);
              label.appendChild(radio);
              label.appendChild(document.createTextNode(' ' + opt));
              block.appendChild(label);
            });
          }
          form.appendChild(block);
        });
        var emailInput = null;
        if (wantLead) {
          emailInput = document.createElement('input');
          emailInput.type = 'email';
          emailInput.required = true;
          emailInput.placeholder = host.getAttribute('data-sentanyl-email-placeholder') || 'Your email to get your results';
          emailInput.className = 'sntl-quiz-email';
          form.appendChild(emailInput);
        }
        var submit = document.createElement('button');
        submit.type = 'submit';
        submit.className = 'sntl-quiz-submit';
        submit.textContent = host.getAttribute('data-sentanyl-cta') || 'See my results';
        form.appendChild(submit);

        form.addEventListener('submit', function (e) {
          e.preventDefault();
          var answers = (quiz.questions || []).map(function (q) {
            if (q.type === 'text' || !(q.options || []).length) {
              var t = form.querySelector('[data-sntl-q="' + q.slug + '"]');
              return { question_slug: q.slug, answer_text: t ? t.value : '' };
            }
            var checked = form.querySelector('input[name="sntl-q-' + q.slug + '"]:checked');
            return { question_slug: q.slug, answer_index: checked ? parseInt(checked.value, 10) : -1 };
          });
          submit.disabled = true;
          api.quizzes.submit(quizId, {
            email: emailInput ? emailInput.value : undefined,
            answers: answers,
          }).then(function (resp) {
            emit(host, 'sentanyl:quiz:submitted', resp);
            form.innerHTML = '';
            var result = textDiv('sntl-quiz-result' + (resp.passed ? ' sntl-quiz-passed' : ' sntl-quiz-failed'),
              'You scored ' + resp.score + '% (' + resp.correct + '/' + resp.total + ')' +
              (resp.pass_threshold ? (resp.passed ? ' — passed!' : ' — ' + resp.pass_threshold + '% needed to pass.') : ''));
            form.appendChild(result);
          }).catch(function (err) {
            submit.disabled = false;
            emit(host, 'sentanyl:quiz:error', err);
            showInlineNote(form, (err.body && err.body.error) || 'Could not submit the quiz — try again.', true);
          });
        });
        host.appendChild(form);
      }).catch(function () {
        host.appendChild(textDiv('sntl-quiz-empty', 'Quiz unavailable right now.'));
      });
    });
  }

  function mountPortalLinks(root) {
    each(root, '[data-sentanyl-portal-link]', function (a) {
      var path = a.getAttribute('data-sentanyl-portal-link') || '';
      api.portal.url(path.charAt(0) === '/' ? path : (path ? '/' + path : '')).then(function (url) {
        if (a.tagName === 'A') a.href = url;
        else a.addEventListener('click', function () { location.href = url; });
      }).catch(function () {});
    });
  }

  function mountAll(root) {
    mountForms(root);
    mountCheckout(root);
    mountNewsletter(root);
    mountProducts(root);
    mountCoaching(root);
    mountVideos(root);
    mountQuizzes(root);
    mountPortalLinks(root);
    return api;
  }

  /* ─── tiny render helpers ───────────────────────────────────────────── */

  function each(root, sel, fn) {
    Array.prototype.forEach.call((root || document).querySelectorAll(sel), function (el) {
      if (el.__sntlMounted) return;
      el.__sntlMounted = true;
      fn(el);
    });
  }

  function valueOf(selector) {
    if (!selector) return '';
    var el = document.querySelector(selector);
    return el ? el.value : '';
  }

  function textDiv(cls, text) {
    var d = document.createElement('div');
    d.className = cls;
    d.textContent = text;
    return d;
  }

  function formatAmount(cents, currency) {
    var v = (cents || 0) / 100;
    try {
      return new Intl.NumberFormat(undefined, { style: 'currency', currency: (currency || 'usd').toUpperCase() }).format(v);
    } catch (e) {
      return '$' + v.toFixed(2);
    }
  }

  // Minimal unstyled cards with class hooks; coded sites style via CSS.
  function renderCards(host, items, cls) {
    host.innerHTML = '';
    var grid = document.createElement('div');
    grid.className = 'sntl-grid';
    items.forEach(function (item) {
      var card = document.createElement('div');
      card.className = cls;
      if (item.img) {
        var img = document.createElement('img');
        img.src = item.img;
        img.alt = item.title || '';
        img.className = 'sntl-card-img';
        card.appendChild(img);
      }
      card.appendChild(textDiv('sntl-card-title', item.title || ''));
      if (item.meta) card.appendChild(textDiv('sntl-card-meta', item.meta));
      if (item.body) card.appendChild(textDiv('sntl-card-body', item.body));
      if (item.checkout) {
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'sntl-card-buy';
        btn.setAttribute('data-sentanyl-checkout', item.checkout);
        btn.textContent = 'Buy now';
        card.appendChild(btn);
      }
      grid.appendChild(card);
    });
    host.appendChild(grid);
  }

  function showInlineNote(el, text, isError) {
    var note = el.__sntlNote;
    if (!note) {
      note = document.createElement('div');
      el.__sntlNote = note;
      if (el.parentNode) el.parentNode.insertBefore(note, el.nextSibling);
    }
    note.className = 'sntl-note' + (isError ? ' sntl-note-error' : ' sntl-note-success');
    note.textContent = text;
  }

  /* ─── boot ──────────────────────────────────────────────────────────── */

  window.Sentanyl = api;

  if (scriptEl && scriptEl.hasAttribute('data-sentanyl-auto')) {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', function () { mountAll(); });
    } else {
      mountAll();
    }
  }
})();
