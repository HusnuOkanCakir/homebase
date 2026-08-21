# Makale karar defteri

Bu belge bir literatür özeti değil, 4 GB sınırı için hangi tekniği ne zaman deneyeceğimizi kaydeder. Bağlantılar makale veya resmi proje deposuna gider.

| Teknik | Birincil kaynak | Karar |
|---|---|---|
| GGUF + `llama.cpp` | [`llama.cpp` feature matrix](https://github.com/ggml-org/llama.cpp/wiki/Feature-matrix) | Ana yol: CPU/GPU hibrit offload, quantized KV ve loopback OpenAI-uyumlu servis. Bu lab b10549/full commit'e kilitlidir. |
| Pascal yazılım zinciri | [NVIDIA toolkit/driver/mimari matrisi](https://docs.nvidia.com/datacenter/tesla/drivers/cuda-toolkit-driver-and-architecture-matrix.html) | CUDA 12.9 + R580, `sm_60`/`sm_61`; Pascal'ı dışlayan CUDA 13'e yükseltilmez. |
| vLLM | [resmî GPU kurulum gereksinimleri](https://docs.vllm.ai/en/latest/getting_started/installation/gpu/) | Güncel NVIDIA yolu compute capability 7.5+ istediği için 6.x Pascal hedefinden elenir. |
| GPTQ | [Frantar vd., 2022](https://arxiv.org/abs/2210.17323), [resmi kod](https://github.com/IST-DASLab/gptq) | 3/4-bit kalibrasyon tabanı; 27B'yi 4 GB'a indirmez. |
| AWQ | [Lin vd., 2023](https://arxiv.org/abs/2306.00978), [resmi kod](https://github.com/mit-han-lab/llm-awq) | Hassas kanalları koruma fikri Candidate A bit tahsisine alınır; 4-bit tam model elenir. |
| AQLM | [Egiazarian vd., 2024](https://arxiv.org/abs/2401.06118), [resmi kod](https://github.com/Vahe1994/AQLM) | 2-bit civarı ağırlık deneyi için P4 adayı; Qwen hibrit katman desteği önce küçük surrogate'ta doğrulanmalı. |
| QuIP# | [Tseng vd., 2024](https://arxiv.org/abs/2402.04396), [resmi kod](https://github.com/Cornell-RelaxML/quip-sharp) | Çok düşük bit karşılaştırması; dönüşüm ve kernel maliyeti nedeniyle P4. |
| SparseGPT | [Frantar ve Alistarh, 2023](https://arxiv.org/abs/2301.00774), [resmi kod](https://github.com/IST-DASLab/SparseGPT) | Tek atışlı pruning kontrolü; düzensiz sparsity'nin Pascal'da hız kazandıracağı varsayılmaz. |
| SliceGPT | [Ashkboos vd., 2024](https://arxiv.org/abs/2401.15024), [resmi kod](https://github.com/microsoft/TransformerCompression) | Hidden-width azaltma hipotezi için P4; Gated DeltaNet uyarlaması araştırma işi. |
| Minitron | [Muralidharan vd., 2024](https://arxiv.org/abs/2407.14679), [resmi kod](https://github.com/NVIDIA/Model-Optimizer) | Depth/width/head pruning + kısa toparlama eğitimi Candidate A'nın ana yöntemi; ücretsiz compute ile tam 27B koşusu bloke. |
| QLoRA | [Dettmers vd., 2023](https://arxiv.org/abs/2305.14314), [resmi kod](https://github.com/artidoro/qlora) | Pruning sonrası küçük recovery adaptörü için; temel inference ağırlığını küçültmez. |
| KIVI | [Liu vd., 2024](https://arxiv.org/abs/2402.02750), [resmi kod](https://github.com/jy-yuan/KIVI) | KV 2-bit ancak 4K aşamasından sonra; Qwen3.8 hibrit backend desteği doğrulanmadan etkin değil. |
| FlashAttention | [Dao vd., 2022](https://arxiv.org/abs/2205.14135), [resmi kod](https://github.com/Dao-AILab/flash-attention) | Dao CUDA çekirdekleri Pascal'ı desteklemez. Yalnız `llama.cpp`'nin kendi `--flash-attn` yolu destekleniyor görünürse A/B taranır; ağırlıkları/KV'yi küçültmez. |
| PagedAttention | [Kwon vd., 2023](https://arxiv.org/abs/2309.06180), [vLLM resmi kodu](https://github.com/vllm-project/vllm) | Çoklu istek/fragmentation optimizasyonu; tek kullanıcılı P1 kritik yolunda değil. |
| FlexGen | [Sheng vd., 2023](https://arxiv.org/abs/2303.06865), [resmi kod](https://github.com/FMInference/FlexGen) | CPU/GPU offload tasarımına kanıt; doğrudan Qwen3.8 runtime seçimi değil. |
| BitNet b1.58 | [Ma vd., 2024](https://arxiv.org/abs/2402.17764), [bitnet.cpp](https://github.com/microsoft/BitNet) | Baştan düşük-bit eğitim yönü; hazır BF16 checkpoint'i kayıpsız 1.58-bit modele çevirdiği varsayılmaz. |
| ParetoQ | [Wang vd., 2025](https://arxiv.org/abs/2502.02631) | Makalenin ≤2-bit bölgede PTQ'dan QAT'a geçiş bulgusu nedeniyle Candidate A'da 2-bit hedef ancak recovery/QAT ile değerlendirilir. |
| QTIP | [Tseng vd., 2024](https://arxiv.org/abs/2406.11235) | 2-bit trellis quant karşılaştırması; Qwen hibrit katman ve Pascal kernel desteği kanıtlanana kadar P4. |
| Speculative decoding | [Leviathan vd., 2023](https://proceedings.mlr.press/v202/leviathan23a.html) | İlk deney, ek ağırlık istemeyen `llama.cpp` n-gram yoludur; Qwen MTP sidecar bu fazda kullanılmaz. Ağırlık belleğini azaltmaz ve ancak uçtan uca en az %10 hız kazandırırsa tutulur. |
| LLM in a Flash | [Alizadeh vd., 2023](https://arxiv.org/abs/2312.11514) | SSD/DRAM weight streaming araştırma kolu; Linux/Pascal sunucuda doğrudan uygulanabilir hız sonucu varsayılmaz. |
| PIM-GPT | [Shin vd., 2023](https://arxiv.org/abs/2310.09385) | PIM/NMC gelecek simülasyon work-package'i; mevcut commodity sunucuya deploy yolu değil. |
| BitDistill | [2025](https://arxiv.org/abs/2510.13998) | Düşük-bit distillation adayı; bildirilen kazanımların task-specific olabileceği not edilir, genel amaçlı kaliteye taşındığı varsayılmaz. |

## Modele özgü kaynaklar ve çıkarımlar

- [Resmi model kartı](https://huggingface.co/Qwen/Qwen3.8-27B), [resmi config](https://huggingface.co/Qwen/Qwen3.8-27B/blob/main/config.json) ve [Qwen3.8 kaynak deposu](https://github.com/QwenLM/Qwen3.8) birincil gerçek kaynaktır.
- [Unsloth artefakt deposu](https://huggingface.co/unsloth/Qwen3.8-27B-GGUF) topluluk quant kaynağıdır; Qwen tarafından yayımlanmış native quant değildir. Hareketli `main` dosya sildi/değiştirdiği için deneyler tüm seçili Q2/Q3 artefaktlarının birlikte bulunduğu immutable `313447f257f7ebde0b968e4778feef774546ed81` revision'ını kullanır.
- [ggml-org referansı](https://huggingface.co/ggml-org/Qwen3.8-27B-GGUF) otomatik GGUF dönüşümüdür. Q4 yalnız yüksek bellekli karşılaştırmadır.
- Qwen'in 3 Gated DeltaNet + 1 full-attention düzeninde yalnız attention KV'sini küçültmek bütün bellek sorununu çözmez. GDN recurrent state ve runtime workspace ayrı ölçülür.
- RAG kaliteyi görev bağlamıyla destekleyebilir fakat model ağırlığını küçültmez; compression deneyinden ayrı tutulur. Temel kaynak: [Lewis vd., 2020](https://arxiv.org/abs/2005.11401).

Hiçbir makale sonucu bu donanımda gerçekleşmiş sonuç olarak aktarılmamıştır; her teknik için Qwen3.8/llama.cpp desteği deneysel kapıdır.
