# Qwen3.8-27B / 4 GB VRAM araştırma laboratuvarı

Bu dizin, Qwen3.8-27B'yi eski 4 GB NVIDIA GPU'lu sunucuda dürüstçe sınamak
için Homebase uygulamasından ayrılmış, sıfır bütçeli bir araştırma alanıdır.
Henüz ölçülmüş bir başarı veya üretime hazır entegrasyon yoktur.

## Kısa karar

Qwen3.8-27B'nin tek parça 2 bit GGUF'u bile 7,27–10,32 GB aralığındaki ilk
Q2 adaylarıyla 4 GB VRAM'e bütünüyle sığmaz. İlk gerçekçi yol `llama.cpp` ile
ağırlıkların çoğunu sistem RAM'inde tutup az sayıda katmanı GPU'ya aktarmaktır.
4 GB hedefi “tam GPU yerleşimi” değil, CPU + sınırlı GPU offload deneyidir.
Daha agresif hedef için [Candidate A](notes/CANDIDATE_A.md) yaklaşık 9,89B
parametreli, henüz eğitilmemiş bir mimari hipotezidir.

İdeal metadata'sız hesapta bile 27B × 1,58 bit yaklaşık 5,33 GB, 1 bit ise
3,38 GB'dır. İkinci sayı ölçekler, GDN state, KV cache ve CUDA workspace için
yer bırakmadığından “1-bit ile 4 GB'a sığar” sonucu çıkarılamaz.

Bu, MoE değil yoğun bir metin omurgasıdır: 64 katmanın 48'i Gated DeltaNet,
16'sı full-attention'dır ve metin ağırlıkları her token'da kullanılır. Bu yüzden
uzman-offload belleği çözmez. Mimari kaynak resmî
[`config.json`](https://huggingface.co/Qwen/Qwen3.8-27B/blob/main/config.json)
dosyasıdır.

## Kesin kapsam

- Metin girişi/çıkışı; vision projector indirilmez ve çalıştırılmaz.
- MTP/speculative draft ağırlıkları indirilmez ve çalıştırılmaz.
- 262K bağlam denenmez. İlk turlar 2K, sonra en fazla 4K bağlamdır.
- Homebase kodu, servisleri, mağaza manifestleri ve yedekleme kodu değişmez.
- Ücretli API, kredi satın alma, Colab ücretli yükseltmesi ve otomatik harcama yoktur.
- İlk adaylar kilitli Unsloth Q2/Q3 GGUF'larıdır. ggml-org Q4 yalnız referanstır.

Model adı belirsiz değildir: burada resmî
[`Qwen/Qwen3.8-27B`](https://huggingface.co/Qwen/Qwen3.8-27B) kullanılır.
Resmî yapılandırma
[`config.json`](https://huggingface.co/Qwen/Qwen3.8-27B/blob/main/config.json),
kaynak kod [`QwenLM/Qwen3.8`](https://github.com/QwenLM/Qwen3.8) ve lisans
Apache-2.0'dır. Hugging Face tarafındaki `qwen3_5` mimari etiketi, Qwen3.8'in
Qwen3.5 altyapısını yeniden kullanmasındandır; başka model seçildiği anlamına
gelmez.

## Başlangıç sırası

1. `config/models.lock.json` içindeki revision, boyut ve SHA-256 değerlerini
   değiştirmeden envanteri doğrula. Unsloth `main` dosya sildi/değiştirdiği için
   adaylar, hepsinin birlikte bulunduğu immutable
   `313447f257f7ebde0b968e4778feef774546ed81` revision'ına kilitlidir.
2. Önce 563 MB `qwen35-0.8b-ggml-q4-0-sanity` artefaktını fetch et. CPU ve
   CUDA/auto yollarında coherent, boş olmayan çıktı ve sıfır crash/OOM görmeden
   27B'ye geçme. Bu yalnız Gated DeltaNet runtime sanity'sidir, kalite vekili
   değildir.
3. Colab'da `notebooks/colab_zero_budget_probe.ipynb` dosyasını aç. Varsayılan
   mod büyük model indirmez; donanımı ölçer ve küçük yerel surrogate çalıştırır.
4. Q4 referansını yalnız ücretsiz oturum en az 15 GB VRAM gösteriyorsa, yeterli
   RAM/disk varsa ve kullanıcı `REQUEST_Q4_REFERENCE = True` yaparsa indir.
   Notebook runtime kurmaz: b10549 build yolları verilmeli ve `--version`
   attestasyonu geçmelidir. 15 GB kapısı tam VRAM yerleşimini garanti etmez;
   18,97 GB dosya `--n-gpu-layers auto --fit on --fit-target 1024` ile
   RAM/VRAM'e yerleştirilir.
5. Sunucuda önce `probe`/`doctor` çıktısını kaydet. Sırayı koru: UD-IQ2_M
   CPU-only doğruluk tabanı, UD-IQ2_M CUDA auto-offload, IQ2_XXS, IQ3_XXS ve
   Q3_K_M. 2K kapısı geçmeden 4K'ya çıkma.
6. Her dosyayı çalıştırmadan önce `sha256sum` ile kilit manifestine karşı
   doğrula. Model kartının hareketli `main` dalına güvenme.

Örnek doğrulama:

```bash
python3 -m json.tool config/models.lock.json >/dev/null
python3 -m json.tool notebooks/colab_zero_budget_probe.ipynb >/dev/null
sha256sum /srv/qwen-lab/models/qwen38-27b-unsloth-ud-iq2-m/Qwen3.8-27B-UD-IQ2_M.gguf
```

## Yerel çalıştırma özeti

Komutlar laboratuvar kökünden çalışır. `build` paket kurmaz; hedef makinede
CUDA 12.9, R580 sürücüsü, CMake ve derleyici önceden hazır olmalıdır. Her ağ
indirmesi ayrı ve açık bir `fetch` komutudur.

```bash
cd /home/okan/HomeServer/research/qwen38-27b-4gb
make check

./bin/qwen-lab --data-dir /srv/qwen-lab probe \
  --output /srv/qwen-lab/results/hardware.json
./bin/qwen-lab --data-dir /srv/qwen-lab doctor \
  --output /srv/qwen-lab/results/doctor.json
./bin/qwen-lab --data-dir /srv/qwen-lab build --dry-run
./bin/qwen-lab --data-dir /srv/qwen-lab build

./bin/qwen-lab --data-dir /srv/qwen-lab fetch \
  qwen35-0.8b-ggml-q4-0-sanity
./bin/qwen-lab --data-dir /srv/qwen-lab sanity --approve

./bin/qwen-lab --data-dir /srv/qwen-lab fetch \
  qwen38-27b-unsloth-ud-iq2-m
./bin/qwen-lab --data-dir /srv/qwen-lab bench \
  qwen38-27b-unsloth-ud-iq2-m --case 0 --dry-run
./bin/qwen-lab --data-dir /srv/qwen-lab bench \
  qwen38-27b-unsloth-ud-iq2-m --case 0
./bin/qwen-lab --data-dir /srv/qwen-lab serve \
  qwen38-27b-unsloth-ud-iq2-m --optimization-profile baseline
```

`sanity --approve`, iki ayrı loopback `llama-server` sürecindeki CPU ve CUDA
Gated DeltaNet yanıtları geçmeden 27B `serve`/`bench` çalıştırmalarının kilidini
açmaz. Önce tek `--case`, sonra sınırlandırılmış `--max-cases` kullanın; 352
vakalık matris açık `--confirm-full-matrix` olmadan başlamaz. Sunucu varsayılan
olarak yalnız `127.0.0.1:8088` üzerinde dinler.

Sabit değerlendirme ve 30 dakikalık kabul koşusu için
[`eval/README.md`](eval/README.md) izlenir. `--server-pid` ve
`--nvidia-smi-telemetry` tanısal koşularda isteğe bağlıdır fakat ikisi olmadan
kabul sonucu PASS olamaz. Uncensored/topluluk türevleri yalnız resmî checkpoint
baseline'ı kaydedildikten sonra, dışarıda oluşturulmuş ağsız, salt-okunur,
host-mount'suz ve araçsız sandbox kanıtıyla değerlendirilir; evaluator bu
sandbox'ı oluşturmaz.

Q4 KV, n-gram veya prompt-cache profili denenirken sunucu ilgili
`--optimization-profile` ile yeniden başlatılır ve evaluator'a aynı ad
`--runtime-profile` olarak verilir. `tools/compare_optimization.py`, bu koşuyu
aynı model/runtime/fixture'lardaki baseline ile eşler. Toplam uçtan uca kazanç
%10'un altında kalırsa veya herhangi bir eşlenmiş görevde kalite düşerse profil
ret edilir.

## Deney matrisi

| Aşama | Model/mod | Bağlam | Amaç | Geçiş kapısı |
|---|---|---:|---|---|
| S | Qwen3.5-0.8B Q4_0, CPU/CUDA | 512 | GDN sanity | coherent; 0 crash/OOM; aksi halde bloklu |
| 0 | Colab surrogate | 512 | kayıt, resume ve ölçüm hattı | geçerli JSON sonuç |
| 1 | UD-IQ2_M, yalnız CPU | 2K | güvenli mmap/RAM tabanı | yükleme + 20 kısa istem, OOM yok |
| 2 | UD-IQ2_M, CUDA auto/offload | 2K | Pascal offload sınırı | aynı istemler ve ayarlar |
| 3 | UD-IQ2_XXS | 2K | daha küçük Q2 karşılaştırması | aynı istemler ve ayarlar |
| 4 | UD-IQ3_XXS | 2K | ilk Q3 kalite/bellek noktası | host RAM ve gecikme kabul edilebilir |
| 5 | Q3_K_M | 2K | Q3 üst sınırı | host RAM ve gecikme kabul edilebilir |
| 6 | seçilen aday | 4K | KV/state baskısı | tekrarlanabilir 3 koşu |
| R | ggml Q4_K_M | 2K | yüksek bellekte referans | yalnız VRAM >=15 GB ve yeterli host RAM |

Her turda model SHA, `llama.cpp` commit'i, komut, GPU/RAM, bağlam, GPU katman
sayısı, prompt/decode tok/s, tepe VRAM/RSS ve kalite notu kaydedilir. Aynı anda
yalnız bir değişken değiştirilir. Örnek sonuç dosyası ölçüm değildir:
[`results/sample-result.json`](results/sample-result.json).

Kabul kapıları önceden sabittir: indirilen her dosyada revision, byte boyutu ve
SHA-256 %100 eşleşmeli; sıcak median decode en az 1 tok/s ve 512-token istemde
TTFT en fazla 30 saniye olmalıdır. Otuz dakikalık soak boyunca OOM/crash sayısı
sıfır, warm-up sonrası swap artışı en fazla 256 MiB, kullanılabilir host RAM en
az 1,5 GiB ve sürekli major page fault olmamalıdır. Türkçe ve İngilizce ayrı
hesaplanır; her biri erişilebilen en iyi Q4 referans kalite skorunun en az %85'ini
korumalıdır. Eşzamanlı Homebase sağlık probe'ları hatasız olmalı ve p95 API
gecikmesi %20'den fazla bozulmamalıdır. Tepe VRAM/RSS ve sıcaklık ayrıca
kaydedilir. 2K kapıları geçmeden 4K denenmez; bu değerler başarı iddiası değil,
go/no-go ölçütüdür.

Q2 kalite kapısını geçemez ve Q3 de bellek/hız kapılarını karşılayamazsa
sonuç, “27B bu 16+4 GB donanımda kullanılabilir servis değildir” olarak
kaydedilir. Disk swap/weight streaming ile servis sonucu zorlanmaz.

## Sunucu depolaması ve Pascal notu

Model/cache kökü `/srv/qwen-lab` olmalıdır. Bu dizini Homebase'in uygulama veri
dizinine symlink etmeyin: 10–20 GB modeller yedekleme kapsamına girerse alan ve
süre büyür. Yedekleme politikasında `cache`, `models` ve `checkpoints` açıkça
hariç tutulmalı; yalnız küçük manifest ve sonuç özeti yedeklenmelidir.

Hedef Pascal GPU, R580 sürücüsü ve CUDA 12.9 kombinasyonudur. Sürücü/toolkit
görünmesi modern FP8/FP4 veya FlashAttention çekirdeklerinin Pascal'da çalıştığı
anlamına gelmez. `llama.cpp` gerçek compute capability ile sınanmalı; CUDA yolu
başarısız olursa CPU tabanı korunmalıdır. Kararı gerçek derleme ve
`--list-devices` çıktısı verir.

## Yol haritası

- P0 — kilitli artefaktlar, donanım envanteri, surrogate notebook.
- P1 — Q2/Q3 CPU + kısmi GPU offload; 2K bağlam.
- P2 — seçilen quant için katman, batch ve KV türü taraması; ardından
  Q4 KV, ağırlıksız n-gram speculative decoding ve kısa prompt-cache A/B
  testleri. Her biri uçtan uca en az %10 kazandırmıyorsa kaldırılır.
- P3 — sabit Türkçe/İngilizce görev setiyle kalite ve stabilite karşılaştırması.
- P4 — Candidate A için küçük ölçekli katman duyarlılığı ve distillation prototipi.
- P5 — ancak kapılar geçilirse localhost OpenAI-uyumlu servis tasarımı; Homebase entegrasyonu ayrı karar/PR.

Araştırma kararlarının kaynakları
[`notes/PAPER_DECISIONS.md`](notes/PAPER_DECISIONS.md), mimari bütçe
[`notes/CANDIDATE_A.md`](notes/CANDIDATE_A.md), entegrasyon engelleri ise
[`notes/HANDOFF.md`](notes/HANDOFF.md) içindedir.

## English summary

This is a zero-cost, text-only research sandbox. A 27B checkpoint cannot reside
in 4 GB VRAM; the near-term path is CPU RAM plus limited Pascal GPU offload.
Vision, MTP, 262K context and Homebase integration are deferred, and no
benchmark success is claimed.
