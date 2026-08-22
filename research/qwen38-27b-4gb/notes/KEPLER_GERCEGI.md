# Bu sunucudaki GPU: olculen gercek

Laboratuvarin varsayimi Pascal sinifi bir GPU, R580 surucusu ve CUDA 12.9'dur
(`config/runtime/requirements.json`). Hedef makinedeki donanim bu degildir ve
fark, deney matrisinin yarisini gecersiz kilacak kadar buyuktur.

## Ne var

| | Varsayilan | Olculen |
|---|---|---|
| GPU | Pascal sinifi | NVIDIA GeForce GT 750M (GK107, **Kepler**) |
| Compute capability | 6.0 / 6.1 | **3.0** |
| Surucu | 580 | 470.256.02 |
| CUDA | 12.9 | 11.4 (yalnizca surucu API'si) |
| VRAM | 3500–4608 MiB | 4039 MiB (uyuyor) |
| CPU | — | i7-4700HQ, 4c/8t, **AVX2 + FMA** |
| RAM | 16 GB sinifi | 15,5 GB (uyuyor) |

Compute capability tahmin edilmedi, surucunun kendisine soruldu: `libcuda.so.1`
uzerinden `cuDeviceGetAttribute` ile. `nvidia-smi --query-gpu=compute_cap`
470 surucusunde gecerli bir alan degildir — README'nin ilk adimi bu makinede
calismaz.

## CUDA yolu kapalidir

llama.cpp CUDA backend'i en az compute capability 5.0 ister. GK107 3.0'dir.

Bu bir derleme bayragiyla asilamaz: CUDA 11 `sm_30` destegini kaldirdi ve 470
surucusu CUDA 11.4'te tavan yapar. `sm_30` uretebilen son toolkit CUDA 10.2'dir;
llama.cpp ise CUDA 11+ ister. Yani hem surucunun kabul ettigi hem de llama.cpp'nin
derlenebildigi bir toolkit yoktur.

## Vulkan yolu aciktir — ve zarar verir

`libnvidia-gl-470-server` kurulduktan sonra Vulkan GPU'yu gorur ve llama.cpp
Vulkan backend'i 4038 MiB serbest bellekle calisir. Ama Kepler'de `fp16: 0`,
`int dot: 0`, `matrix cores: none`.

Qwen3.5-0.8B Q4_0, 4 thread, decode t/s:

| GPU'ya tasinan katman | t/s |
|---:|---:|
| 0 (saf CPU) | **27,31** |
| 4 | 16,34 |
| 8 | 15,30 |
| 16 | 12,88 |
| 99 (tumu) | 10,21 |

Monotonik. Tatli nokta yoktur. Prompt isleme farki daha da buyuktur: CPU'da
141,91 t/s, tam offload'da 4,69 t/s — otuz kat.

## Acik surucu yolu: olculdu

Soru soruldu: CUDA ve NVIDIA'nin kapali surucusu olmadan, nouveau + NVK ile
devam edebilir miyiz? Cevap iki parcali.

**Calisiyor.** `nvidia` modulu kaldirilip nouveau yuklendiginde Vulkan aygiti
`GeForce GT 750M (NVK GK107)` olarak gorunur, `driverID = DRIVER_ID_MESA_NVK`,
Mesa 25.2.8, cekirdek 6.8. llama.cpp onu bulur ve 3686 MiB serbest bellek
bildirir. Tek satir NVIDIA kapali kodu calismaz.

**Ama dort kat yavastir.** Ayni sweep, ayni model:

| GPU'ya tasinan katman | proprietary | NVK |
|---:|---:|---:|
| 0 (saf CPU) | 27,31 | 15,79 |
| 4 | 16,34 | 4,64 |
| 8 | 15,30 | 3,93 |
| 16 | 12,88 | 3,26 |
| 99 (tumu) | 10,21 | **2,62** |

NVK turu sirasinda makine mesguldu (load ~1,9), bu yuzden mutlak degerler degil
oranlar okunmalidir: o kosullarda saf CPU tabani 14,34 t/s olculdu, NVK'nin
ngl=0 degeri 15,79 ile bununla tutarli. Tam offload'da NVK proprietary'nin
%26'sidir.

Bir gecis kriteri "NVK proprietary'nin en az %60-70'ine ulasmali" derse bu
makinede gecmez. Daha onemlisi: proprietary Vulkan zaten CPU'dan yavas oldugu
icin, NVK %100'e ulassaydi bile sonuc degismezdi.

**Yan etki, ve olumlu olani:** nouveau karti dusuk saatlerde tutar. Kapali
surucu karti P0'da tutuyordu ve makine ~65 C'de bosta duruyordu; nouveau ile
onceki taban ~52 C idi. Bu, GPU'yu inference icin degil, makineyi serin tutmak
icin nouveau'da birakmanin gecerli bir sebebidir.

## Laboratuvar icin sonuc

Asama matrisindeki 2, 3 ve R adimlari ("CUDA auto/offload", "Pascal offload
siniri", "yuksek bellekte referans") bu makinede uygulanamaz. Kalan yol saf
CPU'dur ve bu kotu bir haber degildir: Haswell'in AVX2 yolu
(`libggml-cpu-haswell.so` otomatik secilir) bu GPU'dan hizlidir.

`doctor` komutunun `fail` vermesi dogrudur ve duzeltilmemelidir. Gate'i makineye
uydurmak, olcumu varsayima uydurmak olur.

Olcumler: [`results/gt750m-kepler-vulkan-vs-cpu.json`](../results/gt750m-kepler-vulkan-vs-cpu.json).
