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

  // fmtWaktu menulis waktu ISO sebagai "YYYY-MM-DD HH:MM:SS" waktu lokal,
  // sama dengan fmtTime di panel.
  function fmtWaktu(iso) {
    const t = new Date(iso);
    if (Number.isNaN(t.getTime())) {
      return "";
    }
    const p = (n) => String(n).padStart(2, "0");
    return `${t.getFullYear()}-${p(t.getMonth() + 1)}-${p(t.getDate())} ` +
      `${p(t.getHours())}:${p(t.getMinutes())}:${p(t.getSeconds())}`;
  }

  // kelompokTombol: status pekerjaan yang menampilkan tombol yang sama
  // (Batalkan untuk antre dan berjalan) masuk satu kelompok.
  function kelompokTombol(status) {
    return status === "queued" || status === "running" ? "aktif" : status;
  }

  document.addEventListener("alpine:init", () => {
    // Halaman pekerjaan: baris log baru (event job_log) ditambahkan sebagai
    // teks, lencana dan pesan tahap diperbarui (event job_update). Bila
    // status pekerjaan berubah sehingga tombolnya lain (menunggu, gagal,
    // selesai, dibatalkan), halaman dimuat ulang. Label dan kelas lencana
    // dari data-badges (peta yang sama dengan yang dipakai panel).
    window.Alpine.data("jobLive", () => ({
      init() {
        const el = this.$el;
        const id = el.dataset.jobId;
        let status = el.dataset.status;
        let lastLog = Number(el.dataset.lastLogId) || 0;
        const badges = JSON.parse(el.dataset.badges || "{}");
        const log = el.querySelector("[data-job-log]");
        const setBadge = (badge, st) => {
          const info = badges[st] || { Label: st, Class: "badge-ghost" };
          for (const b of Object.values(badges)) {
            badge.classList.remove(b.Class);
          }
          badge.classList.add(info.Class);
          badge.textContent = info.Label;
        };
        const es = new EventSource("/events");
        es.addEventListener("job_log", (e) => {
          const d = JSON.parse(e.data);
          if (d.job_id !== id || d.id <= lastLog || !log) {
            return;
          }
          lastLog = d.id;
          const kosong = log.querySelector("[data-job-log-empty]");
          if (kosong) {
            kosong.remove();
          }
          const line = document.createElement("div");
          line.className = "whitespace-pre-wrap";
          if (d.level === "error") {
            line.classList.add("text-error");
          } else if (d.level === "warn") {
            line.classList.add("text-warning");
          }
          line.textContent = `${fmtWaktu(d.ts)} [${d.level}] ${d.message}`;
          const diBawah = log.scrollTop + log.clientHeight >= log.scrollHeight - 4;
          log.appendChild(line);
          if (diBawah) {
            log.scrollTop = log.scrollHeight;
          }
        });
        es.addEventListener("job_update", (e) => {
          const d = JSON.parse(e.data);
          if (d.id !== id) {
            return;
          }
          if (d.step_status !== undefined) {
            const badge = el.querySelector(`[data-step-badge="${d.step}"]`);
            if (badge) {
              setBadge(badge, d.step_status);
            }
            const msg = el.querySelector(`[data-step-message="${d.step}"]`);
            if (msg) {
              msg.textContent = d.message || "";
            }
          }
          if (d.status !== undefined && d.status !== status) {
            if (kelompokTombol(d.status) !== kelompokTombol(status)) {
              location.reload();
              return;
            }
            status = d.status;
            const badge = el.querySelector("[data-job-badge]");
            if (badge) {
              setBadge(badge, status);
            }
          }
        });
      },
    }));


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
