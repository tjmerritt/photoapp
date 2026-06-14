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
      try {
        const resp = await fetch(`/api/v1/comments/${encodeURIComponent(this.c.commentid)}`, {
          method: 'DELETE', headers: getAuthHeaders(),
        });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        document.dispatchEvent(new CustomEvent('photoapp:comment-deleted', { detail: this.c.commentid }));
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
      const uid = getAuthHeaders()['X-User-ID'];
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
      const headers = getAuthHeaders();
      if (!headers['X-User-ID']) {
        document.dispatchEvent(new CustomEvent('photoapp:toast', { detail: 'Select a user to react.' }));
        return;
      }
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
        if (this.loggedInUser) this.loggedInUser.profileImage = e.detail;
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
    saving:    false,
    uploading: false,
    _avatarHash: '',

    init() {
      // The presets getter must depend on a reactive Alpine property (_avatarHash)
      // rather than window._currentUser, which is a plain global that Alpine
      // cannot track.  We populate _avatarHash from two sources:
      //   1. photoapp:auth-ready — dispatched by photoApp.init() after /auth/me
      //      returns for an already-logged-in session (page load path).
      //   2. photoapp:auth-success — dispatched after the user logs in via the
      //      login form during an existing session.
      const sync = (e) => {
        if (e.detail && e.detail.avatarHash) this._avatarHash = e.detail.avatarHash;
      };
      document.addEventListener('photoapp:auth-ready',   sync);
      document.addEventListener('photoapp:auth-success', sync);
    },

    get presets() {
      const h = this._avatarHash;
      return h ? Array.from({ length: 20 }, (_, i) => i === 0 ? h : h + i) : [];
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
        if (!r.ok) throw new Error('Upload failed');
        const d = await r.json();
        if (window._currentUser) window._currentUser.profileImage = d.profileImage;
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

  Alpine.data('photoApp',      photoApp);
  Alpine.data('userSwitcher',  userSwitcher);
  Alpine.data('titleEditor',   titleEditor);
  Alpine.data('commentsPanel', commentsPanel);
  Alpine.data('commentItem',   commentItem);
  Alpine.data('labelEditor',   labelEditor);
  Alpine.data('emojiPicker',   emojiPicker);
  Alpine.data('emojiHover',      emojiHover);
  Alpine.data('avatarSettings',  avatarSettings);
  Alpine.data('authModal',       authModal);
});
