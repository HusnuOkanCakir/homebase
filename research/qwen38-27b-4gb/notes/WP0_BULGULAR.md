# WP0 — 27B'nin gercek olculeri, ve plani neyin kapattigi

Bu belge iki is yapar: onerilen compression planinin aritmetigini gercek
agirliklara karsi dogrular, ve plani calistirmanin ne kadar compute istedigini
hesaplar. Ikincisi projenin kapsamini belirleyen sayidir.

## 1. Parameter breakdown dogrulandi

Model kartina degil, indirilen GGUF'un tensor geometrisine bakildi
(`Qwen3.8-27B-UD-IQ2_XXS.gguf`, 851 tensor, 50 metadata anahtari).

| Bilesen | Planin iddiasi | GGUF'tan olculen |
|---|---:|---:|
| FFN | 17,113 B | **17,113 B** |
| DeltaNet | 5,562 B | **5,562 B** |
| Full attention | 1,678 B | **1,678 B** |
| Embedding | 1,271 B | **1,271 B** |
| LM head | 1,271 B | **1,271 B** |
| **Toplam** | **26,896 B** | **26,896 B** |

Mimari de dogrulandi: `block_count 64`, `embedding_length 5120`,
`feed_forward_length 17408`, `head_count 24`, `head_count_kv 4`,
`key_length 256`, `context_length 262144`. Tensor sayilari 48 SSM ve 16 full
attention katmani veriyor; yani 3:1 makroblok deseni dosyada gorunuyor.

`attn_q` 16 katmanda 1,0066 B, yani katman basina 62,9 M = 5120 x 12288. Q
projection'in ciktisinin iki kat olmasi — gated attention — dogru.

KV cache hesabi da dogru: 16 x 4 x 256 x 2 x 2 = **64 KiB/token**.

**Sonuc: planin sayilari dogru.** Bu onemli, cunku bundan sonrasi sayilarla
degil kapsamla ilgili.

## 2. Quantization zaten tabanda; kazanc pruning'den gelecek

Elimizdeki dosya 7.266.070.528 byte ve 26,896 B parametre tasiyor:

    2,161 bit/parametre

Planin prune edilmis student icin hedefi 2,29 bit/parametre idi. Yani **Unsloth'un
dinamik quant'i bugun, prune edilmemis tam 27B uzerinde, planin hedefledigi
bit oranindan daha iyisini yapiyor.**

Bunun anlami net: 4 GiB'e sigmayi saglayacak sey quantization degil, parametre
sayisini dusurmektir. 27B x 2,16 bit = 6,77 GiB (sigmiyor); 9,9B x 2,29 bit =
2,64 GiB (siginiyor). Fark tamamen pruning'den geliyor.

## 3. Plani calistirmanin maliyeti

Distillation FLOP'lari standart tahminle: student forward+backward ~6NT,
teacher forward ~2NT. Token butceleri Minitron'un Llama-3.1 8B -> 4B icin
kullandigi ~94B token mertebesinden alindi.

| Asama | Token | FLOP |
|---|---:|---:|
| Stage A 27B -> 18B distill | 100 B | 1,62e22 |
| Stage B 18B -> 9,9B distill | 100 B | 1,13e22 |
| 2-bit QAT continual pretrain | 50 B | 3,96e21 |
| **Toplam** | | **3,14e22** |

Donanima karsi:

| Donanim | Sure |
|---|---:|
| Bu sunucunun CPU'su (i7-4700HQ, AVX2) | ~14.000 yil |
| Tek RTX 3090 | ~7 yil |
| Tek A100 | ~8 yil |
| 8x A100 node | ~1 yil |
| 64x A100 kume | ~46 gun |

**Plan bilimsel olarak saglam ve sifir butceyle calistirilamaz.** Bu bir
elestiri degil, kapsam tespitidir: onerilen sey fonlanmis bir lab projesidir.
Laboratuvarin kendi kurali "ucretli API yok, kredi alimi yok" ise, WP1–WP3
(pruning ve QAT) bu kural altinda **bloke** kabul edilmelidir.

## 4. Sifir butceyle yapilabilecek olan

Plandan geriye kalan ve gercekten yapilabilir olan kisim kucuk degil:

- **WP0 profiling ve traffic model.** Bu belge onun ilk yarisi.
- **Var olan IQ2_XXS'i olcmek.** Plandaki "2-bit 27B" teorik degil; dosya
  diskte duruyor. Token basina tasinan byte, TTFT, decode hizi, tepe RSS
  olculebilir.
- **KV cache quantization.** llama.cpp'de hazir (`--cache-type-k/v`). Planin
  KV4/KV2 ablasyonu bugun, egitim olmadan olculebilir.
- **Kucuk modelleri student vekili olarak olcmek.** Qwen3 4B/8B zaten dagitilmis
  durumda; "9,9B student ne yapardi" sorusuna egitim yapmadan yaklasik cevap
  verirler.

Yapilamayacak olan: pruning, distillation, QAT. Bunlar kume ister.

## 5. 27B bu makinede olculdu: 0,61 tok/s

Uc denemede "takildi" sanilan sey donanim degildi, benim iki hatamdi:

- **Baglam sinirlanmamisti.** Model native 262.144 token; 64 KiB/token ile bu
  KV cache tek basina ~16 GiB, makinenin RAM'inden buyuk. Sureci 14,1 GiB RSS
  ile bulmamin sebebi buydu.
- **`llama-cli` interaktif moda dustu** ve `ssh -n` stdin'i kapattigi icin
  bekledi. %0 CPU'da 22 dakika duran sey hesap yapmiyordu, girdi bekliyordu.

`llama-bench` ile dogru calistirildiginda kosu **110 saniye** surdu:

| olcum | deger |
|---|---:|
| prompt (pp32) | 0,71 t/s |
| decode (tg16) | **0,61 t/s** |
| model | 6,76 GiB, 2,161 bit/parametre |
| 100 token'lik bir cevap | 164 saniye |

**Laboratuvarin kendi kapisi >= 1 tok/s decode. 0,61 ile gecmiyor.**

### Darbogac disk degil, bant genisligi

Model RAM'e alindiktan sonra disk okumasi 50 KB/s'ye duser. Yani onceki
disk hipotezim yanlisti; islem memory-bandwidth bound.

    6,76 GiB/token x 0,61 tok/s = 4,12 GiB/s etkin bant genisligi

i7-4700HQ'nun cift kanalli DDR3-1600 teorik tepesi ~25,6 GB/s. Yani tepe
degerin yalnizca **%17'sine** ulasiliyor. Aradaki fark IQ2_XXS acma
maliyetidir — ve bu, planin dusuk-bit kernel tasarimina neden bu kadar yer
ayirdigini dogrudan hakli cikaran sayidir.

### Planin student'i ne verirdi?

Ayni etkin bant genisligiyle, MC-9.9B (2,81 GiB agirlik + scale):

    4,12 / 2,81 = ~1,47 tok/s

Yani pruning gercekten ~2,4 kat kazandirir ve kapiyi ancak gecer. Bu makinede
rahat bir marj yok; plan bir 4 GiB GPU icin tasarlanmis, bu sunucunun CPU'su
icin degil.

## 6. Onceki disk hipotezi



Ilk hipotezim diskti: sunucunun diski 5400 rpm bir dizustu diski (ST1000LM024)
ve model mmap ile aciliyor. Olcum bunu **curuttu** — model bir kez RAM'e
alindiktan sonra disk okumasi ihmal edilebilir.

Yine de bir gercek pay var: ilk yukleme soguk cache ile plakadan 6,76 GiB
okumak demektir ve ayni mile dokuz uygulama vurur. Bu TTFT'yi etkiler,
decode'u degil. Plandaki hicbir tabloda depolama katmani yok; memory-centric
bir projede en azindan yukleme maliyeti bir satir hak ediyor.
