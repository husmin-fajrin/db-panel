// Pengalih tema dbpanel, dimuat sinkron di <head> sebelum stylesheet supaya
// halaman langsung tampil dengan tema yang benar (tanpa kedip terang ke
// gelap). Urutannya: pilihan pengguna di peramban ini (localStorage.tema,
// "gelap" atau "terang") menang; selain itu nilai awal dari Pengaturan
// panel (<html data-tema-bawaan>): "gelap" atau "terang" dipakai apa
// adanya, "sistem" (bawaan) mengikuti prefers-color-scheme sistem operasi.
// Nilai lain di localStorage diabaikan. Tombol di sidebar (komponen
// themeToggle di app.js) menulis localStorage.tema dan mengganti atribut.
(() => {
  "use strict";
  const root = document.documentElement;
  let tema = null;
  try {
    tema = window.localStorage.getItem("tema");
  } catch (e) {
    // Penyimpanan diblokir (mode privat ketat): pakai nilai awal.
  }
  if (tema !== "gelap" && tema !== "terang") {
    const temaBawaan = root.dataset.temaBawaan;
    if (temaBawaan === "gelap" || temaBawaan === "terang") {
      tema = temaBawaan;
    } else {
      // Tanpa matchMedia (peramban sangat lama): gelap, tema utama panel.
      const gelap = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)").matches : true;
      tema = gelap ? "gelap" : "terang";
    }
  }
  root.dataset.theme = tema === "gelap" ? "dbpanel-gelap" : "dbpanel-terang";
})();
