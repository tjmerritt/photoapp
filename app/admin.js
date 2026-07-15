// admin.js — photo visibility admin panel (Alpine.js CSP build)

// thumbUrl — same helper as in app.js; duplicated so admin.html is standalone.
function thumbUrl(url, cssWidth) {
  if (!url || url.indexOf('/api/v1/imgproxy') === -1) return url;
  var dpr = window.devicePixelRatio || 1;
  var w   = Math.round(cssWidth * dpr);
  return url + '&w=' + w;
}

function adminApp() {
  return {
    exhibitions:         [],
    selectedExhibition:  '',
    photos:              [],
    total:               0,
    offset:              0,
    loading:             false,
    authError:           false,
    noExhibitions:       false,
    search:              '', // Phase 6c: title/photoid filter
    toast:               { visible: false, message: '' },

    thumbUrl(url, cssWidth) { return thumbUrl(url, cssWidth); },

    async init() {
      const me = await fetch('/auth/me').then(function(r) { return r.json(); });
      if (!me.loggedIn) { window.location.href = '/'; return; }

      // Probe for admin access (403 = no permission, 404 = server not rebuilt yet)
      const probe = await fetch('/api/v1/admin/exhibitions');
      if (probe.status === 403 || probe.status === 404) { this.authError = true; return; }

      const data = await probe.json();
      this.exhibitions = data.exhibitions || [];

      if (this.exhibitions.length === 0) {
        this.noExhibitions = true;
        return;
      }

      const params = new URLSearchParams(window.location.search);
      const requestedID = params.get('exhibitionid');
      const match = requestedID && this.exhibitions.find(function(e) { return e.exhibitionid === requestedID; });
      this.selectedExhibition = match ? match.exhibitionid : this.exhibitions[0].exhibitionid;
      await this.loadMore();
      await this.$nextTick();
      this.initScroll();
    },

    async changeExhibition() {
      // If the selected exhibition lives on a different host, navigate there.
      const ex = this.exhibitions.find(function(e) { return e.exhibitionid === this.selectedExhibition; }, this);
      if (ex && ex.hostname && ex.hostname !== window.location.host) {
        window.location.href = window.location.protocol + '//' + ex.hostname + '/admin?exhibitionid=' + encodeURIComponent(ex.exhibitionid);
        return;
      }
      // Reset and reload when the user picks a different exhibition.
      this.photos = [];
      this.offset = 0;
      this.total  = 0;
      await this.loadMore();
    },

    // Phase 6c: re-runs the photo list from scratch with the current search
    // term — same reset-then-loadMore shape as changeExhibition() above.
    doSearch() {
      this.photos = [];
      this.offset = 0;
      this.total  = 0;
      this.loadMore();
    },

    initScroll() {
      const self = this;
      window.addEventListener('scroll', function() {
        if (self.loading || self.offset >= self.total) return;
        var pageH = Math.max(
          document.body.scrollHeight,
          document.documentElement.scrollHeight
        );
        if (window.scrollY + window.innerHeight >= pageH - 400) {
          self.loadMore();
        }
      }, { passive: true });
    },

    async loadMore() {
      if (this.loading || !this.selectedExhibition) return;
      this.loading = true;
      try {
        var url = '/api/v1/admin/photos?limit=50&offset=' + this.offset
                + '&exhibitionid=' + encodeURIComponent(this.selectedExhibition)
                + '&search=' + encodeURIComponent(this.search);
        const r    = await fetch(url);
        const data = await r.json();
        this.total  = data.total;
        this.photos = this.photos.concat(data.photos);
        this.offset += data.photos.length;
      } catch(e) {
        this.showToast('Load failed: ' + e.message);
      }
      this.loading = false;
    },

    // Called after x-model updates photo.is_public on checkbox change.
    async savePublic(photo) {
      const newVal = photo.is_public;
      try {
        const r = await fetch('/api/v1/admin/photo?photoid=' + photo.photoid, {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify({ is_public: newVal }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
      } catch(e) {
        photo.is_public = !newVal; // revert
        this.showToast('Save failed: ' + e.message);
      }
    },

    showToast(msg) {
      this.toast.message = msg;
      this.toast.visible = true;
      const self = this;
      setTimeout(function() { self.toast.visible = false; }, 3500);
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 6: shared exhibition-selector bootstrap, used by adminMaster and
// adminUsers (adminApp above predates this factoring-out and keeps its own
// inline copy rather than risk touching working code).
//
// Probes /api/v1/admin/exhibitions, redirects home if not logged in, and sets
// authError/noExhibitions/exhibitions/selectedExhibition directly on the
// given Alpine component instance. Returns true if the caller should go on
// to load its own exhibition-scoped data, false if it should stop (auth
// error, no exhibitions, or not logged in — all of which render their own
// empty state and don't need further loading).
// ─────────────────────────────────────────────────────────────────────────────
async function loadAdminExhibitions(app) {
  const me = await fetch('/auth/me').then(function(r) { return r.json(); });
  if (!me.loggedIn) { window.location.href = '/'; return false; }

  const probe = await fetch('/api/v1/admin/exhibitions');
  if (probe.status === 403 || probe.status === 404) { app.authError = true; return false; }

  const data = await probe.json();
  app.exhibitions = data.exhibitions || [];
  if (app.exhibitions.length === 0) { app.noExhibitions = true; return false; }

  const params = new URLSearchParams(window.location.search);
  const requestedID = params.get('exhibitionid');
  const match = requestedID && app.exhibitions.find(function(e) { return e.exhibitionid === requestedID; });
  app.selectedExhibition = match ? match.exhibitionid : app.exhibitions[0].exhibitionid;
  return true;
}

// If ex lives on a different host than the current page, navigate there
// (mirrors adminApp.changeExhibition's cross-host redirect) — returns true
// if a redirect was issued (caller should stop).
function crossHostRedirect(ex, pagePath) {
  if (ex && ex.hostname && ex.hostname !== window.location.host) {
    window.location.href = window.location.protocol + '//' + ex.hostname + pagePath
      + '?exhibitionid=' + encodeURIComponent(ex.exhibitionid);
    return true;
  }
  return false;
}

// ─────────────────────────────────────────────────────────────────────────────
// adminMaster (6a) — quick-stats panel + links to the other admin pages.
// ─────────────────────────────────────────────────────────────────────────────
function adminMaster() {
  return {
    exhibitions:        [],
    selectedExhibition: '',
    stats:              null,
    loading:            true,
    authError:          false,
    noExhibitions:      false,

    async init() {
      if (!(await loadAdminExhibitions(this))) { this.loading = false; return; }
      await this.loadStats();
      this.loading = false;
    },

    async changeExhibition() {
      const ex = this.exhibitions.find(function(e) { return e.exhibitionid === this.selectedExhibition; }, this);
      if (crossHostRedirect(ex, '/admin-master.html')) return;
      await this.loadStats();
    },

    async loadStats() {
      try {
        const r = await fetch('/api/v1/admin/stats?exhibitionid=' + encodeURIComponent(this.selectedExhibition));
        if (r.status === 403 || r.status === 404) { this.authError = true; return; }
        this.stats = await r.json();
      } catch (e) { /* leave stats null — page shows its loading state */ }
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// adminUsers (6b) — per-user toggles: account enabled, private-photo access,
// and manage-own-labels/emoji/comments.
// ─────────────────────────────────────────────────────────────────────────────
function adminUsers() {
  return {
    exhibitions:        [],
    selectedExhibition: '',
    users:              [],
    total:              0,
    offset:             0,
    limit:              50,
    search:             '',
    loading:            true,
    authError:          false,
    noExhibitions:      false,
    toast:              { visible: false, message: '' },

    async init() {
      if (!(await loadAdminExhibitions(this))) { this.loading = false; return; }
      await this.loadUsers();
      this.loading = false;
    },

    async changeExhibition() {
      const ex = this.exhibitions.find(function(e) { return e.exhibitionid === this.selectedExhibition; }, this);
      if (crossHostRedirect(ex, '/admin-users.html')) return;
      this.offset = 0;
      await this.loadUsers();
    },

    doSearch() {
      this.offset = 0;
      this.loadUsers();
    },

    async loadUsers() {
      this.loading = true;
      try {
        var url = '/api/v1/admin/users?exhibitionid=' + encodeURIComponent(this.selectedExhibition)
                + '&search=' + encodeURIComponent(this.search)
                + '&limit=' + this.limit + '&offset=' + this.offset;
        const r = await fetch(url);
        if (r.status === 403 || r.status === 404) { this.authError = true; this.loading = false; return; }
        const data = await r.json();
        this.users = data.users || [];
        this.total = data.total || 0;
      } catch (e) {
        this.showToast('Load failed: ' + e.message);
      }
      this.loading = false;
    },

    prevPage() { if (this.offset > 0) { this.offset = Math.max(0, this.offset - this.limit); this.loadUsers(); } },
    nextPage() { if (this.offset + this.limit < this.total) { this.offset += this.limit; this.loadUsers(); } },

    // Generic toggle for the five boolean flags — x-model on the checkbox
    // flips user[field] first, then @change calls this to persist it,
    // reverting on failure. Field names match the JSON body keys the
    // PATCH /api/v1/admin/users/:userid endpoint expects exactly, so no
    // translation table is needed here.
    async toggleFlag(user, field) {
      const newVal = user[field];
      try {
        var body = {};
        body[field] = newVal;
        const r = await fetch('/api/v1/admin/users/' + user.userid
                    + '?exhibitionid=' + encodeURIComponent(this.selectedExhibition), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify(body),
        });
        if (!r.ok) {
          const e = await r.json().catch(function() { return {}; });
          throw new Error(e.error || ('HTTP ' + r.status));
        }
      } catch (e) {
        user[field] = !newVal;
        this.showToast('Update failed: ' + e.message);
      }
    },

    showToast(msg) {
      this.toast.message = msg;
      this.toast.visible = true;
      const self = this;
      setTimeout(function() { self.toast.visible = false; }, 3500);
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// adminEmojis (6d) — enable/disable emoji types, with source/status filters
// and a "used only" toggle. Site-wide (emoji_types has no exhibitionid), so
// no exhibition selector here. Infinite-scrolled (same window-scroll-listener
// shape as adminApp's photo grid above) rather than prev/next buttons.
// ─────────────────────────────────────────────────────────────────────────────
function adminEmojis() {
  return {
    emojis:    [],
    total:     0,
    offset:    0,
    limit:     60,
    search:    '',
    source:    'all',   // 'all' | 'openmoji' | 'custom'
    status:    'all',   // 'all' | 'enabled' | 'disabled'
    usedOnly:  false,
    loading:   true,
    authError: false,
    toast:     { visible: false, message: '' },

    thumbUrl(url, cssWidth) { return thumbUrl(url, cssWidth); },

    async init() {
      const me = await fetch('/auth/me').then(function(r) { return r.json(); });
      if (!me.loggedIn) { window.location.href = '/'; return; }
      await this.loadMore();
      await this.$nextTick();
      this.initScroll();
    },

    initScroll() {
      const self = this;
      window.addEventListener('scroll', function() {
        if (self.loading || self.offset >= self.total) return;
        var pageH = Math.max(
          document.body.scrollHeight,
          document.documentElement.scrollHeight
        );
        if (window.scrollY + window.innerHeight >= pageH - 400) {
          self.loadMore();
        }
      }, { passive: true });
    },

    // Any filter change discards the current list and restarts from the top.
    doSearch() {
      this.emojis = [];
      this.offset = 0;
      this.total  = 0;
      this.loadMore();
    },

    async loadMore() {
      if (this.loading) return;
      this.loading = true;
      try {
        var url = '/api/v1/admin/emoji-types?search=' + encodeURIComponent(this.search)
                + '&source=' + encodeURIComponent(this.source)
                + '&status=' + encodeURIComponent(this.status)
                + '&used_only=' + (this.usedOnly ? 'true' : 'false')
                + '&limit=' + this.limit + '&offset=' + this.offset;
        const r = await fetch(url);
        if (r.status === 403) { this.authError = true; this.loading = false; return; }
        const data = await r.json();
        this.total  = data.total || 0;
        this.emojis = this.emojis.concat(data.emojis || []);
        this.offset += (data.emojis || []).length;
      } catch (e) {
        this.showToast('Load failed: ' + e.message);
      }
      this.loading = false;
    },

    async toggleActive(em) {
      const newVal = em.is_active;
      try {
        const r = await fetch('/api/v1/admin/emoji-types/' + em.emojiid, {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify({ is_active: newVal }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
      } catch (e) {
        em.is_active = !newVal;
        this.showToast('Update failed: ' + e.message);
      }
    },

    showToast(msg) {
      this.toast.message = msg;
      this.toast.visible = true;
      const self = this;
      setTimeout(function() { self.toast.visible = false; }, 3500);
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// adminLabels (6e) — enable/disable and restrict/unrestrict label names,
// plus the same color override as the Phase 5a picker. Site-wide (labels
// aren't scoped to an exhibition), so no exhibition selector here either.
// ─────────────────────────────────────────────────────────────────────────────
function adminLabels() {
  return {
    names:           [],
    total:           0,
    offset:          0,
    limit:           60,
    search:          '',
    includeDisabled: true,
    loading:         true,
    authError:       false,
    toast:           { visible: false, message: '' },

    async init() {
      const me = await fetch('/auth/me').then(function(r) { return r.json(); });
      if (!me.loggedIn) { window.location.href = '/'; return; }
      await this.load();
    },

    doSearch() {
      this.offset = 0;
      this.load();
    },

    async load() {
      this.loading = true;
      try {
        var url = '/api/v1/admin/label-names?search=' + encodeURIComponent(this.search)
                + '&include_disabled=' + (this.includeDisabled ? 'true' : 'false')
                + '&limit=' + this.limit + '&offset=' + this.offset;
        const r = await fetch(url);
        if (r.status === 403) { this.authError = true; this.loading = false; return; }
        const data = await r.json();
        this.names = data.names || [];
        this.total = data.total || 0;
      } catch (e) {
        this.showToast('Load failed: ' + e.message);
      }
      this.loading = false;
    },

    // Shared PATCH helper — all three toggles/color edits go through
    // PATCH /api/v1/label-names?name=..., which returns the updated row.
    async patchName(n, body) {
      try {
        const r = await fetch('/api/v1/label-names?name=' + encodeURIComponent(n.name), {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify(body),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        const updated = await r.json();
        n.color      = updated.color;
        n.restricted = updated.restricted;
        n.enabled    = updated.enabled;
        return true;
      } catch (e) {
        this.showToast('Update failed: ' + e.message);
        return false;
      }
    },

    async toggleEnabled(n) {
      const newVal = n.enabled;
      if (!(await this.patchName(n, { enabled: newVal }))) n.enabled = !newVal;
    },

    async toggleRestricted(n) {
      const newVal = n.restricted;
      if (!(await this.patchName(n, { restricted: newVal }))) n.restricted = !newVal;
    },

    async setColor(n, colorHex) {
      await this.patchName(n, { color_hex: colorHex });
    },

    prevPage() { if (this.offset > 0) { this.offset = Math.max(0, this.offset - this.limit); this.load(); } },
    nextPage() { if (this.offset + this.limit < this.total) { this.offset += this.limit; this.load(); } },

    showToast(msg) {
      this.toast.message = msg;
      this.toast.visible = true;
      const self = this;
      setTimeout(function() { self.toast.visible = false; }, 3500);
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// adminTeams (6f) — create/edit/delete teams and manage their membership.
// Exhibition-scoped (a team belongs to one exhibition), same selector
// pattern as adminUsers/adminMaster via loadAdminExhibitions().
// ─────────────────────────────────────────────────────────────────────────────
function adminTeams() {
  return {
    exhibitions:        [],
    selectedExhibition: '',
    teams:              [],
    total:              0,
    offset:             0,
    limit:              50,
    search:             '',
    loading:            true,
    authError:          false,
    noExhibitions:      false,
    toast:              { visible: false, message: '' },

    // New-team form.
    newTeamName:        '',
    newTeamDescription: '',
    creating:           false,

    // Member panel — only one team's members are shown expanded at a time.
    expandedTeamId:     null,
    members:            [],
    membersLoading:     false,
    memberSearch:       '',
    memberResults:      [],
    memberSearching:    false,

    async init() {
      if (!(await loadAdminExhibitions(this))) { this.loading = false; return; }
      await this.loadTeams();
      this.loading = false;
    },

    async changeExhibition() {
      const ex = this.exhibitions.find(function(e) { return e.exhibitionid === this.selectedExhibition; }, this);
      if (crossHostRedirect(ex, '/admin-teams.html')) return;
      this.offset = 0;
      this.expandedTeamId = null;
      await this.loadTeams();
    },

    doSearch() {
      this.offset = 0;
      this.loadTeams();
    },

    async loadTeams() {
      this.loading = true;
      try {
        var url = '/api/v1/admin/teams?exhibitionid=' + encodeURIComponent(this.selectedExhibition)
                + '&search=' + encodeURIComponent(this.search)
                + '&limit=' + this.limit + '&offset=' + this.offset;
        const r = await fetch(url);
        if (r.status === 403 || r.status === 404) { this.authError = true; this.loading = false; return; }
        const data = await r.json();
        this.teams = data.teams || [];
        this.total = data.total || 0;
      } catch (e) {
        this.showToast('Load failed: ' + e.message);
      }
      this.loading = false;
    },

    prevPage() { if (this.offset > 0) { this.offset = Math.max(0, this.offset - this.limit); this.loadTeams(); } },
    nextPage() { if (this.offset + this.limit < this.total) { this.offset += this.limit; this.loadTeams(); } },

    async createTeam() {
      const name = this.newTeamName.trim();
      if (!name) { this.showToast('Team name is required.'); return; }
      this.creating = true;
      try {
        const r = await fetch('/api/v1/admin/teams?exhibitionid=' + encodeURIComponent(this.selectedExhibition), {
          method:  'POST',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify({ name: name, description: this.newTeamDescription.trim() }),
        });
        if (!r.ok) {
          const e = await r.json().catch(function() { return {}; });
          throw new Error(e.error || ('HTTP ' + r.status));
        }
        this.newTeamName = '';
        this.newTeamDescription = '';
        this.offset = 0;
        await this.loadTeams();
      } catch (e) {
        this.showToast('Create failed: ' + e.message);
      }
      this.creating = false;
    },

    startEdit(t) {
      t.editing = true;
      t.editName = t.name;
      t.editDescription = t.description;
    },

    cancelEdit(t) {
      t.editing = false;
    },

    async saveEdit(t) {
      const name = (t.editName || '').trim();
      if (!name) { this.showToast('Team name is required.'); return; }
      try {
        const r = await fetch('/api/v1/admin/teams/' + t.teamid, {
          method:  'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify({ name: name, description: t.editDescription || '' }),
        });
        if (!r.ok) {
          const e = await r.json().catch(function() { return {}; });
          throw new Error(e.error || ('HTTP ' + r.status));
        }
        t.name = name;
        t.description = t.editDescription || '';
        t.editing = false;
      } catch (e) {
        this.showToast('Update failed: ' + e.message);
      }
    },

    async deleteTeam(t) {
      try {
        const r = await fetch('/api/v1/admin/teams/' + t.teamid, { method: 'DELETE' });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        this.teams = this.teams.filter(function(x) { return x.teamid !== t.teamid; });
        this.total = Math.max(0, this.total - 1);
        if (this.expandedTeamId === t.teamid) this.expandedTeamId = null;
      } catch (e) {
        this.showToast('Delete failed: ' + e.message);
      }
    },

    async toggleExpand(t) {
      if (this.expandedTeamId === t.teamid) { this.expandedTeamId = null; return; }
      this.expandedTeamId = t.teamid;
      this.memberSearch = '';
      this.memberResults = [];
      await this.loadMembers(t.teamid);
    },

    async loadMembers(teamid) {
      this.membersLoading = true;
      try {
        const r = await fetch('/api/v1/admin/teams/' + teamid + '/members');
        const data = await r.json();
        this.members = data.members || [];
      } catch (e) {
        this.showToast('Load members failed: ' + e.message);
      }
      this.membersLoading = false;
    },

    async searchMemberCandidates() {
      const q = this.memberSearch.trim();
      if (!q) { this.memberResults = []; return; }
      this.memberSearching = true;
      try {
        const url = '/api/v1/admin/users?exhibitionid=' + encodeURIComponent(this.selectedExhibition)
                  + '&search=' + encodeURIComponent(q) + '&limit=10';
        const r = await fetch(url);
        const data = await r.json();
        const memberIds = this.members.map(function(m) { return m.userid; });
        this.memberResults = (data.users || []).filter(function(u) { return memberIds.indexOf(u.userid) === -1; });
      } catch (e) {
        this.showToast('Search failed: ' + e.message);
      }
      this.memberSearching = false;
    },

    async addMember(teamid, user) {
      try {
        const r = await fetch('/api/v1/admin/teams/' + teamid + '/members?userid=' + encodeURIComponent(user.userid), {
          method: 'POST',
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        this.members.push({ userid: user.userid, username: user.username, email: user.email, joined_at: '' });
        this.memberResults = this.memberResults.filter(function(u) { return u.userid !== user.userid; });
      } catch (e) {
        this.showToast('Add member failed: ' + e.message);
      }
    },

    async removeMember(teamid, user) {
      try {
        const r = await fetch('/api/v1/admin/teams/' + teamid + '/members/' + user.userid, { method: 'DELETE' });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        this.members = this.members.filter(function(m) { return m.userid !== user.userid; });
      } catch (e) {
        this.showToast('Remove member failed: ' + e.message);
      }
    },

    showToast(msg) {
      this.toast.message = msg;
      this.toast.visible = true;
      const self = this;
      setTimeout(function() { self.toast.visible = false; }, 3500);
    },
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// adminPermissions (6g) — viewer for entity_role_grants, split into "global"
// (resource_type IS NULL — applies across every exhibition, not just the
// role's home one; see GrantsHandler's backend doc comment) and
// "exhibition-specific" grants, each revocable.
// ─────────────────────────────────────────────────────────────────────────────
function adminPermissions() {
  return {
    exhibitions:        [],
    selectedExhibition: '',
    globalGrants:       [],
    globalTotal:        0,
    exGrants:           [],
    exTotal:            0,
    loading:            true,
    authError:          false,
    noExhibitions:      false,
    toast:              { visible: false, message: '' },

    async init() {
      if (!(await loadAdminExhibitions(this))) { this.loading = false; return; }
      await Promise.all([this.loadGlobal(), this.loadForExhibition()]);
      this.loading = false;
    },

    async changeExhibition() {
      const ex = this.exhibitions.find(function(e) { return e.exhibitionid === this.selectedExhibition; }, this);
      if (crossHostRedirect(ex, '/admin-permissions.html')) return;
      await this.loadForExhibition();
    },

    async loadGlobal() {
      try {
        const r = await fetch('/api/v1/admin/grants/global?limit=200');
        if (r.status === 403 || r.status === 404) { this.authError = true; return; }
        const data = await r.json();
        this.globalGrants = data.grants || [];
        this.globalTotal  = data.total  || 0;
      } catch (e) {
        this.showToast('Load failed: ' + e.message);
      }
    },

    async loadForExhibition() {
      try {
        var url = '/api/v1/admin/grants/exhibition?exhibitionid=' + encodeURIComponent(this.selectedExhibition) + '&limit=200';
        const r = await fetch(url);
        if (r.status === 403 || r.status === 404) { this.authError = true; return; }
        const data = await r.json();
        this.exGrants = data.grants || [];
        this.exTotal  = data.total  || 0;
      } catch (e) {
        this.showToast('Load failed: ' + e.message);
      }
    },

    async revokeGlobal(g) {
      try {
        const r = await fetch('/api/v1/admin/grants/' + g.grantid, { method: 'DELETE' });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        this.globalGrants = this.globalGrants.filter(function(x) { return x.grantid !== g.grantid; });
        this.globalTotal = Math.max(0, this.globalTotal - 1);
      } catch (e) {
        this.showToast('Revoke failed: ' + e.message);
      }
    },

    async revokeExhibition(g) {
      try {
        const r = await fetch('/api/v1/admin/grants/' + g.grantid, { method: 'DELETE' });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        this.exGrants = this.exGrants.filter(function(x) { return x.grantid !== g.grantid; });
        this.exTotal = Math.max(0, this.exTotal - 1);
      } catch (e) {
        this.showToast('Revoke failed: ' + e.message);
      }
    },

    showToast(msg) {
      this.toast.message = msg;
      this.toast.visible = true;
      const self = this;
      setTimeout(function() { self.toast.visible = false; }, 3500);
    },
  };
}

document.addEventListener('alpine:init', function() {
  Alpine.data('adminApp', adminApp);
  Alpine.data('adminMaster', adminMaster);
  Alpine.data('adminUsers', adminUsers);
  Alpine.data('adminEmojis', adminEmojis);
  Alpine.data('adminLabels', adminLabels);
  Alpine.data('adminTeams', adminTeams);
  Alpine.data('adminPermissions', adminPermissions);
});
