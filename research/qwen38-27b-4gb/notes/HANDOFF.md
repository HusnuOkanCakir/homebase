# Entegrasyon engelleri ve teslim notu

Bu araştırma dalı Homebase koduna bağlanmaz. Gelecekte entegrasyon düşünülürse önce aşağıdaki kapılar kapanmalıdır.

## Açık engeller

- GPU modelinin PCI kimliği/compute capability'si ve gerçek 4 GB kullanılabilir VRAM'i kayıt altına alınmadı.
- Pascal + R580 + CUDA 12.9 ile sabitlenen `llama.cpp` b10549 build'inin CUDA derlemesi ve Qwen3.8 `qwen35` tensor desteği doğrulanmadı; notebook binary `--version` attestation'ı ister.
- Host RAM, swap, boş disk ve disk bant genişliği bilinmiyor. 7,27–13,82 GB Q2/Q3 dosyaları için VRAM dışı kapasite şart.
- Unsloth deposunun `main` dalı hızlı değişiyor; sadece manifestteki revision + SHA kabul edilmeli.
- 4 GB'ta güvenilir `n_gpu_layers`, batch, KV tipi ve context değerleri ölçülmedi.
- Candidate A üretilmedi/eğitilmedi. Sıfır bütçeyle tam distillation compute'u yok.
- Lisans Apache-2.0 olsa da türetilmiş checkpoint yayımlanmadan önce kaynak/NOTICE zinciri yeniden gözden geçirilmeli.
- Resmî-source manifest kaydı bu fazda yalnız immutable revision ile
  `model.safetensors.index.json` dosyasını doğrular. Gelecekte yeni bir quant,
  pruning veya student üretmeden önce 18 BF16 shard'ın tamamı ayrı byte
  boyutu ve SHA-256 ile ek bir conversion-input lock dosyasında sabitlenmelidir.

## Operasyon sınırları

- `/srv/qwen-lab/{models,cache,checkpoints,results}` model alanıdır; Homebase yedek köklerinden açıkça hariç tutulur. Büyük cache'i uygulama verisine symlink etmek yedekleme tehlikesidir.
- Vision projector, MTP sidecar ve 262K context bu fazda yasaktır. Text-only, 2K→4K ilerlenir.
- Paid API/compute çağrısı yoktur. Hugging Face dosya indirme yalnız açık kullanıcı seçimiyle yapılır.
- Ham prompt/çıktılar ve büyük profiller git'e girmez; yalnız anonim küçük özet şeması izlenir.
- Gelecekteki Stage 2 tasarımında model yalnız localhost API istemcisi/servisi
  olarak kalır; shell, doğrudan `hostd` soketi, host mount'u veya ayrıcalıklı
  araç erişimi verilmez.

## Entegrasyona geçiş kabul kriteri

1. Kilitli en az bir Q2/Q3 artefaktı checksum doğrulamasından geçer.
2. Üç tekrarlı 2K ve 4K koşuda OOM/crash yoktur; VRAM/RSS/tok/s kaydı vardır.
3. Sabit görev setinde Türkçe ve İngilizce skorların her biri gerçek Q4/en iyi
   erişilebilir referansın en az %85'ini korur.
4. Servis localhost'a bağlı, kimlik doğrulamalı/erişim kontrollü ve kaynak limitli tasarlanır.
5. Ancak bundan sonra ayrı PR ile OpenAI-uyumlu localhost endpoint ve Homebase adaptörü tartışılır.

Şu anki teslim; araştırma belgeleri, kilit manifesti, donanım/runtime aracı,
CPU/CUDA sanity kapısı, benchmark matrisi, değerlendirme/acceptance hattı ve
Colab probe'udur. Gerçek model/GPU benchmark'ı veya Homebase entegrasyonu
teslimi değildir.
