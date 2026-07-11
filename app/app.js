// ─────────────────────────────────────────────────────────────────────────────
// Global auth helpers — used by all child components so we don't need closure
// args (which require arrow functions, unsupported in the Alpine CSP evaluator).
// window._loggedIn   : true when a real session cookie is active
// window._testUserID : userid string (set for both real users and test-user mode)
// window._currentUser: full user object ({ userid, username, profileImage, ... })
// ─────────────────────────────────────────────────────────────────────────────
// thumbUrl(url, cssWidth) — appends &w=<actual-pixels> to an imgproxy URL so
// the backend can downscale large source images to fit the display slot.
// cssWidth is the CSS pixel width of the display element; we multiply by
// devicePixelRatio so retina screens still get sharp images.
// Non-proxy URLs (e.g. /uploads/...) are returned unchanged.
function thumbUrl(url, cssWidth) {
  if (!url || url.indexOf('/api/v1/imgproxy') === -1) return url;
  var dpr = window.devicePixelRatio || 1;
  var w   = Math.round(cssWidth * dpr);
  return url + '&w=' + w;
}

// sortTemplates(list) — canonical display order for display templates:
// smallest photo_count first, then alphabetical by name. The backend already
// returns templates in this order; this is used client-side after a
// create/save so the in-memory list doesn't need a full reload to re-sort.
function sortTemplates(list) {
  return (list || []).slice().sort(function (a, b) {
    return (a.photo_count - b.photo_count) || a.name.localeCompare(b.name);
  });
}

function getAuthHeaders() {
  if (window._loggedIn) return {};   // cookie handles auth for real sessions
  return window._testUserID ? { 'X-User-ID': window._testUserID } : {};
}
function getCurrentUser() {
  return window._currentUser || null;
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilities
// ─────────────────────────────────────────────────────────────────────────────
function formatDate(dateStr) {
  if (!dateStr) return '';
  try {
    const d = new Date(dateStr), now = new Date(), diff = (now - d) / 1000;
    if (diff < 5)     return 'just now';
    if (diff < 60)    return `${Math.floor(diff)}s ago`;
    if (diff < 3600)  return `${Math.floor(diff/60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff/3600)}h ago`;
    if (diff < 604800)return `${Math.floor(diff/86400)}d ago`;
    return d.toLocaleDateString(undefined, { year:'numeric', month:'short', day:'numeric' });
  } catch { return dateStr; }
}

function avatarSrc(user) {
  if (!user) return '';
  return user.profileImage || '';
}

// ─────────────────────────────────────────────────────────────────────────────
// commentsPanel — top-level comment list + new comment posting.
// Takes only `photo`; auth is handled via globals.
// ─────────────────────────────────────────────────────────────────────────────
function commentsPanel(photo) {
  return {
    comments:    photo.comments || [],
    newText:     '',
    posting:     false,
    loadingMore: false,

    init() {
      document.addEventListener('photoapp:comment-deleted', (e) => {
        const idx = this.comments.findIndex(c => c.commentid === e.detail);
        if (idx !== -1) {
          this.comments[idx] = { ...this.comments[idx], deleted: true };
        }
      });
    },

    async post() {
      const text = this.newText.trim();
      if (!text || !getCurrentUser()) return;
      this.posting = true;
      try {
        const resp = await fetch(`/api/v1/comments?photoid=${encodeURIComponent(photo.photoid)}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body: JSON.stringify({ comment: text }),
        });
        if (!resp.ok) throw new Error((await resp.json().catch(() => ({}))).error || `HTTP ${resp.status}`);
        const c = await resp.json();
        this.comments.unshift(c);
        this.newText = '';
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Failed to post: ${e.message}` }));
      }
      this.posting = false;
    },

    async loadMore() {
      if (!photo.commentsurl) return;
      this.loadingMore = true;
      try {
        const resp = await fetch(photo.commentsurl);
        const data = await resp.json();
        this.comments.push(...(data.comments || []));
        photo.commentsurl = data.pages?.next ? data.pages.next : null;
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Failed to load more: ${e.message}` }));
      }
      this.loadingMore = false;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// commentItem — single comment row: editing, deleting, replies.
// depth is 0-based; Reply is hidden at depth >= 5 (6-level max).
// Takes only c, photoid, depth; auth via globals.
// ─────────────────────────────────────────────────────────────────────────────
function commentItem(c, photoid, depth = 0) {
  return {
    c,
    depth,
    photoid,
    expanded:     false,
    clampLines:   4,
    needsClamp:   false,
    editing:      false,
    editText:     '',
    saving:       false,
    showingReplies: false,
    loadingReplies: false,
    replyOpen:    false,
    replyText:    '',
    postingReply: false,
    replies:      [],

    get canReply() { return !!getCurrentUser() && this.depth < 5; },

    // Returns '[deleted]' when the comment has been soft-deleted so the template
    // doesn't need per-level conditional logic around c.comment.
    get commentBody() { return this.c.deleted ? '[deleted]' : (this.c.comment || ''); },

    init() {
      // When a reply inside this comment's thread is deleted, mark it in the
      // replies array so it shows '[deleted]' rather than disappearing.
      document.addEventListener('photoapp:comment-deleted', (e) => {
        const idx = this.replies.findIndex(r => r.commentid === e.detail);
        if (idx !== -1) {
          this.replies[idx] = { ...this.replies[idx], deleted: true };
        }
      });
    },

    checkClamp() {
      this.$nextTick(() => {
        const el = this.$refs.commentBody;
        if (el) {
          const lineH = parseFloat(getComputedStyle(el).lineHeight) || 20;
          this.needsClamp = el.scrollHeight > lineH * this.clampLines + 4;
        }
      });
    },

    showMore() {
      this.clampLines += 1;
      const el = this.$refs.commentBody;
      if (el) {
        const lh = parseFloat(getComputedStyle(el).lineHeight) || 20;
        if (el.scrollHeight <= lh * this.clampLines + 4) this.expanded = true;
      }
    },

    startEdit() {
      this.editText = this.c.comment;
      this.editing  = true;
    },
    cancelEdit() { this.editing = false; },

    async saveEdit() {
      const text = this.editText.trim();
      if (!text) return;
      this.saving = true;
      try {
        const resp = await fetch(`/api/v1/comments/${encodeURIComponent(this.c.commentid)}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body: JSON.stringify({ comment: text }),
        });
        if (!resp.ok) throw new Error((await resp.json().catch(() => ({}))).error || `HTTP ${resp.status}`);
        const updated = await resp.json();
        this.c.comment = updated.comment;
        this.editing = false;
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Edit failed: ${e.message}` }));
      }
      this.saving = false;
    },

    async deleteComment() {
      if (!confirm('Delete this comment?')) return;
      const commentid = this.c.commentid;
      try {
        const resp = await fetch(`/api/v1/comments/${encodeURIComponent(commentid)}`, {
          method: 'DELETE', headers: getAuthHeaders(),
        });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        // Mark this component's own comment as deleted so it shows '[deleted]'
        // immediately and the replies beneath it remain visible.
        this.c = { ...this.c, deleted: true };
        this.editing = false;
        document.dispatchEvent(new CustomEvent('photoapp:comment-deleted', { detail: commentid }));
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Delete failed: ${e.message}` }));
      }
    },

    async loadReplies() {
      this.showingReplies  = true;
      this.loadingReplies  = true;
      try {
        const resp = await fetch(`/api/v1/comments?photoid=${encodeURIComponent(photoid)}&parentid=${encodeURIComponent(this.c.commentid)}`);
        const data = await resp.json();
        this.replies = data.comments || [];
      } catch(e) { console.error('Failed to load replies', e); }
      this.loadingReplies = false;
    },

    async postReply() {
      const text = this.replyText.trim();
      if (!text || !getCurrentUser()) return;
      this.postingReply = true;
      try {
        const resp = await fetch(
          `/api/v1/comments?photoid=${encodeURIComponent(photoid)}&parentid=${encodeURIComponent(this.c.commentid)}`,
          {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
            body: JSON.stringify({ comment: text }),
          }
        );
        if (!resp.ok) throw new Error((await resp.json().catch(() => ({}))).error || `HTTP ${resp.status}`);
        const reply = await resp.json();
        this.replies.push(reply);
        this.c.replycount = (this.c.replycount || 0) + 1;
        this.replyText    = '';
        this.replyOpen    = false;
        this.showingReplies = true;
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Reply failed: ${e.message}` }));
      }
      this.postingReply = false;
    },

    formatDate,
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// Skintone swatch colours (Fitzpatrick order).
// ─────────────────────────────────────────────────────────────────────────────
const SKINTONE_COLORS = {
  'light':        '#FFDBB4',
  'medium-light': '#EAC085',
  'medium':       '#C68642',
  'medium-dark':  '#8D5524',
  'dark':         '#4A2912',
};

// ─────────────────────────────────────────────────────────────────────────────
// emojiPicker — emoji browser + reaction toggling.
// Takes `photo`; auth via global getAuthHeaders().
// ─────────────────────────────────────────────────────────────────────────────
function emojiPicker(photo) {
  return {
    photo,
    search:     '',
    emojis:     [],
    total:      0,
    offset:     0,
    limit:      64,
    loading:    false,
    reactedIds: new Set(),

    skintoneTarget:   null,
    skintoneVariants: [],
    skintoneLoading:  false,

    async init() {
      const uid = window._testUserID;   // set for both real sessions and test-user mode
      if (uid) {
        (photo.emojis || []).forEach(em => {
          if (em.users && em.users.some(u => u.id === uid)) {
            this.reactedIds.add(em.emojiid);
          }
        });
      }
      await this.load();
    },

    async load() {
      this.loading = true;
      try {
        const params = new URLSearchParams({ limit: this.limit, offset: this.offset });
        if (this.search) params.set('search', this.search);
        const resp = await fetch(`/api/v1/emoji/types?${params}`);
        const data = await resp.json();
        this.emojis = data.emojis || [];
        this.total  = data.total  || 0;
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Failed to load emojis: ${e.message}` }));
      }
      this.loading = false;
    },

    doSearch() { this.offset = 0; this.skintoneTarget = null; this.load(); },
    nextPage()  { this.offset += this.limit; this.skintoneTarget = null; this.load(); },
    prevPage()  { this.offset = Math.max(0, this.offset - this.limit); this.skintoneTarget = null; this.load(); },

    skintoneColor(tone) { return SKINTONE_COLORS[tone] || '#FFD700'; },

    async openSkintone(em) {
      if (this.skintoneTarget && this.skintoneTarget.emojiid === em.emojiid) {
        this.skintoneTarget = null;
        return;
      }
      this.skintoneTarget  = em;
      this.skintoneVariants = [];
      this.skintoneLoading  = true;
      try {
        const resp = await fetch(`/api/v1/emoji/variants?hexcode=${encodeURIComponent(em.hexcode)}`);
        const data = await resp.json();
        this.skintoneVariants = data.variants || [];
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Failed to load variants: ${e.message}` }));
      }
      this.skintoneLoading = false;
    },

    handleEmojiClick(em) {
      if (em.has_skintones) {
        this.openSkintone(em);
      } else {
        this.skintoneTarget = null;
        this.react(em);
      }
    },

    async react(em) {
      if (!window._loggedIn && !window._testUserID) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: 'Select a user to react.' }));
        return;
      }
      const headers = getAuthHeaders();
      const alreadyReacted = this.reactedIds.has(em.emojiid);
      const method = alreadyReacted ? 'DELETE' : 'POST';
      try {
        const resp = await fetch(
          `/api/v1/emoji/react?photoid=${encodeURIComponent(this.photo.photoid)}&emojiid=${encodeURIComponent(em.emojiid)}`,
          { method, headers }
        );
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || `HTTP ${resp.status}`);
        }
        if (alreadyReacted) {
          this.reactedIds.delete(em.emojiid);
          const uid = window._testUserID;
          const existing = this.photo.emojis.find(e => e.emojiid === em.emojiid);
          if (existing) {
            existing.count--;
            if (uid) existing.users = (existing.users || []).filter(u => u.id !== uid);
            if (existing.count <= 0) this.photo.emojis = this.photo.emojis.filter(e => e.emojiid !== em.emojiid);
          }
        } else {
          this.reactedIds.add(em.emojiid);
          const uid = window._testUserID;
          const cu = getCurrentUser();
          const userEntry = uid ? { id: uid, name: (cu && cu.username) || '', tn: (cu && cu.profileImage) || null } : null;
          const existing = this.photo.emojis.find(e => e.emojiid === em.emojiid);
          if (existing) {
            existing.count++;
            if (userEntry) existing.users = [...(existing.users || []), userEntry];
          } else {
            this.photo.emojis.push({ emojiid: em.emojiid, emoji: em.emoji, imageurl: em.imageurl, alttext: em.alttext, count: 1, users: userEntry ? [userEntry] : [] });
          }
        }
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Reaction failed: ${e.message}` }));
      }
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// labelColorFor — stable, muted color keyed to the label *name*, not position.
// Colors are assigned lazily on first sight and cached for the session.
// ─────────────────────────────────────────────────────────────────────────────
const _labelColorCache = {};
// 100 muted label colors: 20 hues × 5 saturation/lightness variants.
// Hues spaced 18° apart; saturation 18–28%; lightness 41–54% (all legible with white text).
const _LABEL_PALETTE = [
// awk 'BEGIN { for (i = 0; i < 360; i += 3) printf("  '"'"'hsl(%3d 25%% 50%%)'"'"',\n", i, "%", "%"); }'
  'hsl(  0 25% 50%)', 'hsl(  3 25% 50%)', 'hsl(  6 25% 50%)', 'hsl(  9 25% 50%)', 'hsl( 12 25% 50%)',
  'hsl( 15 25% 50%)', 'hsl( 18 25% 50%)', 'hsl( 21 25% 50%)', 'hsl( 24 25% 50%)', 'hsl( 27 25% 50%)',
  'hsl( 30 25% 50%)', 'hsl( 33 25% 50%)', 'hsl( 36 25% 50%)', 'hsl( 39 25% 50%)', 'hsl( 42 25% 50%)',
  'hsl( 45 25% 50%)', 'hsl( 48 25% 50%)', 'hsl( 51 25% 50%)', 'hsl( 54 25% 50%)', 'hsl( 57 25% 50%)',
  'hsl( 60 25% 50%)', 'hsl( 63 25% 50%)', 'hsl( 66 25% 50%)', 'hsl( 69 25% 50%)', 'hsl( 72 25% 50%)',
  'hsl( 75 25% 50%)', 'hsl( 78 25% 50%)', 'hsl( 81 25% 50%)', 'hsl( 84 25% 50%)', 'hsl( 87 25% 50%)',
  'hsl( 90 25% 50%)', 'hsl( 93 25% 50%)', 'hsl( 96 25% 50%)', 'hsl( 99 25% 50%)', 'hsl(102 25% 50%)',
  'hsl(105 25% 50%)', 'hsl(108 25% 50%)', 'hsl(111 25% 50%)', 'hsl(114 25% 50%)', 'hsl(117 25% 50%)',
  'hsl(120 25% 50%)', 'hsl(123 25% 50%)', 'hsl(126 25% 50%)', 'hsl(129 25% 50%)', 'hsl(132 25% 50%)',
  'hsl(135 25% 50%)', 'hsl(138 25% 50%)', 'hsl(141 25% 50%)', 'hsl(144 25% 50%)', 'hsl(147 25% 50%)',
  'hsl(150 25% 50%)', 'hsl(153 25% 50%)', 'hsl(156 25% 50%)', 'hsl(159 25% 50%)', 'hsl(162 25% 50%)',
  'hsl(165 25% 50%)', 'hsl(168 25% 50%)', 'hsl(171 25% 50%)', 'hsl(174 25% 50%)', 'hsl(177 25% 50%)',
  'hsl(180 25% 50%)', 'hsl(183 25% 50%)', 'hsl(186 25% 50%)', 'hsl(189 25% 50%)', 'hsl(192 25% 50%)',
  'hsl(195 25% 50%)', 'hsl(198 25% 50%)', 'hsl(201 25% 50%)', 'hsl(204 25% 50%)', 'hsl(207 25% 50%)',
  'hsl(210 25% 50%)', 'hsl(213 25% 50%)', 'hsl(216 25% 50%)', 'hsl(219 25% 50%)', 'hsl(222 25% 50%)',
  'hsl(225 25% 50%)', 'hsl(228 25% 50%)', 'hsl(231 25% 50%)', 'hsl(234 25% 50%)', 'hsl(237 25% 50%)',
  'hsl(240 25% 50%)', 'hsl(243 25% 50%)', 'hsl(246 25% 50%)', 'hsl(249 25% 50%)', 'hsl(252 25% 50%)',
  'hsl(255 25% 50%)', 'hsl(258 25% 50%)', 'hsl(261 25% 50%)', 'hsl(264 25% 50%)', 'hsl(267 25% 50%)',
  'hsl(270 25% 50%)', 'hsl(273 25% 50%)', 'hsl(276 25% 50%)', 'hsl(279 25% 50%)', 'hsl(282 25% 50%)',
  'hsl(285 25% 50%)', 'hsl(288 25% 50%)', 'hsl(291 25% 50%)', 'hsl(294 25% 50%)', 'hsl(297 25% 50%)',
  'hsl(300 25% 50%)', 'hsl(303 25% 50%)', 'hsl(306 25% 50%)', 'hsl(309 25% 50%)', 'hsl(312 25% 50%)',
  'hsl(315 25% 50%)', 'hsl(318 25% 50%)', 'hsl(321 25% 50%)', 'hsl(324 25% 50%)', 'hsl(327 25% 50%)',
  'hsl(330 25% 50%)', 'hsl(333 25% 50%)', 'hsl(336 25% 50%)', 'hsl(339 25% 50%)', 'hsl(342 25% 50%)',
  'hsl(345 25% 50%)', 'hsl(348 25% 50%)', 'hsl(351 25% 50%)', 'hsl(354 25% 50%)', 'hsl(357 25% 50%)',
];
function labelColorFor(name) {
  if (_labelColorCache[name]) return _labelColorCache[name];
  // djb2-style hash over the label name string.
  let h = 5381;
  for (let i = 0; i < name.length; i++) h = ((h << 5) + h) ^ name.charCodeAt(i);
  const color = _LABEL_PALETTE[Math.abs(h) % _LABEL_PALETTE.length];
  _labelColorCache[name] = color;
  return color;
}

// ─────────────────────────────────────────────────────────────────────────────
// labelEditor — add/edit a label on a photo.
// Takes data (null=add, obj=edit) and photo; auth via global.
// ─────────────────────────────────────────────────────────────────────────────
function labelEditor(data, photo) {
  return {
    photo,
    editingLabel: data,

    knownNames:  [],
    knownValues: [],
    loadingValues: false,

    selectedName:  data ? data.name  : '',
    customName:    '',
    nameIsOther:   false,

    selectedValue: '',
    customValue:   '',
    valueIsOther:  false,

    saving: false,

    get effectiveName()  { return this.nameIsOther  ? this.customName.trim()  : this.selectedName; },
    get effectiveValue() { return this.valueIsOther ? this.customValue.trim() : this.selectedValue; },

    async init() {
      try {
        const r = await fetch('/api/v1/label-names');
        const d = await r.json();
        this.knownNames = d.names || [];
      } catch { this.knownNames = []; }

      if (this.editingLabel) {
        if (!this.knownNames.includes(this.editingLabel.name)) {
          this.knownNames = [this.editingLabel.name, ...this.knownNames];
        }
        await this.loadValues(this.editingLabel.name);
        if (this.knownValues.includes(this.editingLabel.value)) {
          this.selectedValue = this.editingLabel.value;
        } else {
          this.valueIsOther = true;
          this.customValue  = this.editingLabel.value;
        }
      } else if (this.knownNames.length === 0) {
        this.nameIsOther  = true;
        this.valueIsOther = true;
        this.$nextTick(() => { if (this.$refs.customNameInput) this.$refs.customNameInput.focus(); });
      }
    },

    onValueChange() {
      if (this.selectedValue === '__other__') {
        this.customValue   = this.editingLabel ? this.editingLabel.value : '';
        this.valueIsOther  = true;
        this.selectedValue = '';
        this.$nextTick(() => {
          if (this.$refs.customValueInput) {
            this.$refs.customValueInput.focus();
            this.$refs.customValueInput.select();
          }
        });
      }
    },

    async onNameChange() {
      if (this.selectedName === '__other__') {
        this.nameIsOther  = true;
        this.selectedName = '';
        this.valueIsOther = true;
        this.knownValues  = [];
        this.selectedValue = '';
        this.customValue   = '';
        this.$nextTick(() => { if (this.$refs.customNameInput) this.$refs.customNameInput.focus(); });
        return;
      }
      this.selectedValue = '';
      this.valueIsOther  = false;
      this.customValue   = '';
      await this.loadValues(this.selectedName);
    },

    async loadValues(name) {
      if (!name) return;
      this.loadingValues = true;
      try {
        const r = await fetch(`/api/v1/label-values?name=${encodeURIComponent(name)}`);
        const d = await r.json();
        this.knownValues = d.values || [];
      } catch { this.knownValues = []; }
      this.loadingValues = false;
      if (this.knownValues.length === 1) this.selectedValue = this.knownValues[0];
      if (this.knownValues.length === 0) this.valueIsOther = true;
    },

    async save() {
      const name  = this.effectiveName;
      const value = this.effectiveValue;
      if (!name || !value) return;
      this.saving = true;
      try {
        let resp;
        if (this.editingLabel) {
          resp = await fetch(`/api/v1/labels/${encodeURIComponent(this.editingLabel.labelid)}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
            body: JSON.stringify({ value }),
          });
        } else {
          resp = await fetch(`/api/v1/labels?photoid=${encodeURIComponent(this.photo.photoid)}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
            body: JSON.stringify({ name, value }),
          });
        }
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || `HTTP ${resp.status}`);
        }
        const saved = await resp.json();
        if (this.editingLabel) {
          const idx = this.photo.labels.findIndex(l => l.labelid === saved.labelid);
          if (idx !== -1) this.photo.labels[idx] = saved;
        } else {
          this.photo.labels.push(saved);
        }
        document.dispatchEvent(new CustomEvent('photoapp:close-label-modal'));
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Save failed: ${e.message}` }));
      }
      this.saving = false;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// titleEditor — inline edit for a photo's title.
// Takes `photo`; auth via global.
// ─────────────────────────────────────────────────────────────────────────────
function titleEditor(photo) {
  return {
    photo,
    editing: false,
    draft: '',
    original: '',

    startEdit() {
      this.original = this.photo.title.text;
      this.draft    = this.photo.title.text;
      this.editing  = true;
      this.$nextTick(() => {
        const el = this.$refs.titleInput;
        if (el) { el.focus(); el.select(); }
      });
    },

    cancel() {
      this.draft   = this.original;
      this.editing = false;
    },

    async save() {
      if (!this.editing) return;
      const newTitle = this.draft.trim();
      if (!newTitle || newTitle === this.original) {
        this.cancel();
        return;
      }
      this.editing = false;
      try {
        const resp = await fetch(
          `/api/v1/photo?photoid=${encodeURIComponent(this.photo.photoid)}`,
          {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
            body: JSON.stringify({ title: newTitle }),
          }
        );
        if (!resp.ok) {
          const err = await resp.json().catch(() => ({}));
          throw new Error(err.error || `HTTP ${resp.status}`);
        }
        this.photo.title.text = newTitle;
        this.original         = newTitle;
      } catch (e) {
        this.photo.title.text = this.original;
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: `Save failed: ${e.message}` }));
      }
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// userSwitcher — dropdown to impersonate other users (dev/admin feature).
// displayUsers filters out the current real user via window._currentUser.
// ─────────────────────────────────────────────────────────────────────────────
function userSwitcher() {
  return {
    open: false,
    users: [],
    loaded: false,

    get displayUsers() {
      const uid = window._currentUser && window._currentUser.userid;
      return uid ? this.users.filter(u => u.userid !== uid) : this.users;
    },

    toggle() {
      if (!this.loaded) {
        fetch('/auth/users').then(r => r.json()).then(d => {
          this.users = d.users || [];
          this.loaded = true;
        });
      }
      this.open = !this.open;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// photoApp — root component.
// ─────────────────────────────────────────────────────────────────────────────
function photoApp() {
  return {
    photo: null,
    loading: true,
    error: null,

    loggedInUser: null,
    authConfig: { googleEnabled: false, appleEnabled: false },

    testUser: null,

    searchQuery: '',

    thumbUrl(url, cssWidth) { return thumbUrl(url, cssWidth); },
    labelColorFor(name) { return labelColorFor(name); },
    avatarSrc(user) { return avatarSrc(user); },

    authHeaders() {
      if (this.loggedInUser) return {};
      return this.testUser ? { 'X-User-ID': this.testUser.userid } : {};
    },

    get currentUser() {
      return this.loggedInUser || this.testUser || null;
    },

    selectTestUser(user) {
      this.testUser = user;
      window._testUserID = user ? user.userid : null;
      window._currentUser = user;
      if (this.photo) this.loadPhoto(this.photo.photoid);
    },

    async logout() {
      await fetch('/auth/logout', { method: 'POST' });
      this.loggedInUser = null;
      window._testUserID = null;
      window._loggedIn = false;
      window._currentUser = null;
      if (this.photo) this.loadPhoto(this.photo.photoid);
    },

    labelModalData: null,
    userModal: false,

    toast: { visible: false, message: '', timer: null },

    activeLabelID: null,

    async init() {
      try {
        const [cfg, me] = await Promise.all([
          fetch('/auth/config').then(r => r.json()),
          fetch('/auth/me').then(r => r.json()),
        ]);
        this.authConfig = cfg;
        if (me.loggedIn) {
          this.loggedInUser = me;
          window._testUserID = me.userid;
          window._loggedIn = true;
          window._currentUser = me;
          // Notify avatarSettings (and any other listeners) that auth is ready.
          // photoapp:auth-success is only dispatched on login; this covers the
          // "already logged-in on page load" path where /auth/me returns a session.
          document.dispatchEvent(new CustomEvent('photoapp:auth-ready', { detail: me }));
        }
      } catch { /* non-fatal */ }

      const params = new URLSearchParams(window.location.search);
      const photoid = params.get('photoid') || 'random';
      const label   = params.get('label') || null;
      this.activeLabelID = label;
      await this.loadPhoto(photoid, label);
      document.addEventListener('photoapp:toast', (e) => this.showToast(e.detail));
      document.addEventListener('photoapp:close-label-modal', () => { Alpine.store('ui').labelModal = false; });
      document.addEventListener('photoapp:comment-deleted', (e) => {
        if (this.photo) this.photo.comments = this.photo.comments.filter(c => c.commentid !== e.detail);
      });
      document.addEventListener('photoapp:auth-success', (e) => {
        this.loggedInUser = e.detail;
        window._testUserID = e.detail.userid;
        window._loggedIn = true;
        window._currentUser = e.detail;
        if (this.photo) this.loadPhoto(this.photo.photoid);
      });
      document.addEventListener('photoapp:profile-image', (e) => {
        if (this.loggedInUser) this.loggedInUser = { ...this.loggedInUser, profileImage: e.detail };
      });
    },

    async loadPhoto(photoid, labelID) {
      if (labelID !== undefined) this.activeLabelID = labelID || null;
      this.loading = true;
      this.error = null;
      try {
        let url;
        if (photoid === 'random') {
          url = '/api/v1/photo?random=true';
        } else {
          url = `/api/v1/photo?photoid=${encodeURIComponent(photoid)}`;
          if (this.activeLabelID) url += `&label=${encodeURIComponent(this.activeLabelID)}`;
        }
        const resp = await fetch(url, { headers: this.authHeaders() });
        if (!resp.ok) throw new Error(`Photo not found (${resp.status})`);
        this.photo = await resp.json();
        const qs = new URLSearchParams({ photoid: this.photo.photoid });
        if (this.activeLabelID) qs.set('label', this.activeLabelID);
        window.history.replaceState(null, '', `?${qs}`);
      } catch (e) { this.error = e.message; }
      this.loading = false;
    },

    // ── Search ──────────────────────────────────────────────────────────────
    // Calls /api/v1/search, loads the top result as the main photo, and injects
    // the remaining results (up to 12) into the related photos sidebar.
    // navigatePhoto handles clicks on related-photo links.
    // Plain clicks load the photo in-page; modifier-key clicks (⌘, Ctrl, Shift)
    // and middle-clicks fall through so the browser opens a new tab/window.
    navigatePhoto(event, photoid) {
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.button === 1) return;
      event.preventDefault();
      this.loadPhoto(photoid);
    },

    async doSearch() {
      // Read directly from the DOM element to bypass any x-model sync lag on iOS.
      const inputEl = this.$refs && this.$refs.searchInput;
      const q = (inputEl ? inputEl.value : this.searchQuery).trim();
      if (!q) return;
      try {
        const resp = await fetch(`/api/v1/search?q=${encodeURIComponent(q)}`, {
          headers: this.authHeaders(),
        });
        if (!resp.ok) throw new Error(`Search failed (${resp.status})`);
        const data = await resp.json();
        const results = data.results || [];
        if (results.length === 0) {
          this.showToast('No photos found.');
          return;
        }
        // Save error state before loadPhoto so we can detect failure.
        const prevError = this.error;
        await this.loadPhoto(results[0].photoid);
        // loadPhoto catches its own errors into this.error; surface them as toasts.
        if (this.error && this.error !== prevError) {
          this.showToast(`Could not load photo: ${this.error}`);
          return;
        }
        // Replace the related sidebar with the other search results.
        // Use a fresh object to guarantee Alpine's reactivity picks up the change.
        if (this.photo && results.length > 1) {
          this.photo = {
            ...this.photo,
            related: results.slice(1).map(r => ({
              photoid:  r.photoid,
              imageurl: r.imageurl,
              clickurl: `/photo.html?photoid=${encodeURIComponent(r.photoid)}`,
              width:    r.width,
              height:   r.height,
            })),
          };
        }
        // Scroll the newly loaded photo into view.
        window.scrollTo({ top: 0, behavior: 'smooth' });
        // Preserve both photoid and query in the URL.
        const qs = new URLSearchParams({ photoid: this.photo.photoid, q });
        window.history.replaceState(null, '', `?${qs}`);
      } catch(e) {
        this.showToast(`Search error: ${e.message}`);
      }
    },

    showToast(message) {
      clearTimeout(this.toast.timer);
      this.toast.message = message;
      this.toast.visible = true;
      this.toast.timer = setTimeout(() => { this.toast.visible = false; }, 3500);
    },

    async toggleReaction(em) {
      if (!this.currentUser) { this.showToast('Select a user to react.'); return; }
      const uid = this.currentUser.userid;
      const alreadyReacted = em.users && em.users.some(u => u.id === uid);
      const method = alreadyReacted ? 'DELETE' : 'POST';
      try {
        const resp = await fetch(
          `/api/v1/emoji/react?photoid=${encodeURIComponent(this.photo.photoid)}&emojiid=${encodeURIComponent(em.emojiid)}`,
          { method, headers: this.authHeaders() }
        );
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || `HTTP ${resp.status}`);
        }
        if (alreadyReacted) {
          em.count--;
          em.users = (em.users || []).filter(u => u.id !== uid);
          if (em.count <= 0) this.photo.emojis = this.photo.emojis.filter(e => e.emojiid !== em.emojiid);
        } else {
          em.count++;
          const cu2 = this.currentUser;
          em.users = [...(em.users || []), { id: uid, name: (cu2 && cu2.username) || '', tn: (cu2 && cu2.profileImage) || null }];
        }
      } catch(e) {
        this.showToast(`Reaction failed: ${e.message}`);
      }
    },

    openLabelModal(label) {
      this.labelModalData = label;
      Alpine.store('ui').labelModal = true;
    },

    async deleteLabel(label) {
      if (!this.currentUser) { this.showToast('Select a user to delete labels.'); return; }
      try {
        const resp = await fetch(`/api/v1/labels/${encodeURIComponent(label.labelid)}`, {
          method: 'DELETE',
          headers: this.authHeaders(),
        });
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || `HTTP ${resp.status}`);
        }
        this.photo.labels = this.photo.labels.filter(l => l.labelid !== label.labelid);
      } catch(e) {
        this.showToast(`Delete failed: ${e.message}`);
      }
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// avatarSettings — profile settings panel: preset avatars + custom upload.
// Accesses loggedInUser via this.$parent (parent is photoApp).
// ─────────────────────────────────────────────────────────────────────────────
function avatarSettings() {
  return {
    saving:             false,
    uploading:          false,
    _avatarHash:        '',
    _profileImageSource: '',   // local URL of the user's real photo (upload / OAuth download)
    _provider:          '',    // 'local' | 'google' | 'apple'

    init() {
      // Sync reactive state from auth events so Alpine can track changes.
      // photoapp:auth-ready  — page load with existing session
      // photoapp:auth-success — login during session
      const sync = (e) => {
        if (!e.detail) return;
        if (e.detail.avatarHash)        this._avatarHash         = e.detail.avatarHash;
        if (e.detail.profileImageSource) this._profileImageSource = e.detail.profileImageSource;
        if (e.detail.provider)          this._provider           = e.detail.provider;
      };
      document.addEventListener('photoapp:auth-ready',   sync);
      document.addEventListener('photoapp:auth-success', sync);
    },

    get presets() {
      const h = this._avatarHash;
      return h ? Array.from({ length: 20 }, (_, i) => i === 0 ? h : h + i) : [];
    },

    async selectProfileImage() {
      if (this.saving || !this._profileImageSource) return;
      this.saving = true;
      const url = this._profileImageSource;
      try {
        const r = await fetch('/auth/profile', {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ profileImage: url }),
        });
        if (!r.ok) throw new Error('Save failed');
        if (window._currentUser) window._currentUser.profileImage = url;
        window._profileImage = url;
        document.dispatchEvent(new CustomEvent('photoapp:profile-image', { detail: url }));
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: e.message }));
      }
      this.saving = false;
    },

    async selectPreset(hash) {
      if (this.saving) return;
      this.saving = true;
      const url = '/avatars/' + hash;
      try {
        const r = await fetch('/auth/profile', {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ profileImage: url }),
        });
        if (!r.ok) throw new Error('Save failed');
        if (window._currentUser) window._currentUser.profileImage = url;
        window._profileImage = url;
        document.dispatchEvent(new CustomEvent('photoapp:profile-image', { detail: url }));
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: e.message }));
      }
      this.saving = false;
    },

    async uploadCustom(e) {
      const file = e.target.files && e.target.files[0];
      if (!file) return;
      this.uploading = true;
      try {
        const fd = new FormData();
        fd.append('image', file);
        const r = await fetch('/auth/profile/avatar', { method: 'POST', body: fd });
        if (!r.ok) {
          const d = await r.json().catch(() => ({}));
          throw new Error(d.error || 'Upload failed');
        }
        const d = await r.json();
        this._profileImageSource = d.profileImage;
        if (window._currentUser) {
          window._currentUser.profileImage       = d.profileImage;
          window._currentUser.profileImageSource = d.profileImage;
        }
        document.dispatchEvent(new CustomEvent('photoapp:profile-image', { detail: d.profileImage }));
      } catch(e) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: e.message }));
      }
      this.uploading = false;
      e.target.value = '';
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// authModal — login / register panel inside the settings popup.
// ─────────────────────────────────────────────────────────────────────────────
function authModal() {
  return {
    authTab:      'login',
    authEmail:    '',
    authUsername: '',
    authPassword: '',
    authError:    '',
    authLoading:  false,

    async doLogin() {
      this.authError = '';
      this.authLoading = true;
      try {
        const r = await fetch('/auth/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ email: this.authEmail, password: this.authPassword }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || 'Login failed');
        const me = await fetch('/auth/me').then(res => res.json());
        document.dispatchEvent(new CustomEvent('photoapp:auth-success', { detail: me }));
        this.authEmail = this.authPassword = '';
      } catch(e) { this.authError = e.message; }
      this.authLoading = false;
    },

    async doRegister() {
      this.authError = '';
      this.authLoading = true;
      try {
        const r = await fetch('/auth/register', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username: this.authUsername, email: this.authEmail, password: this.authPassword }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || 'Registration failed');
        const me = await fetch('/auth/me').then(res => res.json());
        document.dispatchEvent(new CustomEvent('photoapp:auth-success', { detail: me }));
        this.authEmail = this.authUsername = this.authPassword = '';
        Alpine.store('ui').settingsOpen = false;
      } catch(e) { this.authError = e.message; }
      this.authLoading = false;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// emojiHover — manages the hover-tooltip for emoji reaction chips.
// Extracted from the inline x-data on the emoji strip so we can define methods
// (the CSP evaluator cannot parse arrow functions in @mouseenter handlers).
// ─────────────────────────────────────────────────────────────────────────────
function emojiHover() {
  return {
    hoverEmojiId: null,
    hoverTimer:   null,

    hasReacted(em) {
      const cu = window._currentUser;
      return !!(cu && em.users && em.users.some(u => u.id === cu.userid));
    },

    reactionTitle(em) {
      return this.hasReacted(em) ? 'Click to remove reaction' : 'Click to react';
    },

    onEmojiEnter(em) {
      clearTimeout(this.hoverTimer);
      if (this.hoverEmojiId) {
        this.hoverEmojiId = em.emojiid;
      } else {
        this.hoverTimer = setTimeout(() => { this.hoverEmojiId = em.emojiid; }, 500);
      }
    },

    onEmojiLeave() {
      clearTimeout(this.hoverTimer);
      this.hoverTimer = setTimeout(() => { this.hoverEmojiId = null; }, 150);
    },

    stopHover() {
      clearTimeout(this.hoverTimer);
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// packRows — justified row-layout algorithm for the photo wall.
//
// Groups `photos` into rows.  Each row has an explicit height (px) and each
// photo carries a `flexGrow` weight so that CSS flex distributes widths
// proportionally without relying on pixel-perfect container measurement.
// This means horizontal layout is always correct regardless of when or how
// containerWidth is measured — only the row *height* depends on it.
//
// n (photos per row) is chosen from {2, 3, 4} subject to:
//   • n never repeats from the previous row.
//   • n=4 only when ≥2 photos in the candidate window are portrait (h > w)
//     OR all aspect ratios are "similar" (max/min < 1.5).
//   • The last partial row uses TARGET_ROW_H instead of filling the container.
//
// Returns an array of row objects:
//   { startIndex, height,
//     photos: [{ photoid, imageurl, width, height, flexGrow, displayWidth }] }
//
// flexGrow   — CSS flex-grow value; proportional to aspect-corrected width.
// displayWidth — best-guess pixel width used only as the imgproxy size hint;
//               the actual rendered width is determined by CSS flex.
// ─────────────────────────────────────────────────────────────────────────────
function packRows(photos, containerWidth) {
  if (!photos.length) return [];

  const GAP          = 4;   // px gap between photos (matches CSS gap)
  const MAX_ROW_H    = 400; // px — cap very-tall rows
  const TARGET_ROW_H = 240; // px — target height for partial last rows
  const MIN_ROW_H    = 80;  // px — floor so tiny images don't collapse

  const rows = [];
  let i = 0;
  let prevN = null;

  while (i < photos.length) {
    const remaining = photos.length - i;

    // ── Choose n ──────────────────────────────────────────────────────────────
    let n;
    const candidates = [2, 3, 4].filter(x => x !== prevN && x <= remaining);

    if (candidates.length === 0) {
      n = remaining; // forced (e.g. 1 photo left with prevN constraints)
    } else {
      const windowSize = Math.max(...candidates);
      const slice      = photos.slice(i, i + windowSize);
      const portraits  = slice.filter(p => (p.height || 1) > (p.width || 1)).length;
      const aspects    = slice.map(p => (p.width || 1) / (p.height || 1));
      const maxA       = Math.max(...aspects);
      const minA       = Math.min(...aspects);
      const similar    = (minA > 0) && (maxA / minA < 1.5);

      if (candidates.includes(4) && (portraits >= 2 || similar)) {
        n = 4;
      } else if (candidates.includes(3)) {
        n = 3;
      } else {
        n = candidates[0];
      }
    }

    const rowPhotos = photos.slice(i, i + n);
    const isPartial = rowPhotos.length < n;

    // ── Compute aspect-corrected (nominal) widths ──────────────────────────────
    // Scale every photo to the same height (maxNatH) so widths are comparable.
    const maxNatH = Math.max(...rowPhotos.map(p => p.height || 1));
    let nominalW = 0;
    const photoScales = rowPhotos.map(p => {
      const s = maxNatH / (p.height || 1);
      nominalW += (p.width || 1) * s;
      return s;
    });

    // ── Compute row height ────────────────────────────────────────────────────
    // rowHeight controls how tall the row appears.  It depends on containerWidth
    // but only affects the visual height, not whether photos overflow horizontally.
    let rowHeight = TARGET_ROW_H;
    if (containerWidth > 0) {
      const totalGapPx = GAP * (rowPhotos.length - 1);
      const rowScale   = (containerWidth - totalGapPx) / nominalW;
      rowHeight = Math.round(maxNatH * rowScale);
      if (isPartial) rowHeight = Math.min(rowHeight, TARGET_ROW_H);
      rowHeight = Math.min(rowHeight, MAX_ROW_H);
      rowHeight = Math.max(rowHeight, MIN_ROW_H);
    }

    // ── Build row ──────────────────────────────────────────────────────────────
    // flexGrow = aspect-corrected nominal width (un-rounded) → CSS distributes
    // the row's available space proportionally, always summing to 100% width.
    // displayWidth = pixel estimate for the imgproxy size hint (not used for layout).
    const finalScale = rowHeight / maxNatH;

    rows.push({
      startIndex: i,
      height:     rowHeight,
      photos: rowPhotos.map((p, idx) => {
        const scaledW = (p.width || 1) * photoScales[idx];
        return {
          ...p,
          flexGrow:      scaledW,                                   // CSS flex-grow
          displayHeight: rowHeight,
          displayWidth:  Math.max(80, Math.round(scaledW * finalScale)), // thumbUrl hint
        };
      }),
    });

    prevN = rowPhotos.length;
    i    += rowPhotos.length;
  }

  return rows;
}

// ─────────────────────────────────────────────────────────────────────────────
// wallApp — root Alpine component for the photo wall (index.html).
// Shares auth helpers and navbar state with photoApp but replaces the main
// content with a paginated, lazily-loaded justified photo grid.
// ─────────────────────────────────────────────────────────────────────────────
function wallApp() {
  return {
    // ── Auth / navbar state (mirrors photoApp) ────────────────────────────────
    loggedInUser: null,
    testUser:     null,
    authConfig:   { googleEnabled: false, appleEnabled: false },
    toast:        { visible: false, message: '', timer: null },

    get currentUser() { return this.loggedInUser || this.testUser || null; },

    thumbUrl(url, w)  { return thumbUrl(url, w); },
    avatarSrc(user)   { return avatarSrc(user);  },
    labelColorFor(n)  { return labelColorFor(n); },

    authHeaders() {
      if (this.loggedInUser) return {};
      return this.testUser ? { 'X-User-ID': this.testUser.userid } : {};
    },

    selectTestUser(user) {
      this.testUser           = user;
      window._testUserID      = user ? user.userid : null;
      window._currentUser     = user;
    },

    async logout() {
      await fetch('/auth/logout', { method: 'POST' });
      this.loggedInUser   = null;
      window._testUserID  = null;
      window._loggedIn    = false;
      window._currentUser = null;
    },

    showToast(message) {
      clearTimeout(this.toast.timer);
      this.toast.message = message;
      this.toast.visible = true;
      this.toast.timer   = setTimeout(() => { this.toast.visible = false; }, 3500);
    },

    // ── Wall state ────────────────────────────────────────────────────────────
    photos:         [],   // all loaded PhotoListItem objects
    rows:           [],   // packed row objects from packRows()
    loading:        false,
    hasMore:        true,
    offset:         0,
    limit:          40,
    containerWidth: 0,


    async init() {
      // Auth setup — same flow as photoApp.init.
      try {
        const [cfg, me] = await Promise.all([
          fetch('/auth/config').then(r => r.json()),
          fetch('/auth/me').then(r => r.json()),
        ]);
        this.authConfig = cfg;
        if (me.loggedIn) {
          this.loggedInUser       = me;
          window._testUserID      = me.userid;
          window._loggedIn        = true;
          window._currentUser     = me;
          document.dispatchEvent(new CustomEvent('photoapp:auth-ready', { detail: me }));
        }
      } catch { /* non-fatal */ }

      document.addEventListener('photoapp:toast',        e => this.showToast(e.detail));
      document.addEventListener('photoapp:auth-success', e => {
        this.loggedInUser       = e.detail;
        window._testUserID      = e.detail.userid;
        window._loggedIn        = true;
        window._currentUser     = e.detail;
      });
      document.addEventListener('photoapp:profile-image', e => {
        if (this.loggedInUser) this.loggedInUser = { ...this.loggedInUser, profileImage: e.detail };
      });

      // Wait one tick so Alpine has populated $refs, then measure container.
      await new Promise(resolve => this.$nextTick(resolve));

      const wall = this.$refs.wall;
      if (wall) {
        this.containerWidth = wall.clientWidth;
        new ResizeObserver(() => {
          const w = wall.clientWidth;
          if (w !== this.containerWidth) {
            this.containerWidth = w;
            this.rows = packRows(this.photos, this.containerWidth);
          }
        }).observe(wall);
      }

      // Infinite-scroll sentinel.
      const sentinel = this.$refs.sentinel;
      if (sentinel) {
        new IntersectionObserver(([entry]) => {
          if (entry.isIntersecting && this.hasMore && !this.loading) {
            this.loadMore();
          }
        }, { rootMargin: '600px' }).observe(sentinel);
      }

      await this.loadMore();
    },

    async loadMore() {
      if (this.loading || !this.hasMore) return;
      this.loading = true;
      try {
        const resp = await fetch(
          `/api/v1/photos?limit=${this.limit}&offset=${this.offset}`,
          { headers: this.authHeaders() }
        );
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const data = await resp.json();
        const batch = data.photos || [];
        this.photos.push(...batch);
        this.offset  += batch.length;
        this.hasMore  = this.offset < data.total;
        this.rows     = packRows(this.photos, this.containerWidth);
      } catch(e) {
        this.showToast(`Failed to load photos: ${e.message}`);
      }
      this.loading = false;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// galleriesNav — loads gallery list for the hamburger menu.
// Registered as a nested x-data component inside the hamburger dropdown.
// galleriesHref is a plain data property (not a getter) so Alpine tracks it.
// ─────────────────────────────────────────────────────────────────────────────
function galleriesNav() {
  return {
    galleries:     [],
    loaded:        false,
    galleriesHref: '#',

    async init() {
      try {
        const resp = await fetch('/api/v1/galleries?limit=100');
        if (resp.ok) {
          const data = await resp.json();
          this.galleries = data.galleries || [];
          if (this.galleries.length === 1) {
            this.galleriesHref = '/galleries.html?galleryid=' + this.galleries[0].galleryid;
          } else if (this.galleries.length > 1) {
            this.galleriesHref = '/galleries.html';
          }
        }
      } catch { /* non-fatal — menu degrades gracefully */ }
      this.loaded = true;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// galleriesApp — gallery list page + galleryid-based redirect.
// If ?galleryid= is in the URL, fetches that gallery and redirects to its first
// display.  If there is only one gallery total, also auto-redirects.
// ─────────────────────────────────────────────────────────────────────────────
function galleriesApp() {
  return {
    loggedInUser: null,
    authConfig:   { googleEnabled: false, appleEnabled: false },
    toast:        { visible: false, message: '', timer: null },

    galleries: [],
    loading:   true,
    error:     null,

    avatarSrc(user)  { return avatarSrc(user); },

    showToast(message) {
      clearTimeout(this.toast.timer);
      this.toast.message = message;
      this.toast.visible = true;
      this.toast.timer   = setTimeout(() => { this.toast.visible = false; }, 3500);
    },

    async _redirectToFirstDisplay(galleryid) {
      const resp = await fetch('/api/v1/galleries/' + encodeURIComponent(galleryid));
      if (!resp.ok) return false;
      const g = await resp.json();
      if (g.displays && g.displays.length > 0) {
        window.location.href = '/display.html?displayid=' + g.displays[0].displayid;
        return true;
      }
      return false;
    },

    async init() {
      try {
        const [cfg, me] = await Promise.all([
          fetch('/auth/config').then(r => r.json()),
          fetch('/auth/me').then(r => r.json()),
        ]);
        this.authConfig = cfg;
        if (me.loggedIn) {
          this.loggedInUser       = me;
          window._testUserID      = me.userid;
          window._loggedIn        = true;
          window._currentUser     = me;
          document.dispatchEvent(new CustomEvent('photoapp:auth-ready', { detail: me }));
        }
      } catch { /* non-fatal */ }

      document.addEventListener('photoapp:auth-success', e => {
        this.loggedInUser   = e.detail;
        window._testUserID  = e.detail.userid;
        window._loggedIn    = true;
        window._currentUser = e.detail;
      });
      document.addEventListener('photoapp:toast', e => this.showToast(e.detail));

      const params    = new URLSearchParams(window.location.search);
      const galleryid = params.get('galleryid');

      // ?galleryid= → redirect to first display
      if (galleryid) {
        try {
          const redirected = await this._redirectToFirstDisplay(galleryid);
          if (!redirected) {
            this.error   = 'This gallery has no displays yet.';
            this.loading = false;
          }
        } catch(e) {
          this.error   = 'Could not load gallery.';
          this.loading = false;
        }
        return;
      }

      // Load full gallery list
      try {
        const resp = await fetch('/api/v1/galleries?limit=100');
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const data = await resp.json();
        this.galleries = data.galleries || [];

        // Single gallery → redirect straight to its first display
        if (this.galleries.length === 1) {
          const redirected = await this._redirectToFirstDisplay(this.galleries[0].galleryid);
          if (redirected) return;
        }
      } catch(e) {
        this.error = e.message;
      }
      this.loading = false;
    },

    async navigateToGallery(galleryid) {
      try {
        const redirected = await this._redirectToFirstDisplay(galleryid);
        if (!redirected) this.showToast('This gallery has no displays yet.');
      } catch(e) {
        this.showToast('Could not load gallery.');
      }
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// Slot layout geometry — shared by templateAdminApp (preview pane) and
// displayApp/displayEditApp (actual viewer/editor). A template's
// `slot_positions` is an array of { x, y, w, h } in percent, one entry per
// slot, positioned absolutely within a 16:9 canvas — this is exactly what
// the Template Admin preview renders, and the display pages must render the
// same geometry or the "preview" is misleading.
// defaultSlotPositions(n) generates an even grid (used both as the starting
// layout for new templates and as a fallback if a template's slot_positions
// is missing or doesn't match the display's slot count).
// ─────────────────────────────────────────────────────────────────────────────
// Each slot's position also carries a `placard: { x, y }` — the top-left
// corner (percent, same 16:9 canvas coordinate space as x/y/w/h) where that
// slot's placard is anchored. It's a separate, independently-positioned box
// (not nested inside the slot's own x/y/w/h) since a placard's *size* is
// fixed at the gallery level (see normalizeGalleryPlacard()) and doesn't
// need to fit inside the photo's own slot bounds. The default below anchors
// it just beneath the slot as a reasonable starting point.
function defaultSlotPositions(n) {
  n = Math.max(1, n | 0);
  var cols  = n <= 1 ? 1 : n <= 2 ? 2 : n <= 3 ? 3 : n <= 4 ? 2 : n <= 6 ? 3 : 4;
  var rows  = Math.ceil(n / cols);
  var gap   = 2; // percent
  var cellW = (100 - gap * (cols - 1)) / cols;
  var cellH = (100 - gap * (rows - 1)) / rows;
  var positions = [];
  for (var i = 0; i < n; i++) {
    var col = i % cols, row = Math.floor(i / cols);
    var x = +(col * (cellW + gap)).toFixed(2);
    var y = +(row * (cellH + gap)).toFixed(2);
    positions.push({
      x: x, y: y, w: +cellW.toFixed(2), h: +cellH.toFixed(2),
      placard: { x: x, y: Math.min(96, +(y + cellH + 1).toFixed(2)) },
    });
  }
  return positions;
}

// normalizeSlotPositions(raw, count) — use the template's own slot_positions
// if it's a valid array matching the slot count, else fall back to an even
// grid. A mismatch (e.g. a display with more/fewer slots than the template
// currently defines) falls back rather than rendering a broken partial layout.
function normalizeSlotPositions(raw, count) {
  var arr = Array.isArray(raw) ? raw : [];
  if (count <= 0) return [];
  if (arr.length !== count) return defaultSlotPositions(count);
  var defaults = defaultSlotPositions(count);
  return arr.map(function (p, i) {
    var d       = defaults[i];
    var placard = p && p.placard;
    return {
      x: Number(p && p.x) || 0,
      y: Number(p && p.y) || 0,
      w: Number(p && p.w) || 0,
      h: Number(p && p.h) || 0,
      placard: {
        x: (placard && typeof placard.x === 'number') ? placard.x : d.placard.x,
        y: (placard && typeof placard.y === 'number') ? placard.y : d.placard.y,
      },
    };
  });
}

function slotBoxStyle(positions, index) {
  var p = (positions && positions[index]) || { x: 0, y: 0, w: 100, h: 100 };
  return 'position: absolute; left: ' + p.x + '%; top: ' + p.y + '%; width: ' + p.w + '%; height: ' + p.h + '%;';
}

// ─────────────────────────────────────────────────────────────────────────────
// Matte + frame presentation helpers — shared by displayApp and displayEditApp.
// A display template's `presentation` JSON (set in Template Admin) controls
// how each photo slot is bordered:
//   {
//     "matte": { "enabled": true,  "color": "#e8e3d5", "width": 16 },
//     "frame": { "enabled": false, "color": "#3d3424", "width": "5%" }
//   }
// A width may be a plain number (px) or a string like "5%" — a percentage
// of the photo's own rendered (fitted) width, so the matte/frame scales
// with however large the photo actually appears rather than staying a
// fixed pixel amount. matte.width may also be a per-side object like
// { "top": 8, "right": "4%", "bottom": "4%", "left": 8 } — px and percent
// can be mixed freely, per side. Missing sides default to 0. Either matte
// or frame can be turned off independently via "enabled". Absent/invalid
// presentation falls back to a plain matte in the theme's frame color and
// no outer frame.
//
// Percentages can't be resolved by CSS alone (a photo's rendered size isn't
// known until layout), so they're resolved in two places that must agree:
//   - computeFrameBoxSize() solves for the photo's fitted width analytically
//     (the overhead is now a linear function of that width, see below) and
//     exposes it as `contentW` on its result.
//   - frameOuterStyle()/matteInnerStyle() take that same `contentW` and use
//     it to turn any percentage sides into their final px value for the
//     actual CSS border/padding.
// Before the first JS layout pass measures anything, `contentW` is
// undefined and percentage sides resolve to 0 (invisible) for one tick,
// then snap to the correct size — the same brief fallback window
// photoFrameFallbackStyle() already has for the frame's own size.
// ─────────────────────────────────────────────────────────────────────────────
function normalizeSideWidths(width) {
  if (typeof width === 'number' || typeof width === 'string') {
    return { top: width, right: width, bottom: width, left: width };
  }
  var w = width || {};
  function pick(v) { return (typeof v === 'number' || typeof v === 'string') ? v : 0; }
  return { top: pick(w.top), right: pick(w.right), bottom: pick(w.bottom), left: pick(w.left) };
}

// Splits one width entry into a fixed px amount and a coefficient (fraction
// of contentW) — e.g. "5%" -> { fixed: 0, coef: 0.05 }, 16 -> { fixed: 16, coef: 0 }.
function splitWidthValue(v) {
  if (typeof v === 'string') {
    var m = v.trim().match(/^(-?[\d.]+)\s*%$/);
    if (m) return { fixed: 0, coef: parseFloat(m[1]) / 100 };
  }
  return { fixed: typeof v === 'number' ? v : 0, coef: 0 };
}

// Resolves one width entry to an actual px value given the photo's known
// rendered width (contentW). Plain numbers ignore contentW entirely.
function resolveWidthPx(v, contentW) {
  var s = splitWidthValue(v);
  return s.fixed + s.coef * (contentW || 0);
}

function frameOuterStyle(presentation, contentW) {
  var p     = presentation || {};
  var frame = Object.assign({ enabled: false, color: 'var(--board-border)', width: 8 }, p.frame || {});
  if (!frame.enabled) return 'border: none;';
  var px = resolveWidthPx(frame.width, contentW);
  return 'border: ' + px + 'px solid ' + frame.color + ';';
}

function matteInnerStyle(presentation, contentW) {
  var p     = presentation || {};
  var matte = Object.assign({ enabled: true, color: 'var(--frame-bg)', width: 16 }, p.matte || {});
  if (!matte.enabled) return 'background: none; padding: 0;';
  var w      = normalizeSideWidths(matte.width);
  var top    = resolveWidthPx(w.top,    contentW);
  var right  = resolveWidthPx(w.right,  contentW);
  var bottom = resolveWidthPx(w.bottom, contentW);
  var left   = resolveWidthPx(w.left,   contentW);
  return 'background: ' + matte.color + '; padding: ' + top + 'px ' + right + 'px ' + bottom + 'px ' + left + 'px;';
}

// ─────────────────────────────────────────────────────────────────────────────
// Photo alignment within its slot — also part of a template's `presentation`:
//   { "align": { "horizontal": "center", "vertical": "center" } }
// horizontal: "left" | "center" | "right"; vertical: "top" | "center" | "bottom".
// The framed photo (frame + matte + image) always keeps its own aspect
// ratio — it is never stretched to match the slot's shape — so whenever a
// slot's aspect ratio doesn't match the photo's, there's unfilled space on
// two opposite sides of the slot. `align` controls which edge that space
// collects against; the unfilled space itself always renders as the page
// background (no fill color of its own).
// ─────────────────────────────────────────────────────────────────────────────
function normalizeAlign(presentation) {
  var p = presentation || {};
  var a = p.align || {};
  var h = ['left', 'center', 'right'].indexOf(a.horizontal) !== -1 ? a.horizontal : 'center';
  var v = ['top', 'center', 'bottom'].indexOf(a.vertical)   !== -1 ? a.vertical   : 'center';
  return { horizontal: h, vertical: v };
}

var ALIGN_JUSTIFY = { left: 'flex-start', center: 'center', right: 'flex-end' };
var ALIGN_ITEMS    = { top:  'flex-start', center: 'center', bottom: 'flex-end' };

function photoAreaStyle(presentation) {
  var a = normalizeAlign(presentation);
  return 'display: flex; justify-content: ' + ALIGN_JUSTIFY[a.horizontal] + '; align-items: ' + ALIGN_ITEMS[a.vertical] + ';';
}

// The framed photo's box size — computed in actual pixels so the matte and
// frame widths are exactly what's configured on *every* side.
//
// A pure CSS approach (aspect-ratio: photoW/photoH on .photo-frame, with
// matte/frame as padding/border inside it) looks right at first, but isn't:
// setting the frame's aspect-ratio to the raw photo ratio, then subtracting
// the matte's fixed-px padding from that box, leaves a content area whose
// ratio no longer matches the photo — so object-fit:contain on the <img>
// letterboxes inside the matte on one axis, making the visual matte gap on
// that axis wider than the configured width even though the CSS padding
// itself is uniform. (E.g. a 20px matte around a photo can render as ~20px
// top/bottom but ~27px left/right, purely from that rounding.)
//
// computeFrameBoxSize fixes this by working backwards from the *measured*
// available space: it subtracts the matte/frame's exact overhead first,
// fits the photo's true aspect ratio into what's left, then adds the
// overhead back on — so the returned box, once padded/bordered by matte and
// frame via normal CSS, has the image filling its content area exactly with
// no internal letterboxing, and the matte is the same width on all sides.
//
// Percentage-based widths (see the comment above matteInnerStyle()) make
// the overhead itself depend on the photo's fitted width — matte/frame
// overhead is no longer a constant, it's `fixed + coef * contentW`. Both
// the width and height fit constraints are still linear in contentW though,
// so this solves them directly rather than iterating:
//   contentW + (overheadWFixed + overheadWCoef*contentW) <= availW
//   contentW/ratio + (overheadHFixed + overheadHCoef*contentW) <= availH
// (When nothing is percentage-based, overheadWCoef/overheadHCoef are 0 and
// this reduces to exactly the old fixed-overhead arithmetic.)
function computeFrameBoxSize(availW, availH, slot, presentation) {
  if (!(availW > 0) || !(availH > 0)) return null;

  var photo  = slot && slot.photo;
  var photoW = (photo && photo.width  > 0) ? photo.width  : 4;
  var photoH = (photo && photo.height > 0) ? photo.height : 3;
  var ratio  = photoW / photoH;

  var p     = presentation || {};
  var matte = Object.assign({ enabled: true,  width: 16 }, p.matte || {});
  var frame = Object.assign({ enabled: false, width: 8  }, p.frame || {});
  var mw    = matte.enabled ? normalizeSideWidths(matte.width) : { top: 0, right: 0, bottom: 0, left: 0 };

  var top    = splitWidthValue(mw.top);
  var right  = splitWidthValue(mw.right);
  var bottom = splitWidthValue(mw.bottom);
  var left   = splitWidthValue(mw.left);
  var side   = splitWidthValue(frame.enabled ? frame.width : 0);

  var overheadWFixed = left.fixed + right.fixed + 2 * side.fixed;
  var overheadWCoef  = left.coef  + right.coef  + 2 * side.coef;
  // Percentages are always relative to the photo's WIDTH (per spec), even
  // for top/bottom matte — so the height overhead uses the same
  // width-based coefficients, not separate height-relative ones.
  var overheadHFixed = top.fixed + bottom.fixed + 2 * side.fixed;
  var overheadHCoef  = top.coef  + bottom.coef  + 2 * side.coef;

  var maxW1 = (availW - overheadWFixed) / (1 + overheadWCoef);
  var maxW2 = (availH - overheadHFixed) / (1 / ratio + overheadHCoef);
  var contentW = Math.max(0, Math.min(maxW1, maxW2));
  if (!isFinite(contentW)) contentW = 0;
  var contentH = contentW / ratio;

  var overheadW = overheadWFixed + overheadWCoef * contentW;
  var overheadH = overheadHFixed + overheadHCoef * contentW;

  return {
    // Never exceed the measured available space even in extreme cases
    // (e.g. overhead alone already larger than the slot).
    w: Math.min(availW, contentW + overheadW),
    h: Math.min(availH, contentH + overheadH),
    // Exposed so frameOuterStyle()/matteInnerStyle() can resolve any
    // percentage-based sides to their exact final px value.
    contentW: contentW,
  };
}

// Before the JS layout pass has measured anything (first paint), fall back
// to the CSS-only aspect-ratio approximation so there's no flash of a
// collapsed/unsized box. This is corrected to the exact size on the very
// next tick via _layoutFrames() — see displayApp/displayEditApp.
function photoFrameFallbackStyle(slot) {
  var photo = slot && slot.photo;
  var w = (photo && photo.width  > 0) ? photo.width  : 4;
  var h = (photo && photo.height > 0) ? photo.height : 3;
  return 'aspect-ratio: ' + w + ' / ' + h + '; max-width: 100%; max-height: 100%;';
}

function photoFrameSizeStyle(size, slot) {
  if (size) return 'width: ' + size.w.toFixed(2) + 'px; height: ' + size.h.toFixed(2) + 'px;';
  return photoFrameFallbackStyle(slot);
}

// ─────────────────────────────────────────────────────────────────────────────
// Placard configuration — GALLERY-level (set once in Gallery Admin's Placard
// Settings, applies to every slot in that gallery for visual consistency).
// Stored as gallery.placard_defaults JSON:
//   {
//     "width": 20, "height": 8,             // percent of the 16:9 canvas
//     "background": "#faf7f0", "borderColor": "#d6ccb0",
//     "items": [
//       { "id": "photographer", "text": "Photographer: {Photographer}",
//         "xIn": 0.3, "yIn": 0.2,           // inches from the placard's OWN
//                                            // top-left corner (absolute —
//                                            // NOT a percent of the placard's
//                                            // size; see the note below)
//         "fontFamily": "'DM Serif Display', serif", "fontSize": 12,
//         "fontWeight": 400, "fontStyle": "normal", "color": "#3d3424",
//         "hideIfMissing": true }
//     ]
//   }
// An item's `xIn`/`yIn` is a fixed physical distance from the placard's own
// top-left corner — deliberately NOT a percent of the placard's width/height,
// so resizing the placard later (via Gallery Admin's Placard Settings) never
// reflows an item's position. An item can end up outside the (resized)
// placard's bounds this way — that's expected; the placard box clips its
// contents (overflow: hidden), so the item just becomes invisible until the
// placard is made large enough again or the item is dragged back in.
// Rendering still needs a *percent* to position via CSS, so it's computed at
// render time as xIn / placardWidthIn * 100 (see placardItemStyle()) — the
// percent is a render-time detail, not what's stored.
// An item's `text` may reference photo labels via `{LabelName}` — resolved
// against the assigned photo's Labels (see resolvePlacardItemText()). If any
// referenced label is missing and hideIfMissing is true, the whole item is
// skipped for that slot; otherwise the token resolves to an empty string.
// Each slot's *position* on the canvas (independent of its photo's slot box)
// comes from the template's slot_positions[i].placard — see
// defaultSlotPositions() above. A slot's rendered items can be overridden
// per-item via display_slots.placard.overrides (see placardItemsFor()),
// e.g. to correct one photo's caption without touching its labels.
// This replaces the older template-level presentation.placard
// (position/fields) system entirely.
// ─────────────────────────────────────────────────────────────────────────────
// Default position (in inches, from the placard's own top-left corner) for
// the Nth item (0-indexed) added with no explicit xIn/yIn — wraps into a new
// column every 4 rows so new items always start out visible inside the
// *current* placard bounds no matter how many already exist, with a small
// inset from each edge. Falls back to the module-level default placard size
// if the actual current size isn't known yet (e.g. normalizing stored data
// before any editor session has opened it).
function defaultPlacardItemPosition(n, placardWidthIn, placardHeightIn) {
  var w = placardWidthIn  > 0 ? placardWidthIn  : DEFAULT_PLACARD_WIDTH_IN;
  var h = placardHeightIn > 0 ? placardHeightIn : DEFAULT_PLACARD_HEIGHT_IN;
  var rows = 4, cols = 3;
  var row  = n % rows;
  var col  = Math.floor(n / rows) % cols;
  var marginX = w * 0.08;
  var marginY = h * 0.08;
  var stepX = cols > 1 ? (w - 2 * marginX) / cols : 0;
  var stepY = rows > 1 ? (h - 2 * marginY) / rows : 0;
  return {
    xIn: Math.round((marginX + col * stepX) * 100) / 100,
    yIn: Math.round((marginY + row * stepY) * 100) / 100,
  };
}

// ── Physical units ──────────────────────────────────────────────────────────
// widthIn/heightIn (the placard's own physical size, in inches) are the
// canonical stored fields — same convention as items' xIn/yIn, and what the
// Gallery Admin visual editor and the raw-JSON editor both show, so the two
// views always agree. `boardWidthIn` is the physical width, in inches, that
// the whole 16:9 canvas is meant to represent (e.g. "this gallery's display
// is a 48 inch wide board") — a conversion reference the editor UI uses; it
// has no other effect on rendering. `unit` remembers which unit (in/cm) the
// editor was last shown in. Rendering still needs percent-of-canvas (CSS
// can't use inches directly against a canvas that scales to any screen
// size), so percent is derived at normalize time via:
//   width%  = (widthIn  / boardWidthIn)          * 100
//   height% = (heightIn / (boardWidthIn * 9/16)) * 100
// Older saved galleries (from before this field existed) only have percent
// width/height; normalizeGalleryPlacard() falls back to converting those
// into widthIn/heightIn so they still load correctly.
var CM_PER_IN = 2.54;
var DEFAULT_BOARD_WIDTH_IN   = 48;   // a reasonably-sized display board
var DEFAULT_PLACARD_WIDTH_IN = 4;    // a small caption card
var DEFAULT_PLACARD_HEIGHT_IN = 1.5;

function inToCm(v) { return v * CM_PER_IN; }
function cmToIn(v) { return v / CM_PER_IN; }
function round2(v) { return Math.round(v * 100) / 100; }

// The placard's own physical size in inches. Normalized gallery objects
// already carry widthIn/heightIn directly (see normalizeGalleryPlacard), so
// this just reads them back out — kept as a small helper since several call
// sites want the pair as a unit.
function placardPhysicalSize(g) {
  return { widthIn: g.widthIn, heightIn: g.heightIn };
}

function normalizeGalleryPlacard(raw) {
  var p = raw || {};
  var boardWidthIn  = typeof p.boardWidthIn === 'number' && p.boardWidthIn > 0 ? p.boardWidthIn : DEFAULT_BOARD_WIDTH_IN;
  var boardHeightIn = boardWidthIn * 9 / 16;
  // widthIn/heightIn are canonical; width/height (percent) are only read
  // here as a fallback for galleries saved before physical units existed.
  var widthIn, heightIn;
  if (typeof p.widthIn === 'number' && p.widthIn > 0) {
    widthIn = p.widthIn;
  } else if (typeof p.width === 'number') {
    widthIn = (p.width / 100) * boardWidthIn;
  } else {
    widthIn = DEFAULT_PLACARD_WIDTH_IN;
  }
  if (typeof p.heightIn === 'number' && p.heightIn > 0) {
    heightIn = p.heightIn;
  } else if (typeof p.height === 'number') {
    heightIn = (p.height / 100) * boardHeightIn;
  } else {
    heightIn = DEFAULT_PLACARD_HEIGHT_IN;
  }
  var width  = (widthIn  / boardWidthIn)  * 100;
  var height = (heightIn / boardHeightIn) * 100;
  return {
    widthIn:     widthIn,
    heightIn:    heightIn,
    width:       width,   // derived percent-of-canvas, for CSS rendering only
    height:      height,  // derived percent-of-canvas, for CSS rendering only
    boardWidthIn: boardWidthIn,
    unit:        p.unit === 'cm' ? 'cm' : 'in',
    background:  p.background  || '#faf7f0',
    borderColor: p.borderColor || '#d6ccb0',
    items: Array.isArray(p.items) ? p.items.map(function (it, idx) {
      var pos = defaultPlacardItemPosition(idx, widthIn, heightIn);
      return {
        id:            it.id || ('item-' + idx),
        text:          it.text || '',
        xIn:           typeof it.xIn === 'number' ? it.xIn : pos.xIn,
        yIn:           typeof it.yIn === 'number' ? it.yIn : pos.yIn,
        fontFamily:    it.fontFamily || "'DM Sans', sans-serif",
        fontSize:      typeof it.fontSize === 'number' ? it.fontSize : 12,
        fontWeight:    it.fontWeight || 400,
        fontStyle:     it.fontStyle  || 'normal',
        color:         it.color      || '#3d3424',
        hideIfMissing: it.hideIfMissing !== false,
      };
    }) : [],
  };
}

function typographyStyle(typo) {
  var t = typo || {};
  var css = '';
  if (t.fontFamily)             css += 'font-family: ' + t.fontFamily + ';';
  if (t.fontSize != null)       css += 'font-size: ' + t.fontSize + 'px;';
  if (t.fontWeight)             css += 'font-weight: ' + t.fontWeight + ';';
  if (t.fontStyle)              css += 'font-style: ' + t.fontStyle + ';';
  if (t.color)                  css += 'color: ' + t.color + ';';
  if (t.textAlign)              css += 'text-align: ' + t.textAlign + ';';
  if (t.textTransform)          css += 'text-transform: ' + t.textTransform + ';';
  if (t.letterSpacing != null)  css += 'letter-spacing: ' + t.letterSpacing + 'px;';
  if (t.lineHeight != null)     css += 'line-height: ' + t.lineHeight + ';';
  return css;
}

// Substitutes {LabelName} tokens in a placard item's text template against
// a photo's labels ([{ name, value }, ...]). Returns { text, missing } —
// missing is true if any referenced token had no matching label, so callers
// can honor hideIfMissing.
function resolvePlacardItemText(template, labels) {
  var missing = false;
  var text = String(template || '').replace(/\{([^{}]+)\}/g, function (m, name) {
    var lbl = (labels || []).filter(function (l) { return l && l.name === name; })[0];
    if (!lbl) { missing = true; return ''; }
    return lbl.value;
  });
  return { text: text, missing: missing };
}

// The placard box's own position + appearance for one slot — position comes
// from the template (slotPos.placard), size/background from the gallery.
function placardBoxStyle(gallery, slotPos) {
  var g   = normalizeGalleryPlacard(gallery && gallery.placard_defaults);
  var pos = (slotPos && slotPos.placard) || { x: 0, y: 0 };
  return 'position: absolute; left: ' + pos.x + '%; top: ' + pos.y + '%; width: ' + g.width + '%; height: ' + g.height + '%; background: ' + g.background + '; border: 1px solid ' + g.borderColor + ';';
}

// Converts an item's absolute inches-from-top-left position into the
// percent CSS actually needs, against the placard's *current* physical
// size — this is where "fixed physical position, not repositioned when the
// placard is resized" actually happens: the stored xIn/yIn never change on
// a resize, only this computed percent does (see the comment above
// defaultPlacardItemPosition() for why an item can end up rendered outside
// the box, and clipped, after a resize).
function placardItemStyle(item, placardWidthIn, placardHeightIn) {
  var xPct = placardWidthIn  > 0 ? (item.xIn / placardWidthIn)  * 100 : 0;
  var yPct = placardHeightIn > 0 ? (item.yIn / placardHeightIn) * 100 : 0;
  return 'position: absolute; left: ' + xPct + '%; top: ' + yPct + '%;' + typographyStyle(item);
}

// Resolved { id, text, style } list for one slot: gallery items with label
// substitution applied, a per-slot text override (display_slots.placard.
// overrides, keyed by item id) taking precedence when set, and items hidden
// per hideIfMissing when their substitution can't be resolved and there's no
// override.
function placardItemsFor(gallery, slot) {
  var g         = normalizeGalleryPlacard(gallery && gallery.placard_defaults);
  var size      = placardPhysicalSize(g);
  var overrides = (slot && slot.placard && slot.placard.overrides) || {};
  var labels    = (slot && slot.photo && slot.photo.labels) || [];
  var out = [];
  g.items.forEach(function (item) {
    var ov = overrides[item.id];
    var text;
    if (typeof ov === 'string' && ov !== '') {
      text = ov;
    } else {
      var r = resolvePlacardItemText(item.text, labels);
      if (r.missing && item.hideIfMissing) return;
      text = r.text;
    }
    if (!text) return;
    out.push({ id: item.id, text: text, style: placardItemStyle(item, size.widthIn, size.heightIn) });
  });
  return out;
}

// ─────────────────────────────────────────────────────────────────────────────
// displayApp — museum exhibit board viewer. Read-only, public-facing.
// URL: /display.html?displayid=<uuid>
// Fetches display detail (→ galleryid), then gallery detail (→ ordered display
// list for prev/next nav).  prevDisplayId/nextDisplayId are plain data props
// so Alpine can track them without getters.
// No auth/editing here by design — that lives in displayEditApp (below),
// which display-edit.html uses instead.
// ─────────────────────────────────────────────────────────────────────────────
function displayApp() {
  return {
    display:         null,
    gallery:         null,
    galleryDisplays: [],
    currentIndex:    -1,
    loading:         true,
    error:           null,

    // Plain data properties for prev/next (updated after load)
    prevDisplayId: null,
    nextDisplayId: null,
    // Resolved slot geometry (one { x, y, w, h } per slot, percent-based —
    // see slotBoxStyle() near the top of this file). Computed in init() from
    // the template's slot_positions, falling back to an even grid.
    slotPositions: [],
    // Exact pixel { w, h } for each slot's framed-photo box, measured from
    // the actual rendered .photo-area size — see computeFrameBoxSize() near
    // the top of this file for why this can't just be done with CSS
    // aspect-ratio. null entries (before the first measurement pass) fall
    // back to the CSS approximation via photoFrameSizeStyle().
    frameSizes: [],

    thumbUrl(url, w) { return thumbUrl(url, w); },
    // i is needed (not just the presentation) so percentage-based matte/frame
    // widths can resolve against this specific slot's measured contentW —
    // see the comment above matteInnerStyle() in the shared helpers above.
    frameOuterStyle(i) {
      const presentation = this.display && this.display.template && this.display.template.presentation;
      const size = this.frameSizes[i];
      return frameOuterStyle(presentation, size && size.contentW);
    },
    matteInnerStyle(i) {
      const presentation = this.display && this.display.template && this.display.template.presentation;
      const size = this.frameSizes[i];
      return matteInnerStyle(presentation, size && size.contentW);
    },
    slotBoxStyle(i) { return slotBoxStyle(this.slotPositions, i); },
    photoAreaStyle() { return photoAreaStyle(this.display && this.display.template && this.display.template.presentation); },
    photoFrameSizeStyle(i, slot) { return photoFrameSizeStyle(this.frameSizes[i], slot); },
    // Gallery-level placard box (position from the template, size/appearance
    // + item content from the gallery) — see the placard helper block above.
    placardBoxStyle(i) { return placardBoxStyle(this.gallery, this.slotPositions[i]); },
    placardItemsFor(slot) { return placardItemsFor(this.gallery, slot); },

    // Measures each slot's rendered .photo-area and computes its exact
    // frame box size (see computeFrameBoxSize()). Re-run whenever the grid
    // resizes (ResizeObserver, set up in init()) or the display data changes.
    layoutFrames() {
      const grid = this.$refs.grid;
      if (!grid) return;
      const areas       = grid.querySelectorAll('.photo-area');
      const slots        = (this.display && this.display.slots) || [];
      const presentation = this.display && this.display.template && this.display.template.presentation;
      const next = [];
      for (let i = 0; i < areas.length; i++) {
        next.push(computeFrameBoxSize(areas[i].clientWidth, areas[i].clientHeight, slots[i], presentation));
      }
      this.frameSizes = next;
    },

    goToPrev() { if (this.prevDisplayId) window.location.href = '/display.html?displayid=' + this.prevDisplayId; },
    goToNext() { if (this.nextDisplayId) window.location.href = '/display.html?displayid=' + this.nextDisplayId; },

    async init() {
      const params    = new URLSearchParams(window.location.search);
      const displayid = params.get('displayid');
      if (!displayid) {
        this.error   = 'No display specified.';
        this.loading = false;
        return;
      }

      try {
        // Fetch display (returns galleryid)
        const dResp = await fetch('/api/v1/displays/' + encodeURIComponent(displayid));
        if (!dResp.ok) throw new Error('Display not found (' + dResp.status + ')');
        this.display = await dResp.json();

        // Fetch gallery to get title and ordered display list
        const gResp = await fetch('/api/v1/galleries/' + encodeURIComponent(this.display.galleryid));
        if (gResp.ok) {
          this.gallery        = await gResp.json();
          this.galleryDisplays = this.gallery.displays || [];
          this.currentIndex    = this.galleryDisplays.findIndex(d => d.displayid === displayid);
          if (this.currentIndex > 0) {
            this.prevDisplayId = this.galleryDisplays[this.currentIndex - 1].displayid;
          }
          if (this.currentIndex >= 0 && this.currentIndex < this.galleryDisplays.length - 1) {
            this.nextDisplayId = this.galleryDisplays[this.currentIndex + 1].displayid;
          }
        }

        // Resolve slot geometry from the template's slot_positions (falls
        // back to an even grid if missing or mismatched with slot count).
        const n = this.display.slots ? this.display.slots.length : 0;
        const rawPositions = this.display.template && this.display.template.slot_positions;
        this.slotPositions = normalizeSlotPositions(rawPositions, n);
      } catch(e) {
        this.error = e.message;
      }
      this.loading = false;

      // Wait for the grid to actually render, then measure it and set up a
      // ResizeObserver so frame sizes stay exact across viewport/layout
      // changes (font loading reflowing a placard's height, window resize).
      await new Promise(resolve => this.$nextTick(resolve));
      const grid = this.$refs.grid;
      if (grid) {
        this.layoutFrames();
        new ResizeObserver(() => this.layoutFrames()).observe(grid);
      }
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// displayEditApp — museum exhibit board editor.
// URL: /display-edit.html?displayid=<uuid>
// Same load/nav logic as displayApp (viewer), plus auth state and the photo
// picker: a search-driven modal (opened per-slot, shown to logged-in users)
// that assigns a photo to a display slot via PATCH. Kept as a separate
// component (rather than a mode flag on displayApp) so the public viewer
// never carries editing code or auth calls it doesn't need.
// ─────────────────────────────────────────────────────────────────────────────
function displayEditApp() {
  return {
    loggedInUser: null,
    authConfig:   { googleEnabled: false, appleEnabled: false },
    toast:        { visible: false, message: '', timer: null },

    display:         null,
    gallery:         null,
    galleryDisplays: [],
    currentIndex:    -1,
    loading:         true,
    error:           null,

    // Plain data properties for prev/next (updated after load)
    prevDisplayId: null,
    nextDisplayId: null,
    // Resolved slot geometry (one { x, y, w, h } per slot, percent-based —
    // see slotBoxStyle() near the top of this file). Computed in init() from
    // the template's slot_positions, falling back to an even grid.
    slotPositions: [],
    // Exact pixel { w, h } for each slot's framed-photo box, measured from
    // the actual rendered .photo-area size — see computeFrameBoxSize() near
    // the top of this file for why this can't just be done with CSS
    // aspect-ratio. null entries (before the first measurement pass) fall
    // back to the CSS approximation via photoFrameSizeStyle().
    frameSizes: [],

    // Layout guides — a reference overlay showing the 16:9 canvas boundary
    // and each slot's exact bounding box (from slot_positions), so a
    // template author can see the geometry the public view page assumes
    // while editing. Edit-only; display.html never shows this.
    showGuides: true,

    // Photo picker (search + assign a photo to a slot)
    pickerOpen:       false,
    pickerSlotIndex:  null,
    pickerQuery:      '',
    pickerResults:    [],
    pickerLoading:    false,
    pickerError:      '',
    pickerSaving:     false,
    pickerDebounce:   null,

    thumbUrl(url, w) { return thumbUrl(url, w); },
    // i is needed (not just the presentation) so percentage-based matte/frame
    // widths can resolve against this specific slot's measured contentW —
    // see the comment above matteInnerStyle() in the shared helpers above.
    frameOuterStyle(i) {
      const presentation = this.display && this.display.template && this.display.template.presentation;
      const size = this.frameSizes[i];
      return frameOuterStyle(presentation, size && size.contentW);
    },
    matteInnerStyle(i) {
      const presentation = this.display && this.display.template && this.display.template.presentation;
      const size = this.frameSizes[i];
      return matteInnerStyle(presentation, size && size.contentW);
    },
    slotBoxStyle(i) { return slotBoxStyle(this.slotPositions, i); },
    photoAreaStyle() { return photoAreaStyle(this.display && this.display.template && this.display.template.presentation); },
    photoFrameSizeStyle(i, slot) { return photoFrameSizeStyle(this.frameSizes[i], slot); },
    // Gallery-level placard box (position from the template, size/appearance
    // + item content from the gallery) — see the placard helper block above.
    placardBoxStyle(i) { return placardBoxStyle(this.gallery, this.slotPositions[i]); },
    placardItemsFor(slot) { return placardItemsFor(this.gallery, slot); },
    avatarSrc(user)  { return avatarSrc(user);  },

    // Label text for a slot's guide overlay — the raw x/y/w/h (percent) from
    // slot_positions, so it's obvious this is the template's own geometry
    // and not something derived from the photo.
    slotGuideLabel(i) {
      const p = this.slotPositions[i];
      if (!p) return '';
      return 'x:' + p.x + ' y:' + p.y + '  ' + p.w + '×' + p.h;
    },

    // Measures each slot's rendered .photo-area and computes its exact
    // frame box size (see computeFrameBoxSize()). Re-run whenever the grid
    // resizes (ResizeObserver, set up in init()) or the display data changes
    // (e.g. after saveSlotPhoto() assigns a photo with a different aspect
    // ratio, which the grid's own size won't necessarily change to reflect).
    layoutFrames() {
      const grid = this.$refs.grid;
      if (!grid) return;
      const areas       = grid.querySelectorAll('.photo-area');
      const slots        = (this.display && this.display.slots) || [];
      const presentation = this.display && this.display.template && this.display.template.presentation;
      const next = [];
      for (let i = 0; i < areas.length; i++) {
        next.push(computeFrameBoxSize(areas[i].clientWidth, areas[i].clientHeight, slots[i], presentation));
      }
      this.frameSizes = next;
    },

    showToast(message) {
      clearTimeout(this.toast.timer);
      this.toast.message = message;
      this.toast.visible = true;
      this.toast.timer   = setTimeout(() => { this.toast.visible = false; }, 3500);
    },

    goToPrev() { if (this.prevDisplayId) window.location.href = '/display-edit.html?displayid=' + this.prevDisplayId; },
    goToNext() { if (this.nextDisplayId) window.location.href = '/display-edit.html?displayid=' + this.nextDisplayId; },

    async init() {
      try {
        const [cfg, me] = await Promise.all([
          fetch('/auth/config').then(r => r.json()),
          fetch('/auth/me').then(r => r.json()),
        ]);
        this.authConfig = cfg;
        if (me.loggedIn) {
          this.loggedInUser       = me;
          window._testUserID      = me.userid;
          window._loggedIn        = true;
          window._currentUser     = me;
          document.dispatchEvent(new CustomEvent('photoapp:auth-ready', { detail: me }));
        }
      } catch { /* non-fatal */ }

      document.addEventListener('photoapp:auth-success', e => {
        this.loggedInUser   = e.detail;
        window._testUserID  = e.detail.userid;
        window._loggedIn    = true;
        window._currentUser = e.detail;
      });
      document.addEventListener('photoapp:toast', e => this.showToast(e.detail));

      const params    = new URLSearchParams(window.location.search);
      const displayid = params.get('displayid');
      if (!displayid) {
        this.error   = 'No display specified.';
        this.loading = false;
        return;
      }

      try {
        // Fetch display (returns galleryid)
        const dResp = await fetch('/api/v1/displays/' + encodeURIComponent(displayid));
        if (!dResp.ok) throw new Error('Display not found (' + dResp.status + ')');
        this.display = await dResp.json();

        // Fetch gallery to get title and ordered display list
        const gResp = await fetch('/api/v1/galleries/' + encodeURIComponent(this.display.galleryid));
        if (gResp.ok) {
          this.gallery        = await gResp.json();
          this.galleryDisplays = this.gallery.displays || [];
          this.currentIndex    = this.galleryDisplays.findIndex(d => d.displayid === displayid);
          if (this.currentIndex > 0) {
            this.prevDisplayId = this.galleryDisplays[this.currentIndex - 1].displayid;
          }
          if (this.currentIndex >= 0 && this.currentIndex < this.galleryDisplays.length - 1) {
            this.nextDisplayId = this.galleryDisplays[this.currentIndex + 1].displayid;
          }
        }

        // Resolve slot geometry from the template's slot_positions (falls
        // back to an even grid if missing or mismatched with slot count).
        const n = this.display.slots ? this.display.slots.length : 0;
        const rawPositions = this.display.template && this.display.template.slot_positions;
        this.slotPositions = normalizeSlotPositions(rawPositions, n);
      } catch(e) {
        this.error = e.message;
      }
      this.loading = false;

      // Wait for the grid to actually render, then measure it and set up a
      // ResizeObserver so frame sizes stay exact across viewport/layout
      // changes (font loading reflowing a placard's height, window resize).
      await new Promise(resolve => this.$nextTick(resolve));
      const grid = this.$refs.grid;
      if (grid) {
        this.layoutFrames();
        new ResizeObserver(() => this.layoutFrames()).observe(grid);
      }
    },

    // ── Photo picker ──────────────────────────────────────────────────────────

    openPicker(slotIndex) {
      this.pickerSlotIndex = slotIndex;
      this.pickerQuery     = '';
      this.pickerResults   = [];
      this.pickerError     = '';
      this.pickerOpen      = true;
    },

    closePicker() {
      clearTimeout(this.pickerDebounce);
      this.pickerOpen      = false;
      this.pickerSlotIndex = null;
    },

    onPickerInput() {
      clearTimeout(this.pickerDebounce);
      const q = this.pickerQuery.trim();
      if (!q) {
        this.pickerResults = [];
        this.pickerLoading = false;
        return;
      }
      this.pickerLoading = true;
      this.pickerDebounce = setTimeout(() => this.runPickerSearch(q), 300);
    },

    async runPickerSearch(q) {
      this.pickerError = '';
      try {
        const resp = await fetch('/api/v1/search?q=' + encodeURIComponent(q));
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const data = await resp.json();
        // Ignore stale responses if the query has since changed.
        if (this.pickerQuery.trim() === q) {
          this.pickerResults = data.results || [];
        }
      } catch(e) {
        if (this.pickerQuery.trim() === q) this.pickerError = e.message;
      }
      if (this.pickerQuery.trim() === q) this.pickerLoading = false;
    },

    // The slot currently open in the picker, straight from the loaded display.
    currentPickerSlot() {
      if (!this.display || !this.display.slots || this.pickerSlotIndex === null) return null;
      return this.display.slots.find(s => s.slot_index === this.pickerSlotIndex) || null;
    },

    selectPickerPhoto(photo) {
      this.saveSlotPhoto(photo.photoid);
    },

    clearPickerPhoto() {
      this.saveSlotPhoto('');
    },

    // Persists a slot's photo. The API upserts rich_text/placard from whatever
    // is in the request, so both must be resent as-is or they'll be wiped.
    async saveSlotPhoto(photoid) {
      const slot = this.currentPickerSlot();
      if (!slot || this.pickerSaving) return;
      this.pickerSaving = true;
      try {
        const resp = await fetch('/api/v1/displays/' + encodeURIComponent(this.display.displayid), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({
            slots: [{
              slot_index: slot.slot_index,
              photoid:    photoid,
              rich_text:  slot.rich_text || '',
              placard:    slot.placard !== undefined ? slot.placard : null,
            }],
          }),
        });
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || 'HTTP ' + resp.status);
        }
        this.display = await resp.json();
        this.showToast(photoid ? 'Photo updated.' : 'Photo removed.');
        this.closePicker();
        // The new photo's aspect ratio may differ even if the slot's own
        // box size on screen hasn't changed, so re-measure explicitly
        // rather than relying solely on the ResizeObserver.
        await new Promise(resolve => this.$nextTick(resolve));
        this.layoutFrames();
      } catch(e) {
        this.showToast('Failed to update photo: ' + e.message);
      }
      this.pickerSaving = false;
    },

    // ── Placard caption overrides ────────────────────────────────────────────
    // Per-slot text overrides for the gallery's placard items (e.g. fixing
    // one photo's caption without touching its labels). Stored in
    // display_slots.placard.overrides, keyed by item id.
    captionModalOpen:  false,
    captionSlotIndex:  null,
    captionDraft:      {},
    captionSaving:     false,

    // The gallery's configured placard items (id/text/typography), for both
    // the editor labels and to know which override keys to save.
    galleryPlacardItems() {
      return normalizeGalleryPlacard(this.gallery && this.gallery.placard_defaults).items;
    },

    currentCaptionSlot() {
      if (!this.display || !this.display.slots || this.captionSlotIndex === null) return null;
      return this.display.slots.find(s => s.slot_index === this.captionSlotIndex) || null;
    },

    openCaptionEditor(slotIndex) {
      this.captionSlotIndex = slotIndex;
      const slot      = this.display.slots.find(s => s.slot_index === slotIndex);
      const overrides = (slot && slot.placard && slot.placard.overrides) || {};
      const draft = {};
      this.galleryPlacardItems().forEach(item => { draft[item.id] = overrides[item.id] || ''; });
      this.captionDraft   = draft;
      this.captionModalOpen = true;
    },

    closeCaptionEditor() {
      this.captionModalOpen = false;
      this.captionSlotIndex = null;
    },

    // The auto-resolved value for one item on the currently-open slot, shown
    // as the input's placeholder so it's clear what an override replaces.
    resolvedCaptionPlaceholder(item) {
      const slot   = this.currentCaptionSlot();
      const labels = (slot && slot.photo && slot.photo.labels) || [];
      const r = resolvePlacardItemText(item.text, labels);
      return r.missing ? '(label missing — item hidden unless overridden)' : (r.text || '(empty)');
    },

    async saveCaptionOverrides() {
      const slot = this.currentCaptionSlot();
      if (!slot || this.captionSaving) return;
      this.captionSaving = true;
      const overrides = {};
      Object.keys(this.captionDraft).forEach(id => {
        const v = (this.captionDraft[id] || '').trim();
        if (v) overrides[id] = v;
      });
      try {
        const resp = await fetch('/api/v1/displays/' + encodeURIComponent(this.display.displayid), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({
            slots: [{
              slot_index: slot.slot_index,
              photoid:    slot.photo ? slot.photo.photoid : '',
              rich_text:  slot.rich_text || '',
              placard:    Object.keys(overrides).length ? { overrides: overrides } : null,
            }],
          }),
        });
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || 'HTTP ' + resp.status);
        }
        this.display = await resp.json();
        this.showToast('Captions updated.');
        this.closeCaptionEditor();
      } catch(e) {
        this.showToast('Failed to update captions: ' + e.message);
      }
      this.captionSaving = false;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// galleryAdminApp — gallery administration page.
// Requires auth + GalleryCreate/Modify/Delete permissions.
// Features: create gallery, rename/delete gallery, expand to see displays,
// add/remove/reorder displays within a gallery, assign/change/clear each
// display's template.
// ─────────────────────────────────────────────────────────────────────────────
function galleryAdminApp() {
  return {
    loggedInUser: null,
    authConfig:   { googleEnabled: false, appleEnabled: false },
    toast:        { visible: false, message: '', timer: null },

    galleries:    [],
    loading:      true,
    error:        null,

    // Create form
    newTitle:    '',
    creating:    false,
    createError: '',

    // Rename
    editingGalleryId: null,
    editTitle:        '',
    savingTitle:      false,

    // Expanded gallery displays
    expandedGalleryId: null,
    expandedDisplays:  [],
    loadingDisplays:   false,
    // Placard defaults for the currently-expanded gallery (raw JSON from the
    // API, normalized on demand by normalizeGalleryPlacard()).
    expandedGalleryPlacard: null,

    // Display templates (for the "new display" and per-row template pickers)
    templates:            [],
    newDisplayTemplateId: '',

    avatarSrc(user) { return avatarSrc(user); },

    showToast(message) {
      clearTimeout(this.toast.timer);
      this.toast.message = message;
      this.toast.visible = true;
      this.toast.timer   = setTimeout(() => { this.toast.visible = false; }, 3500);
    },

    async init() {
      try {
        const [cfg, me] = await Promise.all([
          fetch('/auth/config').then(r => r.json()),
          fetch('/auth/me').then(r => r.json()),
        ]);
        this.authConfig = cfg;
        if (me.loggedIn) {
          this.loggedInUser       = me;
          window._testUserID      = me.userid;
          window._loggedIn        = true;
          window._currentUser     = me;
          document.dispatchEvent(new CustomEvent('photoapp:auth-ready', { detail: me }));
        }
      } catch { /* non-fatal */ }

      document.addEventListener('photoapp:auth-success', e => {
        this.loggedInUser   = e.detail;
        window._testUserID  = e.detail.userid;
        window._loggedIn    = true;
        window._currentUser = e.detail;
      });
      document.addEventListener('photoapp:toast', e => this.showToast(e.detail));

      await Promise.all([this.loadGalleries(), this.loadTemplates()]);
    },

    async loadTemplates() {
      try {
        const resp = await fetch('/api/v1/display-templates');
        if (!resp.ok) return; // non-fatal — template pickers just show empty
        const data = await resp.json();
        this.templates = sortTemplates(data.templates || []);
      } catch { /* non-fatal */ }
    },

    async loadGalleries() {
      this.loading = true;
      this.error   = null;
      try {
        const resp = await fetch('/api/v1/galleries?limit=100', { headers: getAuthHeaders() });
        if (resp.status === 403) {
          this.error = 'You do not have permission to manage galleries. Please log in as an admin.';
          this.loading = false;
          return;
        }
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const data = await resp.json();
        this.galleries = data.galleries || [];
      } catch(e) {
        this.error = e.message;
      }
      this.loading = false;
    },

    async createGallery() {
      if (this.creating || !this.newTitle.trim()) return;
      this.creating    = true;
      this.createError = '';
      try {
        const resp = await fetch('/api/v1/galleries', {
          method:  'POST',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({ title: this.newTitle.trim() }),
        });
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || 'HTTP ' + resp.status);
        }
        const g = await resp.json();
        this.galleries.push({ ...g, display_count: 0 });
        this.newTitle = '';
        this.showToast('Gallery created.');
      } catch(e) {
        this.createError = e.message;
      }
      this.creating = false;
    },

    startEdit(g) {
      this.editingGalleryId = g.galleryid;
      this.editTitle        = g.title;
    },

    cancelEdit() {
      this.editingGalleryId = null;
      this.editTitle        = '';
    },

    async saveTitle(galleryid) {
      if (!this.editTitle.trim()) return;
      this.savingTitle = true;
      try {
        const resp = await fetch('/api/v1/galleries/' + encodeURIComponent(galleryid), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({ title: this.editTitle.trim() }),
        });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const idx = this.galleries.findIndex(g => g.galleryid === galleryid);
        if (idx !== -1) this.galleries[idx] = { ...this.galleries[idx], title: this.editTitle.trim() };
        this.cancelEdit();
        this.showToast('Gallery renamed.');
      } catch(e) {
        this.showToast('Save failed: ' + e.message);
      }
      this.savingTitle = false;
    },

    async deleteGallery(galleryid) {
      if (!confirm('Delete this gallery and all its displays?')) return;
      try {
        const resp = await fetch('/api/v1/galleries/' + encodeURIComponent(galleryid), {
          method: 'DELETE', headers: getAuthHeaders(),
        });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        this.galleries = this.galleries.filter(g => g.galleryid !== galleryid);
        if (this.expandedGalleryId === galleryid) {
          this.expandedGalleryId = null;
          this.expandedDisplays  = [];
        }
        this.showToast('Gallery deleted.');
      } catch(e) {
        this.showToast('Delete failed: ' + e.message);
      }
    },

    async toggleDisplays(galleryid) {
      if (this.expandedGalleryId === galleryid) {
        this.expandedGalleryId = null;
        this.expandedDisplays  = [];
        this.expandedGalleryPlacard = null;
        return;
      }
      this.expandedGalleryId    = galleryid;
      this.loadingDisplays      = true;
      this.expandedDisplays     = [];
      this.expandedGalleryPlacard = null;
      this.newDisplayTemplateId = '';
      try {
        const resp = await fetch('/api/v1/galleries/' + encodeURIComponent(galleryid));
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const g = await resp.json();
        // selectedTemplateId backs the x-model on each row's template <select> —
        // kept as a plain field (rather than deriving :value from d.template) so
        // the dropdown updates instantly on selection instead of waiting on a
        // reactive re-render tied to the PATCH response.
        this.expandedDisplays = (g.displays || []).map(d => ({
          ...d,
          selectedTemplateId: d.template ? d.template.templateid : '',
        }));
        this.expandedGalleryPlacard = g.placard_defaults || null;
      } catch(e) {
        this.showToast('Failed to load displays: ' + e.message);
      }
      this.loadingDisplays = false;
    },

    // ── Placard settings (gallery-level) ─────────────────────────────────────
    // Placard size, background, and item content are configured once per
    // gallery for visual consistency across every display in it. Editing
    // happens on a deep-cloned draft so an in-progress edit doesn't affect
    // the live preview elsewhere until Save.
    placardModalOpen: false,
    placardGalleryId: null,
    placardDraft:     null,
    placardSaving:    false,
    placardError:     '',

    // Raw-JSON escape hatch: the visual editor covers everyday adjustments,
    // but exact numeric tweaks (e.g. nudging an item to x: 12.35 after the
    // general layout is already right) are fiddly with drag/number inputs.
    // Toggling this shows the exact placard_defaults JSON that gets saved —
    // the same shape normalizeGalleryPlacard()/placardItemsFor() consume —
    // so it's a precise view of the real data, not a separate format.
    placardJsonMode:  false,
    placardJsonText:  '',
    placardJsonError: '',

    // The draft's width/height are edited as physical size (inches or cm),
    // not percent — percent has no intuitive meaning on its own since the
    // canvas has no fixed physical size. `widthIn`/`heightIn`/`boardWidthIn`
    // are always canonically stored in inches; `unit` just controls which
    // unit the *Display fields (what the number inputs actually show/edit)
    // are rendered in. Editing a *Display field converts it straight back
    // to the canonical inches value (see placardUnitInput()); switching
    // units re-renders the *Display fields from the canonical inches value
    // (see refreshPlacardDisplayFields()) — nothing is ever converted
    // through a chain of round-trips that could drift.
    openPlacardSettings(galleryid) {
      this.placardGalleryId = galleryid;
      const normalized = normalizeGalleryPlacard(this.expandedGalleryPlacard);
      this.placardDraft = {
        boardWidthIn: normalized.boardWidthIn,
        widthIn:      normalized.widthIn,
        heightIn:     normalized.heightIn,
        unit:         normalized.unit,
        background:   normalized.background,
        borderColor:  normalized.borderColor,
        items:        JSON.parse(JSON.stringify(normalized.items)),
      };
      this.refreshPlacardDisplayFields();
      this.placardError     = '';
      this.placardJsonMode  = false;
      this.placardJsonText  = '';
      this.placardJsonError = '';
      this.placardModalOpen = true;
    },

    closePlacardSettings() {
      this.placardModalOpen = false;
      this.placardDraft     = null;
      this.placardJsonMode  = false;
    },

    // The exact placard_defaults shape that gets PATCHed to the API —
    // widthIn/heightIn straight from the draft (the visual editor's source
    // of truth), so the JSON view always shows the exact same numbers as
    // the visual editor's fields, just like items' xIn/yIn already do. Used
    // both to populate the JSON textarea and to build the real save
    // payload, so the two paths can never drift apart from each other.
    placardDraftToStored() {
      const d = this.placardDraft;
      return {
        widthIn:      d.widthIn,
        heightIn:     d.heightIn,
        boardWidthIn: d.boardWidthIn,
        unit:         d.unit,
        background:   d.background,
        borderColor:  d.borderColor,
        items:        d.items,
      };
    },

    // Applies a raw placard_defaults-shaped object (parsed from the JSON
    // textarea) back onto the draft's physical fields, normalizing it
    // through the same normalizeGalleryPlacard() used everywhere else so
    // malformed/partial JSON still produces a sane draft rather than
    // crashing the editor (and so legacy percent-only JSON still loads).
    applyPlacardJSON(raw) {
      const normalized = normalizeGalleryPlacard(raw);
      const d = this.placardDraft;
      d.boardWidthIn = normalized.boardWidthIn;
      d.widthIn      = normalized.widthIn;
      d.heightIn     = normalized.heightIn;
      d.unit         = normalized.unit;
      d.background   = normalized.background;
      d.borderColor  = normalized.borderColor;
      d.items        = normalized.items;
      this.refreshPlacardDisplayFields();
    },

    // Parses placardJsonText and applies it to the draft; returns false
    // (leaving placardJsonError set) on invalid JSON so callers can refuse
    // to switch back to the visual editor or save until it's fixed, rather
    // than silently discarding whatever the user typed.
    applyPlacardJsonText() {
      let parsed;
      try {
        parsed = JSON.parse(this.placardJsonText || '{}');
      } catch (e) {
        this.placardJsonError = 'Invalid JSON: ' + e.message;
        return false;
      }
      this.applyPlacardJSON(parsed);
      this.placardJsonError = '';
      return true;
    },

    // Toggling into JSON mode snapshots the current draft as JSON text;
    // toggling back out applies whatever's in the textarea first (so the
    // visual editor and preview reflect any precise edits made there) and
    // refuses to leave JSON mode if the text doesn't parse.
    togglePlacardJsonMode() {
      if (this.placardJsonMode) {
        if (!this.applyPlacardJsonText()) return;
        this.placardJsonMode = false;
      } else {
        this.placardJsonText  = JSON.stringify(this.placardDraftToStored(), null, 2);
        this.placardJsonError = '';
        this.placardJsonMode  = true;
      }
    },

    // Re-renders boardWidthDisplay/widthDisplay/heightDisplay from the
    // canonical inches values in the currently-selected unit. Called after
    // opening the modal and whenever the unit toggle changes.
    refreshPlacardDisplayFields() {
      const d    = this.placardDraft;
      const conv = d.unit === 'cm' ? inToCm : (v) => v;
      d.boardWidthDisplay = String(round2(conv(d.boardWidthIn)));
      d.widthDisplay      = String(round2(conv(d.widthIn)));
      d.heightDisplay     = String(round2(conv(d.heightIn)));
    },

    onPlacardUnitChange() {
      this.refreshPlacardDisplayFields();
    },

    // field is 'boardWidth' | 'width' | 'height'. Converts whatever the user
    // just typed (in the current display unit) back to the canonical inches
    // value; the *Display field itself keeps the raw typed text as-is so
    // typing "4." or "4.5" doesn't get reformatted mid-keystroke.
    placardUnitInput(field, rawValue) {
      const d = this.placardDraft;
      d[field + 'Display'] = rawValue;
      const num  = parseFloat(rawValue);
      const val  = isNaN(num) ? 0 : num;
      const toIn = d.unit === 'cm' ? cmToIn : (v) => v;
      const inches = toIn(val);
      d[field + 'In'] = Math.max(field === 'boardWidth' ? 0.1 : 0, inches);
    },

    addPlacardItem() {
      const n = this.placardDraft.items.length;
      const pos = defaultPlacardItemPosition(n, this.placardDraft.widthIn, this.placardDraft.heightIn);
      this.placardDraft.items.push({
        id: 'item-' + Date.now().toString(36) + '-' + Math.floor(Math.random() * 1e6).toString(36),
        text: '', xIn: pos.xIn, yIn: pos.yIn,
        fontFamily: "'DM Sans', sans-serif", fontSize: 12, fontWeight: 400,
        fontStyle: 'normal', color: '#3d3424', hideIfMissing: true,
      });
    },

    removePlacardItem(id) {
      this.placardDraft.items = this.placardDraft.items.filter(it => it.id !== id);
    },

    // Preview chip style — position is xIn/yIn (absolute inches from the
    // placard's own top-left corner) converted to a percent of the
    // *current* draft size, exactly like the real render (placardItemStyle())
    // so this preview always matches what actually shows up on a display.
    previewItemStyle(item) {
      const d    = this.placardDraft;
      const xPct = d.widthIn  > 0 ? (item.xIn / d.widthIn)  * 100 : 0;
      const yPct = d.heightIn > 0 ? (item.yIn / d.heightIn) * 100 : 0;
      return 'left:' + xPct + '%; top:' + yPct + '%; font-family:' + item.fontFamily +
        '; font-size:' + item.fontSize + 'px; font-weight:' + item.fontWeight +
        '; font-style:' + item.fontStyle + '; color:' + item.color + ';';
    },

    // Small human-readable label ("0.30in, 1.20in from top-left") shown next
    // to each item row so it's clear the position is now a fixed physical
    // offset, not a percentage that would shift if the placard is resized.
    itemPositionLabel(item) {
      const d    = this.placardDraft;
      const conv = d.unit === 'cm' ? inToCm : (v) => v;
      const x = round2(conv(item.xIn));
      const y = round2(conv(item.yIn));
      return x + d.unit + ', ' + y + d.unit + ' from top-left';
    },

    // Drag-to-position: mousedown/touchstart on an item chip in the preview
    // canvas starts tracking pointer movement, converting it to a percent
    // position within the canvas (clamped 0-100), then to an absolute inches
    // offset from the placard's top-left using the draft's *current*
    // physical size — so what's stored is a fixed physical position, not a
    // percentage that would silently shift if the placard is resized later.
    // Alpine's reactivity picks up the plain-object mutation and moves the
    // chip live. Listens on window (not the chip itself) so dragging still
    // works if the pointer leaves the chip mid-drag.
    //
    // The item's top-left is offset from wherever the pointer grabbed it
    // (e.g. the middle of the text), so we capture that offset once at
    // drag start and hold it constant through the drag — otherwise the
    // first move snaps the item's top-left corner straight to the cursor,
    // producing a visible jump equal to however far from the corner it was
    // grabbed, which then has to be dragged back out by hand.
    startItemDrag(item, event) {
      event.preventDefault();
      const canvas = this.$refs.placardCanvas;
      if (!canvas) return;
      const rect = canvas.getBoundingClientRect();
      const d = this.placardDraft;
      const startPt  = event.touches ? event.touches[0] : event;
      const itemPxX  = (d.widthIn  > 0 ? item.xIn / d.widthIn  : 0) * rect.width;
      const itemPxY  = (d.heightIn > 0 ? item.yIn / d.heightIn : 0) * rect.height;
      const offsetX  = startPt.clientX - rect.left - itemPxX;
      const offsetY  = startPt.clientY - rect.top  - itemPxY;
      const move = (e) => {
        const pt = e.touches ? e.touches[0] : e;
        let xPct = ((pt.clientX - rect.left - offsetX) / rect.width)  * 100;
        let yPct = ((pt.clientY - rect.top  - offsetY) / rect.height) * 100;
        xPct = Math.max(0, Math.min(100, xPct));
        yPct = Math.max(0, Math.min(100, yPct));
        item.xIn = Math.round((xPct / 100) * d.widthIn  * 100) / 100;
        item.yIn = Math.round((yPct / 100) * d.heightIn * 100) / 100;
      };
      const up = () => {
        window.removeEventListener('mousemove', move);
        window.removeEventListener('mouseup', up);
        window.removeEventListener('touchmove', move);
        window.removeEventListener('touchend', up);
      };
      window.addEventListener('mousemove', move);
      window.addEventListener('mouseup', up);
      window.addEventListener('touchmove', move, { passive: false });
      window.addEventListener('touchend', up);
    },

    async savePlacardSettings() {
      if (this.placardSaving || !this.placardDraft) return;
      // If the JSON editor is open, apply whatever's currently typed there
      // first — otherwise Save would silently save the stale pre-JSON-edit
      // draft instead of what's on screen. Invalid JSON blocks the save
      // (placardJsonError is already set by applyPlacardJsonText()).
      if (this.placardJsonMode && !this.applyPlacardJsonText()) return;
      this.placardSaving = true;
      this.placardError  = '';
      try {
        // Convert the draft's physical inches back to the percent-of-canvas
        // values that actually drive rendering (see the comment above
        // normalizeGalleryPlacard()) — boardWidthIn/unit are saved alongside
        // so re-opening the editor later shows the same physical size in the
        // same unit, without having to re-derive it from a rounded percent.
        const payload = this.placardDraftToStored();
        const resp = await fetch('/api/v1/galleries/' + encodeURIComponent(this.placardGalleryId), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({ placard_defaults: payload }),
        });
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || 'HTTP ' + resp.status);
        }
        const g = await resp.json();
        this.expandedGalleryPlacard = g.placard_defaults || null;
        this.placardModalOpen = false;
        this.showToast('Placard settings saved.');
      } catch(e) {
        this.placardError = e.message;
      }
      this.placardSaving = false;
    },

    async addDisplay(galleryid) {
      try {
        const resp = await fetch('/api/v1/galleries/' + encodeURIComponent(galleryid) + '/displays', {
          method:  'POST',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({
            sort_order: this.expandedDisplays.length,
            templateid: this.newDisplayTemplateId || undefined,
          }),
        });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const d = await resp.json();
        // Build a DisplaySummary-shaped object from the DisplayDetail response
        this.expandedDisplays.push({
          displayid:          d.displayid,
          sort_order:         d.sort_order,
          template:           d.template || null,
          selectedTemplateId: d.template ? d.template.templateid : '',
          slot_count:         d.slots ? d.slots.length : 0,
          filled_slots:       d.slots ? d.slots.filter(s => s.photo).length : 0,
          created_at:         d.created_at,
          updated_at:         d.updated_at,
        });
        const idx = this.galleries.findIndex(g => g.galleryid === galleryid);
        if (idx !== -1) {
          this.galleries[idx] = { ...this.galleries[idx], display_count: this.galleries[idx].display_count + 1 };
        }
        this.newDisplayTemplateId = '';
        this.showToast('Display added.');
      } catch(e) {
        this.showToast('Failed to add display: ' + e.message);
      }
    },

    // Re-syncs a per-display template <select>'s DOM value on initial render.
    // x-model's own initial binding runs before the nested x-for="t in templates"
    // has created its <option> elements (a select's own directives are processed
    // before Alpine walks into its children), so the browser silently falls back
    // to the first <option> ("No template") and never corrects itself on its own.
    // Called via x-init="syncTemplateSelect($el, d)" on the select.
    syncTemplateSelect(el, d) {
      this.$nextTick(() => { el.value = d.selectedTemplateId; });
    },

    // Assign, change, or clear (templateid === '') the template on an existing display.
    // The <select> is x-model-bound to d.selectedTemplateId, so it already shows the
    // pick instantly; here we just persist it and revert on failure.
    async setDisplayTemplate(displayid, templateid) {
      const idx = this.expandedDisplays.findIndex(d => d.displayid === displayid);
      if (idx === -1) return;
      const previousTemplateId = this.expandedDisplays[idx].template
        ? this.expandedDisplays[idx].template.templateid : '';
      try {
        const resp = await fetch('/api/v1/displays/' + encodeURIComponent(displayid), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({ templateid: templateid }),
        });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const d = await resp.json();
        this.expandedDisplays[idx] = {
          ...this.expandedDisplays[idx],
          template:           d.template || null,
          selectedTemplateId: d.template ? d.template.templateid : '',
          slot_count:         d.slots ? d.slots.length : 0,
          filled_slots:       d.slots ? d.slots.filter(s => s.photo).length : 0,
        };
        this.showToast(templateid ? 'Template assigned.' : 'Template cleared.');
      } catch(e) {
        // Revert the dropdown to whatever was actually saved before this attempt.
        this.expandedDisplays[idx] = { ...this.expandedDisplays[idx], selectedTemplateId: previousTemplateId };
        this.showToast('Failed to update template: ' + e.message);
      }
    },

    async deleteDisplay(displayid, galleryid) {
      if (!confirm('Remove this display?')) return;
      try {
        const resp = await fetch('/api/v1/displays/' + encodeURIComponent(displayid), {
          method: 'DELETE', headers: getAuthHeaders(),
        });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        this.expandedDisplays = this.expandedDisplays.filter(d => d.displayid !== displayid);
        const idx = this.galleries.findIndex(g => g.galleryid === galleryid);
        if (idx !== -1) {
          this.galleries[idx] = { ...this.galleries[idx], display_count: Math.max(0, this.galleries[idx].display_count - 1) };
        }
        this.showToast('Display removed.');
      } catch(e) {
        this.showToast('Delete failed: ' + e.message);
      }
    },

    async moveDisplay(displayid, direction) {
      const idx    = this.expandedDisplays.findIndex(d => d.displayid === displayid);
      if (idx === -1) return;
      const newIdx = direction === 'up' ? idx - 1 : idx + 1;
      if (newIdx < 0 || newIdx >= this.expandedDisplays.length) return;

      // Swap locally for immediate feedback
      const arr        = [...this.expandedDisplays];
      const tmp        = arr[idx];
      arr[idx]         = arr[newIdx];
      arr[newIdx]      = tmp;
      this.expandedDisplays = arr;

      // Persist new order
      try {
        const resp = await fetch('/api/v1/galleries/' + encodeURIComponent(this.expandedGalleryId), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({ display_order: arr.map(d => d.displayid) }),
        });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
      } catch(e) {
        this.showToast('Reorder failed: ' + e.message);
        // Reload displays to get true server state
        try {
          const r = await fetch('/api/v1/galleries/' + encodeURIComponent(this.expandedGalleryId));
          if (r.ok) { const g = await r.json(); this.expandedDisplays = g.displays || []; }
        } catch { /* leave current state */ }
      }
    },

    displayViewHref(displayid) {
      return '/display.html?displayid=' + displayid;
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// templateAdminApp — display template administration page.
// Requires auth + PermAdmin (enforced server-side; 403 surfaces as an error).
// Templates describe layout geometry (slot_positions) and styling rules
// (presentation) for displays. Both are free-form JSON — the frontend owns
// the schema. This editor auto-generates an even grid layout from photo_count
// and lets that JSON be hand-edited for finer control, with a live preview.
// ─────────────────────────────────────────────────────────────────────────────
function templateAdminApp() {
  return {
    loggedInUser: null,
    authConfig:   { googleEnabled: false, appleEnabled: false },
    toast:        { visible: false, message: '', timer: null },

    templates: [],
    loading:   true,
    error:     null,

    // Create form — fields live in a modal (createModalOpen), triggered by
    // the "+ Create Template" button in the page header.
    createModalOpen: false,
    newName:       '',
    newPhotoCount: 4,
    creating:      false,
    createError:   '',

    openCreateModal() {
      this.newName       = '';
      this.newPhotoCount = 4;
      this.createError   = '';
      this.createModalOpen = true;
    },
    closeCreateModal() {
      this.createModalOpen = false;
    },

    // Expanded editor (one at a time)
    expandedId:        null,
    editName:          '',
    editPhotoCount:    1,
    editSlotPositions: '',
    editPresentation:  '',
    editError:         '',
    saving:            false,

    showToast(message) {
      clearTimeout(this.toast.timer);
      this.toast.message = message;
      this.toast.visible = true;
      this.toast.timer   = setTimeout(() => { this.toast.visible = false; }, 3500);
    },

    async init() {
      try {
        const [cfg, me] = await Promise.all([
          fetch('/auth/config').then(r => r.json()),
          fetch('/auth/me').then(r => r.json()),
        ]);
        this.authConfig = cfg;
        if (me.loggedIn) {
          this.loggedInUser       = me;
          window._testUserID      = me.userid;
          window._loggedIn        = true;
          window._currentUser     = me;
          document.dispatchEvent(new CustomEvent('photoapp:auth-ready', { detail: me }));
        }
      } catch { /* non-fatal */ }

      document.addEventListener('photoapp:auth-success', e => {
        this.loggedInUser   = e.detail;
        window._testUserID  = e.detail.userid;
        window._loggedIn    = true;
        window._currentUser = e.detail;
      });
      document.addEventListener('photoapp:toast', e => this.showToast(e.detail));

      await this.loadTemplates();
    },

    async loadTemplates() {
      this.loading = true;
      this.error   = null;
      try {
        const resp = await fetch('/api/v1/display-templates');
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const data = await resp.json();
        this.templates = sortTemplates(data.templates || []);
      } catch(e) {
        this.error = e.message;
      }
      this.loading = false;
    },

    // Even grid layout, shared with displayApp/displayEditApp (see
    // defaultSlotPositions() near the top of this file) so a template's
    // fallback layout is identical to what a display without custom
    // slot_positions actually renders.
    defaultSlotPositions(n) { return defaultSlotPositions(n); },

    // Starter presentation for new templates — a plain matte in the theme's
    // frame color, no outer frame. See app.js's matte/frame helper comment
    // (above displayApp) for the full schema; editable per-template below.
    // Placard appearance/content is configured at the GALLERY level (Gallery
    // Admin's Placard Settings), not here — this template only carries each
    // slot's placard *position* (slot_positions[i].placard).
    defaultPresentation() {
      return {
        matte: { enabled: true,  color: '#e8e3d5', width: 16 },
        frame: { enabled: false, color: '#3d3424', width: 8 },
        align: { horizontal: 'center', vertical: 'center' },
      };
    },

    async createTemplate() {
      if (this.creating || !this.newName.trim() || this.newPhotoCount < 1) return;
      this.creating    = true;
      this.createError = '';
      try {
        const resp = await fetch('/api/v1/display-templates', {
          method:  'POST',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({
            name:           this.newName.trim(),
            photo_count:    this.newPhotoCount,
            slot_positions: this.defaultSlotPositions(this.newPhotoCount),
            presentation:   this.defaultPresentation(),
          }),
        });
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || 'HTTP ' + resp.status);
        }
        const t = await resp.json();
        this.templates = sortTemplates(this.templates.concat([t]));
        this.newName         = '';
        this.newPhotoCount   = 4;
        this.createModalOpen = false;
        this.showToast('Template created.');
      } catch(e) {
        this.createError = e.message;
      }
      this.creating = false;
    },

    startEdit(t) {
      this.expandedId        = t.templateid;
      this.editName          = t.name;
      this.editPhotoCount    = t.photo_count;
      this.editSlotPositions = JSON.stringify(t.slot_positions || [], null, 2);
      this.editPresentation  = JSON.stringify(t.presentation  || {}, null, 2);
      this.editError         = '';
    },

    cancelEdit() {
      this.expandedId = null;
      this.editError   = '';
    },

    regenerateLayout() {
      this.editSlotPositions = JSON.stringify(this.defaultSlotPositions(this.editPhotoCount), null, 2);
    },

    // Used by the live preview while editing — never throws.
    previewSlots() {
      try {
        const parsed = JSON.parse(this.editSlotPositions || '[]');
        return Array.isArray(parsed) ? parsed : [];
      } catch {
        return [];
      }
    },

    async saveTemplate(templateid) {
      if (!this.editName.trim() || this.editPhotoCount < 1) return;
      let slotPositions, presentation;
      try {
        slotPositions = JSON.parse(this.editSlotPositions || '[]');
      } catch {
        this.editError = 'Slot positions must be valid JSON.';
        return;
      }
      try {
        presentation = JSON.parse(this.editPresentation || '{}');
      } catch {
        this.editError = 'Presentation must be valid JSON.';
        return;
      }
      this.editError = '';
      this.saving    = true;
      try {
        const resp = await fetch('/api/v1/display-templates/' + encodeURIComponent(templateid), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
          body:    JSON.stringify({
            name:           this.editName.trim(),
            photo_count:    this.editPhotoCount,
            slot_positions: slotPositions,
            presentation:   presentation,
          }),
        });
        if (!resp.ok) {
          const e = await resp.json().catch(() => ({}));
          throw new Error(e.error || 'HTTP ' + resp.status);
        }
        const t   = await resp.json();
        const idx = this.templates.findIndex(x => x.templateid === templateid);
        const next = this.templates.slice();
        if (idx !== -1) next[idx] = t; else next.push(t);
        this.templates = sortTemplates(next);
        this.expandedId = null;
        this.showToast('Template saved.');
      } catch(e) {
        this.editError = e.message;
      }
      this.saving = false;
    },

    async deleteTemplate(templateid) {
      if (!confirm('Delete this template? Displays using it will lose their layout.')) return;
      try {
        const resp = await fetch('/api/v1/display-templates/' + encodeURIComponent(templateid), {
          method: 'DELETE', headers: getAuthHeaders(),
        });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        this.templates = this.templates.filter(t => t.templateid !== templateid);
        if (this.expandedId === templateid) this.expandedId = null;
        this.showToast('Template deleted.');
      } catch(e) {
        this.showToast('Delete failed: ' + e.message);
      }
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// Alpine init — store + component registration.
// Must run before Alpine initializes (alpine:init fires before Alpine walks DOM).
// app.js is loaded with defer, same as alpinejs.min.js, so order matters:
// <script src="/app.js"></script> BEFORE <script defer src="/alpinejs.min.js">
// ─────────────────────────────────────────────────────────────────────────────
document.addEventListener('alpine:init', () => {
  Alpine.store('ui', {
    emojiPickerOpen: false,
    labelModal: false,
    settingsOpen: false,
  });

  Alpine.data('photoApp',        photoApp);
  Alpine.data('wallApp',         wallApp);
  Alpine.data('galleriesNav',    galleriesNav);
  Alpine.data('galleriesApp',    galleriesApp);
  Alpine.data('displayApp',      displayApp);
  Alpine.data('displayEditApp',  displayEditApp);
  Alpine.data('galleryAdminApp', galleryAdminApp);
  Alpine.data('templateAdminApp', templateAdminApp);
  Alpine.data('userSwitcher',    userSwitcher);
  Alpine.data('titleEditor',     titleEditor);
  Alpine.data('commentsPanel',   commentsPanel);
  Alpine.data('commentItem',     commentItem);
  Alpine.data('labelEditor',     labelEditor);
  Alpine.data('emojiPicker',     emojiPicker);
  Alpine.data('emojiHover',      emojiHover);
  Alpine.data('avatarSettings',  avatarSettings);
  Alpine.data('authModal',       authModal);
});
