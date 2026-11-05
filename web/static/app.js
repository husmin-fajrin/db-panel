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

  // aliranSSE mengembalikan satu EventSource /events yang dipakai bersama
  // oleh semua komponen halaman, supaya tiap halaman hanya membuka satu
  // koneksi SSE (peramban membatasi koneksi per host).
  let sse = null;
  function aliranSSE() {
    if (!sse) {
      sse = new EventSource("/events");
    }
    return sse;
  }

  // onServerStatus memanggil fn(online) setiap ada event server_status untuk
  // server id dari aliran SSE /events.
  function onServerStatus(id, fn) {
    const es = aliranSSE();
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

  // isiTersembunyi mengisi <input type="hidden" name=name> di form,
  // membuatnya bila belum ada.
  function isiTersembunyi(form, name, value) {
    let el = Array.from(form.elements).find((x) => x.name === name);
    if (!el) {
      el = document.createElement("input");
      el.type = "hidden";
      el.name = name;
      form.appendChild(el);
    }
    el.value = value;
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
        // jumlah: baris log di halaman; batas: jobLogMaxLines panel
        // (data-log-max).
        let jumlah = log ? log.querySelectorAll(".whitespace-pre-wrap").length : 0;
        const batas = log ? Number(log.dataset.logMax) || 5000 : 0;
        // tambahBaris menambahkan satu baris log ({id, ts, level, message})
        // bila belum tampil.
        const tambahBaris = (d) => {
          if (d.id <= lastLog || !log) {
            return;
          }
          lastLog = d.id;
          jumlah++;
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
        };
        // Setiap kali aliran SSE tersambung (pertama kali dan setelah
        // putus), baris yang terlewat diambil dari /jobs/{id}/log?after=.
        // Selama pengambilan, event job_log ditahan supaya urutan tetap.
        // Batas halaman dihormati: log yang sudah dipotong panel
        // (data-log-cut) tidak disusulkan, dan penyusulan berhenti begitu
        // halaman memuat `batas` baris; lalu pemberitahuan "Log terlalu
        // panjang" ditampilkan dan sambungan berikutnya tidak menyusul lagi.
        // Baris baru tetap datang langsung lewat job_log, seperti di panel.
        const potong = () => {
          log.dataset.logCut = "true";
          const notice = el.querySelector("[data-log-cut-notice]");
          if (notice) {
            notice.hidden = false;
          }
        };
        let ditahan = null;
        const susulkan = () => {
          if (ditahan || !log || log.dataset.logCut === "true") {
            return;
          }
          ditahan = [];
          const lepas = () => {
            const antre = ditahan;
            ditahan = null;
            antre.forEach(tambahBaris);
          };
          const ambil = () => {
            if (jumlah >= batas) {
              potong();
              return Promise.resolve(null);
            }
            return fetch(`/jobs/${id}/log?after=${lastLog}`, { headers: { Accept: "application/json" } })
              .then((r) => (r.ok ? r.json() : []))
              .then((baris) => {
                const muat = baris.slice(0, Math.max(0, batas - jumlah));
                muat.forEach(tambahBaris);
                if (muat.length < baris.length) {
                  potong();
                  return null;
                }
                if (baris.length >= 500) {
                  return ambil();
                }
                return null;
              });
          };
          ambil().catch(() => {}).finally(lepas);
        };
        const es = aliranSSE();
        es.addEventListener("open", susulkan);
        es.addEventListener("job_log", (e) => {
          const d = JSON.parse(e.data);
          if (d.job_id !== id) {
            return;
          }
          if (ditahan) {
            ditahan.push(d);
          } else {
            tambahBaris(d);
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

    // Formulir "Lepas database" (x-ref="form") di halaman database replika:
    // tombolnya membuka dialog konfirmasi (x-ref="dialog") yang menyebut
    // database terpilih dan apakah datanya ikut dihapus. Tombol "Ya, lepas"
    // di dialog mengirim formulir itu.
    window.Alpine.data("removeDatabases", () => ({
      names: "",
      dropData: false,
      error: "",
      confirm() {
        const form = this.$refs.form;
        const dipilih = Array.from(form.querySelectorAll('input[name="databases"]:checked'), (el) => el.value);
        if (dipilih.length === 0) {
          this.error = "Pilih minimal satu database.";
          return;
        }
        this.error = "";
        this.names = dipilih.join(", ");
        const hapus = form.querySelector('input[name="drop_data"]');
        this.dropData = Boolean(hapus && hapus.checked);
        this.$refs.dialog.showModal();
      },
      get keepClass() {
        return { hidden: this.dropData };
      },
      get dropClass() {
        return { hidden: !this.dropData };
      },
    }));

    // Persetujuan berbahaya di halaman pekerjaan (restart master, hapus
    // database, CLONE): tombolnya membuka dialog (x-ref="dialog") yang
    // menjelaskan akibatnya; formulir persetujuan ada di dalam dialog.
    window.Alpine.data("confirmApproval", () => ({
      open() {
        this.$refs.dialog.showModal();
      },
    }));

    // Konfirmasi kata sandi (spec 5.7), satu dialog per halaman
    // (_confirm.html). Tombol destruktif membawa data-confirm-action,
    // data-confirm-kind, data-confirm-id, data-confirm-text dan, bila nama
    // server harus diketik, data-confirm-name. Klik tombol itu (juga Enter
    // di formulirnya) ditahan: dialog meminta kata sandi panel (dan nama
    // server), meminta token ke POST /confirm, lalu mengisi confirm_token
    // (dan confirm_name) di formulir tombol dan mengirimnya. Panel
    // memeriksa token dan nama itu lagi.
    window.Alpine.data("confirmModal", () => ({
      text: "",
      name: "",
      typed: "",
      error: "",
      busy: false,
      tombol: null,
      init() {
        document.addEventListener("click", (e) => {
          const btn = e.target.closest("[data-confirm-action]");
          if (!btn) {
            return;
          }
          e.preventDefault();
          if (!btn.form || !btn.form.reportValidity()) {
            return;
          }
          this.open(btn);
        });
      },
      open(btn) {
        this.tombol = btn;
        this.text = btn.dataset.confirmText || "";
        this.name = btn.dataset.confirmName || "";
        this.typed = "";
        this.error = "";
        this.busy = false;
        this.$refs.typed.value = "";
        this.$refs.password.value = "";
        this.$root.showModal();
        (this.name ? this.$refs.typed : this.$refs.password).focus();
      },
      input() {
        this.typed = this.$refs.typed.value;
      },
      close() {
        this.$refs.password.value = "";
        this.$root.close();
      },
      get nameClass() {
        return { hidden: this.name === "" };
      },
      get blocked() {
        return this.busy || (this.name !== "" && this.typed !== this.name);
      },
      submit() {
        if (this.blocked || !this.tombol) {
          return;
        }
        const d = this.tombol.dataset;
        const form = this.tombol.form;
        const body = new URLSearchParams({
          action: d.confirmAction || "",
          target_kind: d.confirmKind || "",
          target_id: d.confirmId || "",
          password: this.$refs.password.value,
        });
        this.busy = true;
        this.error = "";
        fetch("/confirm", { method: "POST", credentials: "same-origin", headers: { Accept: "application/json" }, body })
          .then((r) => {
            // Sesi habis: fetch mengikuti redirect ke /login.
            if (r.redirected) {
              throw new Error("Sesi berakhir; muat ulang halaman lalu masuk lagi.");
            }
            return r.json().catch(() => ({})).then((j) => {
              if (!r.ok || typeof j.token !== "string") {
                throw new Error(j.error || "Konfirmasi gagal; coba lagi.");
              }
              return j.token;
            });
          })
          .then((token) => {
            isiTersembunyi(form, "confirm_token", token);
            if (this.name !== "") {
              isiTersembunyi(form, "confirm_name", this.typed);
            }
            this.$refs.password.value = "";
            form.submit();
          })
          .catch((err) => {
            this.error = err.message;
            this.busy = false;
          });
      },
    }));

    // Dialog yang dibuka saat halaman dimuat bila data-open="true" (Resync
    // ulang: setelah panel membaca ulang daftar database replika).
    window.Alpine.data("openDialog", () => ({
      init() {
        if (this.$el.dataset.open === "true" && typeof this.$el.showModal === "function") {
          this.$el.showModal();
        }
      },
    }));

    // Lencana jumlah peringatan aktif di menu (elemen [data-alert-count]
    // di dalam komponen): setiap event alert_update jumlahnya diambil ulang
    // dari GET /alerts/count; lencana disembunyikan saat jumlahnya 0.
    // Jawaban yang datang setelah jawaban permintaan berikutnya dibuang.
    window.Alpine.data("alertCount", () => ({
      init() {
        const badge = this.$el.querySelector("[data-alert-count]");
        if (!badge) {
          return;
        }
        let nomor = 0;
        aliranSSE().addEventListener("alert_update", () => {
          const ini = ++nomor;
          fetch("/alerts/count", { credentials: "same-origin", headers: { Accept: "application/json" } })
            // Sesi habis: fetch mengikuti redirect ke /login.
            .then((r) => (r.ok && !r.redirected ? r.json() : Promise.reject(new Error(String(r.status)))))
            .then((d) => {
              if (ini !== nomor || typeof d.count !== "number") {
                return;
              }
              badge.textContent = String(d.count);
              badge.classList.toggle("hidden", d.count === 0);
            })
            .catch(() => {}); // angka lama tetap tampil; event berikutnya mencoba lagi
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

  // Status pemantauan yang diperbarui langsung: elemen
  // data-status-for="<id server>" (dashboard, halaman master dan replika)
  // diisi ulang dengan GET /servers/<id>/status-fragment setiap ada event
  // health_update untuk server itu; data-status-variant="baris" meminta
  // bentuk ringkasnya. Server yang tidak ada di halaman diabaikan. Jawaban
  // yang datang setelah jawaban permintaan berikutnya dibuang.
  function pasangStatusLangsung() {
    if (!document.querySelector("[data-status-for]")) {
      return;
    }
    const terakhir = new Map(); // "<id>|<varian>" -> nomor permintaan terbaru
    let nomor = 0;
    const es = aliranSSE();
    es.addEventListener("health_update", (e) => {
      const id = String(JSON.parse(e.data).server_id);
      const perVarian = new Map();
      for (const el of document.querySelectorAll("[data-status-for]")) {
        if (el.dataset.statusFor !== id) {
          continue;
        }
        const varian = el.dataset.statusVariant || "";
        if (!perVarian.has(varian)) {
          perVarian.set(varian, []);
        }
        perVarian.get(varian).push(el);
      }
      for (const [varian, els] of perVarian) {
        const kunci = `${id}|${varian}`;
        const ini = ++nomor;
        terakhir.set(kunci, ini);
        const url = `/servers/${encodeURIComponent(id)}/status-fragment` +
          (varian ? `?tampilan=${encodeURIComponent(varian)}` : "");
        fetch(url, { credentials: "same-origin", headers: { Accept: "text/html" } })
          // Sesi habis: fetch mengikuti redirect ke /login; halaman login
          // tidak boleh masuk ke kotak status.
          .then((r) => (r.ok && !r.redirected ? r.text() : Promise.reject(new Error(String(r.status)))))
          .then((html) => {
            if (terakhir.get(kunci) !== ini) {
              return;
            }
            for (const el of els) {
              el.innerHTML = html;
            }
          })
          .catch(() => {}); // fragmen lama tetap tampil; event berikutnya mencoba lagi
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", pasangStatusLangsung);
  } else {
    pasangStatusLangsung();
  }

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
