# Assessment: tampilan aktivitas agent dan persistensi login

Tanggal: 2026-11-03

## Referensi dan keadaan saat ini

Catatan pengguna mengusulkan satu kartu khusus untuk satu tool, grup yang dapat dibuka untuk beberapa tool dan thinking terkait, serta thinking di dekat jawaban jika tidak ada rangkaian tool. Screenshot memperlihatkan ringkasan durasi, daftar aktivitas berurutan, preview output terminal, detail input/output, dan status/exit code.

- [x] Thinking dan tool mengikuti urutan pesan dalam turn. Beberapa tool berurutan beserta thinking terkait ditampilkan dalam grup yang dapat dilipat; tool tunggal memiliki kartu sendiri. Durasi turn ditampilkan jika waktu awal dan akhirnya tersedia.
- [x] Kartu menampilkan status, preview output, input/output, diff lengkap, lokasi, dan exit code yang tersedia. Status buka kartu disimpan dalam tab browser agar bertahan saat reload.
- [x] Output terminal yang dibuat melalui ACP client diteruskan secara live ke tool yang merujuk `terminalId` dan tetap disimpan setelah terminal selesai/dilepas. Batas `outputByteLimit` yang diminta agent tetap berlaku untuk respons ACP, sedangkan timeline menyimpan aliran lengkap yang diterima client.
- [x] Snapshot tool, input, output, thinking, dan jawaban baru tidak lagi dipotong oleh batas lama; update snapshot dan terminal stream ditangani terpisah. Diff tidak lagi dipotong pada 160 baris.
- [ ] Agent yang menjalankan terminal sendiri tanpa mengirim output atau referensi terminal melalui ACP tidak menyediakan stream tersebut ke client. Data historis yang sudah terlanjur dipotong tidak dapat direkonstruksi dari penanda lama.
- [x] Fixture ACP menguji output sebelum dan setelah referensi tool, streaming sebelum exit, output besar, status keluar, dan perintah yang gagal. Tampilan grup dan detail diuji di browser pada ukuran normal dan sempit; login/restart Cursor diuji terpisah seperti di bawah.

## Dasar desain

1. Bentuk timeline berdasarkan `turnId`, urutkan event sebagaimana diterima, dan tampilkan ringkasan aktivitas yang bisa dilipat ketika ada beberapa tool. Satu tool tetap bisa punya kartu khusus. Kelompokkan thinking bersama aktivitas yang berhubungan; thinking tanpa tool berada di atas jawaban terkait. Jangan memindahkan event melewati batas turn atau menyembunyikan error dan permintaan approval.
2. Tampilkan judul, status berjalan/selesai/gagal, dan preview singkat; buka detail input, output, diff, lokasi, dan exit code jika memang tersedia. Sediakan akses ke konten lengkap dan pertahankan posisi scroll serta status terbuka saat streaming, reload, dan penggunaan layar sempit. Durasi hanya ditampilkan jika waktu awal dan akhir yang dapat dipercaya tersedia.
3. Bentuk kontrak event berdasarkan kemampuan tiap agent: bedakan snapshot ACP yang menggantikan nilai sebelumnya dari chunk inkremental yang perlu ditambahkan berdasarkan ID tool/terminal. Simpan data lengkap di penyimpanan yang sesuai dan batasi hanya preview/payload yang dikirim ke tampilan. Jangan menganggap semua agent mendukung stream terminal, timeout, proses background, atau pemeriksaan ulang berdasarkan ID; tampilkan hanya status dan aksi yang benar-benar disediakan.
4. Uji urutan thinking–multi-tool–jawaban, tool tunggal, beberapa update untuk tool yang sama, output besar, terminal berjalan/gagal, approval, reload, dan viewport kecil dengan fixture ACP serta agent nyata. Bandingkan input/output yang disimpan dengan yang ditampilkan, bukan hanya tampilan akhir.


## QA mobile dan output panjang (2026-11-03)

- [x] Browser responsif diuji pada viewport CSS 312×675, 312×352, 360×440 dan 800×360; viewport pendek mensimulasikan ruang yang tersisa saat keyboard muncul. Composer dan chat tetap berada di dalam viewport, termasuk saat Stop aktif dan nama agent panjang.
- [x] Fixture JSON satu baris berisi 394.924 karakter serta fixture mirip `docker build` berisi 1.000 baris (199.913 karakter) dibandingkan persis dengan isi DOM dan hasil salin ke clipboard. Menu **+** membuka pengaturan wrap, awal/akhir, dan salin; awal/akhir diuji untuk scroll vertikal dan satu baris horizontal.
- [x] Fixture ACP menjalankan terminal yang menghasilkan 220 baris bertahap. Output akhir sesuai dengan fixture, dan posisi scroll yang telah digeser tetap berada di tempat saat output bertambah.
- [x] Composer diuji dengan agent yang mengiklankan mode, 12 model, reasoning effort, boolean, dan ukuran context. Pergantian mode/model/effort/boolean, pencarian model, draft saat mengubah opsi, dan Stop diuji; menu pencarian tetap terbuka saat viewport berubah.
- [ ] Pengujian di atas memakai browser desktop yang diubah ukuran viewport-nya. Belum ada pengujian langsung pada Android, keyboard Android, atau lingkungan NusaDesk/PRoot karena perangkat tidak terhubung.

## Login Cursor: pemeriksaan langsung

- [x] Cursor CLI menyimpan kredensial pada penyimpanan lokalnya. Pada mesin uji Linux ini file `~/.config/cursor/auth.json` ada dan memiliki mode `600`; isinya tidak diperiksa atau disalin. Lokasi dapat berbeda bila `XDG_CONFIG_HOME` berbeda.
- [x] Setelah satu kali pemulihan login, jalankan NusaMation dengan data uji terisolasi dan tanpa `CURSOR_API_KEY`. Sesi Cursor ACP berhasil disiapkan dan menjawab `halo`; hentikan server dengan SIGTERM, jalankan kembali dengan data yang sama, siapkan kembali sesi, dan kirim `halo` lagi. Kedua turn selesai dengan jawaban, tanpa login ulang.
- [x] Penghentian runtime NusaMation menutup proses ACP; pemanggilan logout adalah jalur terpisah. Environment proses induk dan pengaturan per agent diteruskan ke proses ACP.
- [ ] Belum diuji persistensi saat reboot OS, perubahan HOME/XDG/user, pencabutan atau kedaluwarsa kredensial, maupun semua metode login agent lain. Kredensial yang tidak lagi valid tetap dapat memerlukan autentikasi ulang dari penyedia.
- [ ] Metode autentikasi `cursor_login` dapat membuka browser oleh agent. Pengalaman login ketika browser tidak tersedia atau sesi kedaluwarsa perlu diuji terpisah; ACP tidak selalu menyediakan URL atau prompt untuk ditampilkan di popup aplikasi.

Selama investigasi, login CLI yang semula tercatat aktif menjadi tidak aktif setelah pengujian awal. Penyebab penghapusan file lama belum teridentifikasi. Login dipulihkan melalui alur resmi Cursor sebelum uji restart di atas; tidak ada perintah logout yang dijalankan oleh skenario uji.
