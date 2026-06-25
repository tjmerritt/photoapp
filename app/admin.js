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

      this.selectedExhibition = this.exhibitions[0].exhibitionid;
      await this.loadMore();
      await this.$nextTick();
      this.initScroll();
    },

    async changeExhibition() {
      // If the selected exhibition lives on a different host, navigate there.
      const ex = this.exhibitions.find(function(e) { return e.exhibitionid === this.selectedExhibition; }, this);
      if (ex && ex.hostname && ex.hostname !== window.location.host) {
        window.location.href = window.location.protocol + '//' + ex.hostname + '/admin';
        return;
      }
      // Reset and reload when the user picks a different exhibition.
      this.photos = [];
      this.offset = 0;
      this.total  = 0;
      await this.loadMore();
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
                + '&exhibitionid=' + encodeURIComponent(this.selectedExhibition);
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

document.addEventListener('alpine:init', function() {
  Alpine.data('adminApp', adminApp);
});
