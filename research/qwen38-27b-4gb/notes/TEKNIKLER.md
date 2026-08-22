# Hangi teknik bu makinede ise yarar — olculerek

Hedef degisti: yeniden egitim yok. O halde soru "hangi SOTA teknikleri
kullanalim" degil, "bu donanimda hangi teknik neyi degistirir" olmali.
Asagidakiler bu sunucuda olculdu.

## Yoneten denklem

    decode t/s  ≈  etkin bant genisligi / model boyutu (byte)

Bu makinede olculen etkin bant genisligi **quant formatina gore degisiyor**:

| Format | Etkin bant genisligi |
|---|---:|
| Q4_K_M | 16,9–17,2 GiB/s |
| Q4_0 | 14,5 GiB/s |
| IQ2_XXS | **4,1 GiB/s** |

IQ2_XXS'in acilmasi Q4_K_M'den ~4 kat pahali. Yani **daha az bit her zaman
daha hizli demek degil.** 2-bit'e inmek dosyayi kucultur ama bu makinede
dequant maliyeti kazanci yer.

## Donum noktasi — tek model uzerinde olculdu

Yukaridaki iki satir iki ayri modelden geliyordu (IQ2_XXS 27B'de, Q4_K_M
4B'de). Bu, iddiayi zayiflatiyordu: fark quant formatindan mi, model
boyutundan mi geldigi ayirt edilemiyordu. Egri simdi **tek model, tek agirlik
kaynagi, tek makine** uzerinde tamamlandi
([`results/quant-curve-4b.json`](../results/quant-curve-4b.json)):

| Quant | GiB | decode t/s | etkin GiB/s | tepe % |
|---|---:|---:|---:|---:|
| Q8_0 | 4,28 | 4,39 | 18,79 | 78,8 |
| Q6_K | 3,31 | 5,42 | 17,94 | 75,2 |
| Q5_K_M | 2,93 | 5,69 | 16,67 | 69,9 |
| **Q4_K_M** | 2,58 | **6,46** | 16,67 | 69,9 |
| IQ4_XS | **2,41** | 5,75 | 13,86 | 58,1 |

Sonuc: **IQ4_XS, Q4_K_M'den %6,6 daha kucuk ve %11,0 daha yavas.** En kucuk
dosya en hizlisi degil. Donum noktasi Q4_K_M'de ve IQ ailesine gecerken
gerceklesiyor — 2-bit'e kadar inmeye gerek yok, ucurum ilk IQ formatinda
basliyor.

Etkin bant genisligi bastan sona duserken (78,8 → 58,1) decode hizi Q4_K_M'ye
kadar yukseliyor: kucultmenin kazanci, acmanin maliyetinden buyuk kaldigi
surece. IQ4_XS'te bu iliski tersine donuyor.

IQ2_XXS bu egride yok: llama.cpp onu importance matrix olmadan uretmeyi
reddediyor. 27B'deki IQ2_XXS olcumu (tepe %17) baska bir model oldugu icin
egriye katilmadi.

> IQ4_XS bu depoda kilitli bir ucuncu-taraf artefakt degil; egrinin tamami tek
> agirlik kaynagindan gelsin diye kilitli Q8_0'dan `llama-quantize
> --allow-requantize` ile burada uretildi. **Olculen sey hizi**; kalitesi
> olculmedi ve importance matrix olmadan kotu olmasi beklenir.

## Olculen modeller

| Model | Quant | GiB | prompt t/s | decode t/s | 100 token |
|---|---|---:|---:|---:|---:|
| Qwen3.8-27B | IQ2_XXS | 6,76 | 0,71 | 0,61 | 164 s |
| Qwen3.8-4B distill | Q4_K_M | 2,58 | 17,27 | **6,55** | 15 s |
| Qwen3.8-2B distill | Q4_K_M | 1,21 | 48,72 | **14,19** | 7 s |
| Qwen3.5-0.8B | Q4_0 | 0,53 | 141,91 | 27,31 | 4 s |

4B, 27B'den **10,7 kat** hizli ve ayni ailenin distill edilmis hali.

## Teknik teknik degerlendirme

### FlashAttention — bu makinede uygulanabilir degil

FlashAttention bir GPU cekirdek teknigidir; degeri N×N attention matrisini
HBM'e yazmak yerine SRAM'de tile'lamaktan gelir. CPU'da o bellek hiyerarsisi
yok ve llama.cpp'nin CPU attention'i zaten blok blok cache icinde calisiyor.

Daha onemlisi, **olcum bunun onemsiz oldugunu soyluyor**. 27B'de token basina:

| Baglam | KV trafigi | toplam trafigin yuzdesi |
|---:|---:|---:|
| 2K | 0,125 GiB | %1,8 |
| 8K | 0,50 GiB | %6,9 |
| 32K | 2,00 GiB | %22,8 |
| 128K | 8,00 GiB | %54,2 |

2K baglamda tum attention trafigini sifirlasak kazanc **%1,8**. Attention
tarafindaki her optimizasyon ancak uzun baglamda anlam kazaniyor.

### PagedAttention / vLLM — sahip olmadigimiz bir problemi cozuyor

PagedAttention KV cache **fragmentasyonunu** cozer: cok sayida es zamanli
istek bir GPU'yu paylastiginda ortaya cikan bir problem. batch=1 yerel bir
asistanda fragmentasyon yok. vLLM ayrica GPU-oncelikli bir sistem.

### 1-bit / BitNet — iki ayri sey karistiriliyor

- **Mevcut bir modeli 1,58-bit'e cevirmek**: QAT ister, yani yeniden egitim.
  Kapsam disi.
- **BitNet olarak egitilmis bir model kullanmak**: mumkun ve gercek.
  `microsoft/bitnet-b1.58-2B-4T` mevcut ve GGUF'u var.

Yani cevap "evet ama yalnizca ikincisi". Ve 2B'lik bir BitNet, 2B'lik bir
Q4_K_M ile yarisir: kazanc boyuttan (dolayisiyla hizdan) gelir, kalite
karsilastirmasi ayri bir is.

### KV cache quantization — gercek, ama uzun baglamda

llama.cpp'de hazir (`--cache-type-k/v`). Egitim istemez. Ama yukaridaki
tabloya gore 2K'da toplam trafigin %1,8'ini kucultur. 32K'da %22,8'ini —
orada gercekten onemli.

### Speculative decoding — en umut verici test edilmemis kaldirac

llama.cpp `--model-draft` destekliyor. Kucuk bir taslak model birkac token
onerir, buyuk model hepsini **tek seferde** dogrular. Bant genisligine bagli
bir makinede bu, N ardisik agirlik okumasini 1'e indirir.

Elimizde ideal bir cift var: 2B taslak + 4B hedef, ayni aile, ayni tokenizer.
Bu henuz olculmedi ve olculmeli.

### Egitimsiz structured pruning — uygulanabilir

ShortGPT/SliceGPT tarzi katman veya boyut cikarma egitim istemez. Kalite
duser ama olculebilir sekilde. "Yeniden egitim olmadan pruning" ifadesinin
durust karsiligi budur.

### PIM / near-memory — bu makinede donanim yok

Arastirma yonu olarak ilginc; bu dizustunde uygulanabilir degil.

### RAG — hiza etki etmez, kaybedilen bilgiyi geri verir

27B'den 4B'ye inince kaybedilen sey oncelikle parametrik olgusal bilgidir.
RAG bunu diske tasir. Yani RAG bir hiz teknigi degil, **kucuk model secmeyi
mumkun kilan sey**. Bu projede dogru yeri burasi.

## Sonuc

Kaldirac siralamasi, olculen degerlere gore:

1. **Model boyutu** — 27B → 4B: 2,6 kat daha az byte
2. **Quant formati** — IQ2_XXS → Q4_K_M: 4,1 kat daha iyi etkin bant genisligi
3. Ikisi birlikte: **~10,7 kat**, olculdu
4. **Speculative decoding** — henuz olculmedi, sirada bu var
5. Uzun baglam hedefleniyorsa KV quantization
6. Geri kalani bu donanimda ya uygulanabilir degil ya da olcumde gorunmez
