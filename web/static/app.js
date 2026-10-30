// Skrip UI dbpanel. Panel memakai Content-Security-Policy tanpa skrip
// inline dan tanpa eval, jadi semua logika ada di berkas ini dan Alpine.js
// yang dipakai adalah build CSP: atribut x-data/x-text/:class/@click di
// template hanya menyebut nama komponen, properti, atau method di bawah ini.
// Berkas ini dimuat (defer) sebelum Alpine supaya komponennya sudah
// terdaftar saat Alpine mulai.
(() => {
  "use strict";

  // salinTeks menyalin teks elemen ke clipboard. navigator.clipboard hanya
  // ada di HTTPS atau localhost, jadi panel lewat HTTP biasa memakai
  // seleksi + execCommand.
  function salinTeks(el) {
    const teks = el.textContent.trim();
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(teks);
    }
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    document.execCommand("copy");
    sel.removeAllRanges();
    return Promise.resolve();
  }

  // onServerStatus memanggil fn(online) setiap ada event server_status untuk
  // server id dari aliran SSE /events.
  function onServerStatus(id, fn) {
    const es = new EventSource("/events");
    es.addEventListener("server_status", (e) => {
      const d = JSON.parse(e.data);
      if (d.id === id) {
        fn(d.online);
      }
    });
    return es;
  }

  document.addEventListener("alpine:init", () => {
    // Lencana Online/Offline di halaman server yang sudah dipasangkan.
    // Data awal dari data-server-id dan data-connected. (Bukan
    // data-online: html/template memperlakukan atribut data-on* sebagai
    // handler event dan mengisinya dengan escaping JS.)
    window.Alpine.data("serverStatus", () => ({
      online: false,
      init() {
        this.online = this.$el.dataset.connected === "true";
        const id = Number(this.$el.dataset.serverId);
        onServerStatus(id, (online) => {
          this.online = online;
        });
      },
      get badgeClass() {
        return { "badge-success": this.online, "badge-error": !this.online };
      },
      get label() {
        return this.online ? "Online" : "Offline";
      },
    }));

    // Kartu pemasangan server yang belum dipasangkan: muat ulang halaman
    // begitu agent-nya terhubung.
    window.Alpine.data("pairWait", () => ({
      init() {
        const id = Number(this.$el.dataset.serverId);
        onServerStatus(id, (online) => {
          if (online) {
            location.reload();
          }
        });
      },
    }));

    // Tombol Salin untuk perintah pemasangan (x-ref="perintah").
    window.Alpine.data("copyCommand", () => ({
      tersalin: false,
      copy() {
        salinTeks(this.$refs.perintah).then(() => {
          this.tersalin = true;
          setTimeout(() => {
            this.tersalin = false;
          }, 2000);
        });
      },
      get buttonLabel() {
        return this.tersalin ? "Tersalin" : "Salin";
      },
    }));
  });

  // Tombol dengan data-open-modal="<id dialog>" membuka dialog itu
  // (pengganti atribut onclick, yang diblokir CSP).
  document.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-open-modal]");
    if (!btn) {
      return;
    }
    const dlg = document.getElementById(btn.dataset.openModal);
    if (dlg && typeof dlg.showModal === "function") {
      dlg.showModal();
    }
  });
})();
