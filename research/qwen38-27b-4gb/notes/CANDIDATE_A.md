# Candidate A: yaklaşık 9,89B metin omurgası

Candidate A çalışır bir checkpoint değildir. 4 GB sınırına yaklaşmak için katman/head/width pruning, weight tying, distillation ve karma hassasiyet gerektiren bir araştırma hipotezidir. Vision ve MTP yoktur.

## Mimari taslak

| Özellik | Resmi 27B metin omurgası | Candidate A |
|---|---:|---:|
| Katman | 64 | 48 |
| Makroblok | 16 × (3 GDN + 1 attention) | 12 × (3 GDN + 1 attention) |
| Hidden | 5120 | 4096 |
| FFN | 17408 | 9728 |
| Attention Q/KV head | 24 / 4 | 16 / 4 |
| Attention head dim | 256 | 256 |
| GDN key/value head | 16 / 48 | 16 / 32 |
| GDN head dim | 128 | 128 |
| Vocab | 248320 | 248320 |
| Embedding / LM head | untied | tied |

## Parametre bütçesi

Aşağıdaki sayılar config'ten türetilmiş yaklaşık tasarım hesabıdır; gerçek uygulamada bias, convolution/state ve backend tensor ayrıntılarıyla küçük farklar olabilir.

| Bileşen | Hesap | Parametre |
|---|---|---:|
| Tied embedding/head | 248320 × 4096 | 1,017,118,720 |
| 48 FFN | 48 × 3 × 4096 × 9728 | 5,737,807,872 |
| 36 GDN | 36 × yaklaşık 67,403,968 | 2,426,542,848 |
| 12 full attention | 12 × yaklaşık 58,728,448 | 704,741,376 |
| Normlar | yaklaşık | 397,312 |
| **Toplam** |  | **9,886,608,128 (~9,89B)** |

Ortalama 2,3 bit/weight ideal ham ağırlık bütçesi 2,842 GB (2,647 GiB) olur. Buna quant tabloları/metadata, daha yüksek hassasiyette hassas tensorlar, runtime workspace, KV ve GDN state eklenir. 4K BF16 KV yaklaşık 192 MiB; GDN FP32 state kaba hesabı yaklaşık 72 MiB/sequence'dır. Dolayısıyla toplamın 4 GiB altında kalması olası fakat garanti değildir; kernel ve allocator ölçümü gerekir.

## Hassasiyet planı

1. Küçük surrogate üzerinde ölçüm hattını doğrula; seed, veri ve commit'i kilitle.
2. Her resmi makroblok için teacher/student hidden cosine, output KL ve held-out NLL çıkar.
3. İlk/son makroblokları, normları, embedding/head'i ve GDN state yolunu daha yüksek bitte tut.
4. Tek değişkenli ablation uygula: depth 64→56→48; hidden; FFN; Q head; GDN value head; weight tying.
5. Her pruning adımından sonra kısa distillation/recovery; yalnız sonra bit tahsisi.
6. 2,0/2,3/2,6 bpw profillerini aynı kalibrasyon ve görev setinde karşılaştır.

Kalite kapıları Türkçe/İngilizce held-out loss, teacher KL, kod/araç çağrısı doğruluğu ve sabit kısa görev setidir. “Refusal azaldı” tek başına intelligence ölçütü değildir. Bellek kapısı tepe VRAM + host RSS; hız kapısı prompt/decode tok/s ve p95 gecikmedir.

## Sıfır bütçe fizibilitesi

Tam 27B teacher'dan 9,9B student eğitmek veya kapsamlı QAT/distillation yapmak 4 GB Pascal ve ücretsiz Colab süreleriyle gerçekçi değildir. Sıfır bütçede yapılabilecekler: parametre hesabı, küçük surrogate, az örnekli layer sensitivity, CPU'da sıralı/streamed kalibrasyon ve hazır GGUF baseline ölçümü. Tam pruning + recovery checkpoint'i bağışlanmış/harici compute olmadan **bloke** kabul edilir. Bu belge benchmark veya eğitilmiş model iddiası taşımaz.

Yeterli, ücretsiz/hibe edilmiş çoklu yüksek bellekli GPU kaynağı bulunursa sıra
değişmez: yapılandırılmış pruning → kısa distillation pilotu → 3-bit QAT →
2-bit QAT. Basit 2-bit PTQ, bu sırayı atlamak için yeterli kanıt sayılmaz.

Gelecekteki go/no-go hedefi; en fazla 3,0 GiB GGUF, en fazla 3,6 GiB toplam
VRAM, 2K bağlamda en az 1 tok/s ve resmî teacher genel-asistan bileşik skorunun en
az %80'idir. Bu hedeflerin tamamı aynı checkpoint/runtime kombinasyonunda
sağlanmadıkça Candidate A “tam-VRAM student” olarak adlandırılmaz.
