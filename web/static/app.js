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

  // fokuskan memberi fokus ke el. Dialog daisyUI (.modal) baru terlihat
  // satu frame setelah showModal() (transisi visibility), jadi focus()
  // tepat sesudahnya tidak berhasil; dicoba lagi tiap frame, paling banyak
  // 20 kali.
  function fokuskan(el, sisa = 20) {
    el.focus();
    if (document.activeElement !== el && sisa > 0 && el.isConnected) {
      requestAnimationFrame(() => fokuskan(el, sisa - 1));
    }
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
          const info = badges[st] || { Label: st, Class: "lencana-neutral" };
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
            line.classList.add("text-error-text");
          } else if (d.level === "warn") {
            line.classList.add("text-warning-text");
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
            // Ikon dan sorotan langkah mengikuti statusnya (app.css,
            // data-langkah).
            // d.step datang dari SSE: di-escape agar tanda kutip di nama
            // langkah tidak memecah selektor dan menghentikan pembaruan.
            const step = CSS.escape(String(d.step));
            const langkah = el.querySelector(`[data-step="${step}"]`);
            if (langkah) {
              langkah.dataset.langkah = d.step_status;
              const isi = langkah.querySelector("[data-step-isi]");
              if (isi) {
                isi.classList.toggle("bg-primary/5", d.step_status === "running");
              }
            }
            const badge = el.querySelector(`[data-step-badge="${step}"]`);
            if (badge) {
              setBadge(badge, d.step_status);
            }
            const msg = el.querySelector(`[data-step-message="${step}"]`);
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
            // Lencana pekerjaan ada di kepala halaman, di luar elemen ini.
            const badge = document.querySelector("[data-job-badge]");
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
        return { "lencana-success": this.online, "lencana-error": !this.online };
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
    // di dialog mengirim formulir itu; bila datanya ikut dihapus, tombol itu
    // meminta nama replika (data-server-name formulir) diketik.
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
      // Nama replika yang harus diketik di dialog konfirmasi, hanya bila
      // datanya ikut dihapus (spec 5.6); kosong: tidak ada nama diminta.
      get confirmName() {
        return this.dropData ? this.$refs.form.dataset.serverName || "" : "";
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

    // "Jangan ganggu" (spec 5.7, R-043): tombolnya membuka dialog
    // (x-ref="dialog") yang menyebut peringatan yang tetap dikirim selama
    // senyap. Dialog ada di dalam formulir senyap; tombol kirimnya
    // mengirim formulir itu (POST biasa, tanpa token), "Batal" menutup
    // dialog lewat formmethod="dialog". Membatalkan senyap tidak lewat sini.
    window.Alpine.data("confirmSnooze", () => ({
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
      nameLabel: "Ketik nama server",
      typed: "",
      replicaName: "",
      typedReplica: "",
      error: "",
      busy: false,
      danger: false,
      tombol: null,
      // sumber: dialog lain yang terbuka saat tombol diklik (mis. "Lepas
      // replika?"); ditutup selama konfirmasi agar tidak bertumpuk, lalu
      // dibuka lagi bila konfirmasi dibatalkan.
      sumber: null,
      init() {
        this.$root.addEventListener("close", () => this.kembali());
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
        // Destructive actions (data-confirm-danger="1") get the pink
        // confirm button and a framed warning; the rest primary.
        this.danger = btn.dataset.confirmDanger === "1";
        this.name = btn.dataset.confirmName || "";
        this.nameLabel = btn.dataset.confirmNameLabel || "Ketik nama server";
        this.replicaName = btn.dataset.confirmReplicaName || "";
        this.typed = "";
        this.typedReplica = "";
        this.$refs.typedReplica.value = "";
        this.error = "";
        this.busy = false;
        this.$refs.typed.value = "";
        this.$refs.password.value = "";
        const sumber = btn.closest("dialog[open]");
        this.sumber = sumber && sumber !== this.$root ? sumber : null;
        if (this.sumber) {
          this.sumber.close();
        }
        this.$root.showModal();
        fokuskan(this.name ? this.$refs.typed : this.replicaName ? this.$refs.typedReplica : this.$refs.password);
      },
      input() {
        this.typed = this.$refs.typed.value;
        this.typedReplica = this.$refs.typedReplica.value;
      },
      close() {
        this.$refs.password.value = "";
        this.$root.close();
      },
      // kembali: dialog konfirmasi tertutup (Batal, Esc, atau setelah
      // unduhan): dialog sumbernya dibuka lagi dan fokus kembali ke tombol
      // yang membukanya.
      kembali() {
        const sumber = this.sumber;
        this.sumber = null;
        if (sumber && !sumber.open && sumber.isConnected) {
          sumber.showModal();
        }
        if (this.tombol && this.tombol.isConnected) {
          fokuskan(this.tombol);
        }
      },
      get tombolClass() {
        return { "btn-error": this.danger, "btn-primary": !this.danger };
      },
      get teksClass() {
        return { "kotak-bahaya": this.danger };
      },
      get nameClass() {
        return { hidden: this.name === "" };
      },
      get replicaNameClass() {
        return { hidden: this.replicaName === "" };
      },
      get blocked() {
        return this.busy || (this.name !== "" && this.typed !== this.name) ||
          (this.replicaName !== "" && this.typedReplica !== this.replicaName);
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
            if (this.replicaName !== "") {
              isiTersembunyi(form, "confirm_replica_name", this.typedReplica);
            }
            this.$refs.password.value = "";
            form.submit();
            // Unduhan (formulir GET) tidak berpindah halaman: tutup dialog
            // agar tombolnya bisa dipakai lagi (dengan token baru).
            if (form.method === "get") {
              this.busy = false;
              this.close();
            }
          })
          .catch((err) => {
            this.error = err.message;
            this.busy = false;
          });
      },
    }));

    // Pemilih kandidat di layar pertimbangan promote (promote.html):
    // memilih replika menampilkan kotak merahnya (data-promote-box: replika
    // sebagian atau yang tidak menjawab pemeriksaan) dan menyembunyikan
    // kotak replika lain, lalu memindahkan nama yang harus diketik
    // (data-name: master lama, data-replica-name: replika) dan kalimat
    // konfirmasi (data-text) radio itu ke tombol Promote yang membuka
    // dialog konfirmasi. Kotak "tidak terjangkau" replika terpilih
    // disembunyikan.
    window.Alpine.data("promotePicker", () => ({
      init() {
        const form = this.$el;
        const perbarui = () => {
          const pilih = form.querySelector('input[name="candidate"]:checked');
          const id = pilih ? pilih.value : "";
          // Baris replika terpilih disorot (bg-primary/5).
          form.querySelectorAll("[data-candidate-row]").forEach((el) => {
            el.classList.toggle("bg-primary/5", el.dataset.candidateRow === id);
          });
          form.querySelectorAll("[data-promote-box]").forEach((el) => {
            el.classList.toggle("hidden", el.dataset.promoteBox !== id);
          });
          // Kotak "tidak terjangkau" replika yang sedang dipilih tidak
          // berlaku: replika itu tidak akan diarahkan ulang.
          form.querySelectorAll("[data-unreachable]").forEach((el) => {
            el.classList.toggle("hidden", el.dataset.unreachable === id);
          });
          const tombol = form.querySelector("[data-confirm-action]");
          if (tombol && pilih) {
            tombol.dataset.confirmName = pilih.dataset.name || "";
            tombol.dataset.confirmReplicaName = pilih.dataset.replicaName || "";
            tombol.dataset.confirmText = pilih.dataset.text || "";
          }
        };
        form.addEventListener("change", (e) => {
          if (e.target.name === "candidate") {
            perbarui();
          }
        });
        perbarui();
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

    // Tombol tema di sidebar: berganti antara gelap dan terang, menyimpan
    // pilihan di localStorage.tema (dibaca theme.js di halaman berikutnya)
    // dan langsung mengganti data-theme di <html>.
    window.Alpine.data("themeToggle", () => ({
      gelap: true,
      init() {
        this.gelap = document.documentElement.dataset.theme !== "dbpanel-terang";
      },
      // label: aksi tombol, bukan tema yang sedang dipakai.
      get label() {
        return this.gelap ? "Tema terang" : "Tema gelap";
      },
      toggle() {
        this.gelap = !this.gelap;
        const tema = this.gelap ? "gelap" : "terang";
        try {
          window.localStorage.setItem("tema", tema);
        } catch (e) {
          // Penyimpanan diblokir: tema tetap berganti untuk halaman ini.
        }
        document.documentElement.dataset.theme = `dbpanel-${tema}`;
      },
    }));

    // Menu di telepon (< md): tombol di bar atas membuka dan menutup
    // sidebar (#menu-utama lewat atribut data-terbuka); Escape menutupnya.
    window.Alpine.data("sideMenu", () => ({
      open: false,
      toggle() {
        this.open = !this.open;
      },
      close() {
        this.open = false;
      },
    }));

    // Tab topologi di dashboard (lebih dari satu topologi): tombol
    // [role=tab] dengan data-tab="<id topologi>" menampilkan panel
    // #topologi-<id> dan menyembunyikan yang lain. Pilihan disimpan di
    // localStorage.topoTab dan dipulihkan saat halaman dibuka; id yang
    // tidak ada (topologi sudah dihapus) diabaikan. Panah kiri/kanan,
    // Home, dan End berpindah tab. Tanpa tablist (satu topologi) komponen
    // ini tidak melakukan apa-apa.
    window.Alpine.data("topoTabs", () => ({
      init() {
        const root = this.$el;
        const daftar = root.querySelector('[role="tablist"]');
        if (!daftar) {
          return;
        }
        const tabs = Array.from(daftar.querySelectorAll('[role="tab"]'));
        const tampilkan = (id, fokus) => {
          for (const t of tabs) {
            const aktif = t.dataset.tab === id;
            t.setAttribute("aria-selected", aktif ? "true" : "false");
            t.tabIndex = aktif ? 0 : -1;
            t.classList.toggle("btn-primary", aktif);
            t.classList.toggle("btn-ghost", !aktif);
            if (aktif && fokus) {
              t.focus();
            }
          }
          for (const p of root.querySelectorAll("[data-panel]")) {
            p.classList.toggle("hidden", p.dataset.panel !== id);
          }
        };
        const pilih = (id, fokus) => {
          tampilkan(id, fokus);
          try {
            window.localStorage.setItem("topoTab", id);
          } catch (e) {
            // Penyimpanan diblokir: pilihan berlaku untuk halaman ini saja.
          }
        };
        let simpanan = null;
        try {
          simpanan = window.localStorage.getItem("topoTab");
        } catch (e) {
          simpanan = null;
        }
        if (simpanan && tabs.some((t) => t.dataset.tab === simpanan)) {
          tampilkan(simpanan, false);
        }
        daftar.addEventListener("click", (e) => {
          const t = e.target.closest('[role="tab"]');
          if (t) {
            pilih(t.dataset.tab, false);
          }
        });
        daftar.addEventListener("keydown", (e) => {
          const i = tabs.indexOf(document.activeElement);
          if (i < 0) {
            return;
          }
          let j = -1;
          switch (e.key) {
            case "ArrowRight":
              j = (i + 1) % tabs.length;
              break;
            case "ArrowLeft":
              j = (i - 1 + tabs.length) % tabs.length;
              break;
            case "Home":
              j = 0;
              break;
            case "End":
              j = tabs.length - 1;
              break;
            default:
              return;
          }
          e.preventDefault();
          pilih(tabs[j].dataset.tab, true);
        });
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

    // Fragmen yang dimuat ulang berkala (layar pengalihan: koneksi
    // aplikasi tiap 15 detik): elemen x-data="pollFragment" dengan
    // data-url (fragmen HTML dari panel) dan data-every (milidetik) diisi
    // ulang dengan jawaban GET data-url. Tidak memuat saat tab tersembunyi;
    // jawaban yang datang setelah jawaban permintaan berikutnya dibuang.
    window.Alpine.data("pollFragment", () => ({
      timer: null,
      init() {
        const el = this.$el;
        const url = el.dataset.url;
        const every = Math.max(Number(el.dataset.every) || 15000, 5000);
        if (!url) {
          return;
        }
        let nomor = 0;
        const muat = () => {
          if (document.hidden) {
            return;
          }
          const ini = ++nomor;
          fetch(url, { credentials: "same-origin", headers: { Accept: "text/html" } })
            // Sesi habis: fetch mengikuti redirect ke /login; halaman login
            // tidak boleh masuk ke fragmen.
            .then((r) => (r.ok && !r.redirected ? r.text() : Promise.reject(new Error(String(r.status)))))
            .then((html) => {
              if (ini === nomor) {
                el.innerHTML = html;
              }
            })
            .catch(() => {}); // isi lama tetap tampil; giliran berikutnya mencoba lagi
        };
        this.timer = setInterval(muat, every);
      },
      destroy() {
        clearInterval(this.timer);
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
